//go:build linux

package daemon

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/logbus"
	"github.com/pm0/pm0/test/harness"
	"google.golang.org/grpc"
)

type blockedBacklogStream struct {
	grpc.ServerStream
	ctx     context.Context
	gate    chan struct{}
	entered chan struct{}
	lines   chan string
	first   bool
}

func (s *blockedBacklogStream) Context() context.Context { return s.ctx }

func (s *blockedBacklogStream) Send(line *v1.LogLine) error {
	if !s.first {
		s.first = true
		close(s.entered)
		select {
		case <-s.gate:
		case <-s.ctx.Done():
			return s.ctx.Err()
		}
	}
	select {
	case s.lines <- string(line.GetData()):
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func TestStreamLogsDeliversSubscriberDropMarker(t *testing.T) {
	srv, c := startServer(t)
	id := startViaClient(t, c, harness.UniqueName("drop-marker"), "--exit-after", "60000")
	view, _ := srv.sup.Describe(fmt.Sprint(id))
	srv.closeRoute(int(id))
	var drops atomic.Int64
	route := logbus.NewRoute(logbus.Options{
		OutPath: view.Config.OutFile, SubBuffer: 2, PollInterval: 5 * time.Millisecond,
		OnDrop: func(kind string, _ logbus.Stream, n int) {
			if kind == "subscriber" {
				drops.Add(int64(n))
			}
		},
	})
	srv.mu.Lock()
	srv.routes[int(id)] = route
	srv.mu.Unlock()
	defer srv.closeRoute(int(id))
	writer, err := os.OpenFile(view.Config.OutFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.WriteString("seed\n"); err != nil {
		t.Fatal(err)
	}
	waitForRoute(t, 5*time.Second, "seed backlog", func() bool { return len(route.BacklogAll(false)) == 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream := &blockedBacklogStream{ctx: ctx, gate: make(chan struct{}), entered: make(chan struct{}), lines: make(chan string, 32)}
	done := make(chan error, 1)
	go func() {
		done <- srv.StreamLogs(&v1.StreamLogsRequest{Selector: &v1.Selector{PmIds: []int32{id}}, Lines: 1, Follow: true}, stream)
	}()
	select {
	case <-stream.entered:
	case <-ctx.Done():
		t.Fatal("stream did not start backlog delivery")
	}
	var flood strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&flood, "line-%d\n", i)
	}
	if _, err := writer.WriteString(flood.String()); err != nil {
		t.Fatal(err)
	}
	waitForRoute(t, 5*time.Second, "subscriber overflow", func() bool { return drops.Load() == 8 })
	close(stream.gate)
	for _, want := range []string{"seed", "line-0", "line-1"} {
		select {
		case got := <-stream.lines:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("missing buffered line %q", want)
		}
	}
	if _, err := writer.WriteString("after\n"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[pm0: 8 stdout lines dropped]", "after"} {
		select {
		case got := <-stream.lines:
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("missing control/live line %q", want)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
}
