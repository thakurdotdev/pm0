//go:build linux

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/logbus"
	"github.com/pm0/pm0/internal/supervisor"
	"github.com/pm0/pm0/test/harness"
)

func entry(seq uint64, st logbus.Stream, at time.Time, data string) logbus.Entry {
	return logbus.Entry{Seq: seq, Stream: st, At: at, Data: data}
}

func datas(ls []logLineEntry) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = string(l.line.GetData())
	}
	return out
}

func TestBacklogTailInterleavesStreams(t *testing.T) {
	t0 := time.Unix(1000, 0)
	v := supervisor.AppView{ID: 1, Config: config.App{Name: "a"}}
	all := []logbus.Entry{
		entry(1, logbus.Stdout, t0, "o1"),
		entry(2, logbus.Stderr, t0, "e1"),
		entry(3, logbus.Stderr, t0, "e2"),
		entry(4, logbus.Stdout, t0, "o2"),
		entry(5, logbus.Stderr, t0, "e3"),
		entry(6, logbus.Stdout, t0, "o3"),
	}
	// Last 2 of each stream, still in write order; newest line last.
	if got, want := datas(backlogTail(v, all, 2, true)), []string{"e2", "o2", "e3", "o3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tail = %v, want %v", got, want)
	}
	if got, want := datas(backlogTail(v, all, 2, false)), []string{"o2", "o3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stdout-only tail = %v, want %v", got, want)
	}
}

func TestMergeByArrivalAcrossApps(t *testing.T) {
	t0 := time.Unix(1000, 0)
	mk := func(name string, offs ...int) []logLineEntry {
		var out []logLineEntry
		for i, o := range offs {
			out = append(out, logLineEntry{
				line: &v1.LogLine{Data: []byte(name + string(rune('0'+i)))},
				at:   t0.Add(time.Duration(o) * time.Millisecond),
			})
		}
		return out
	}
	// b1 shares a timestamp with a0 (tie -> earlier app first); a1 and a2
	// share one too and must keep their own order.
	got := datas(mergeByArrival([][]logLineEntry{mk("a", 0, 30, 30), mk("b", 0, 10, 40)}))
	want := []string{"a0", "b0", "b1", "a1", "a2", "b2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge = %v, want %v", got, want)
	}
}

// TestDaemonLogsBacklogWriteOrder: the backlog replays stdout and stderr in
// the order the app wrote them; the newest line prints last.
func TestDaemonLogsBacklogWriteOrder(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("order")
	script := filepath.Join(t.TempDir(), "order.sh")
	body := "#!/bin/sh\necho out-1\nsleep 0.5\necho err-1 >&2\nsleep 0.5\necho out-2\nsleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start([]*v1.ProcessSpec{{Name: name, Script: script, Autorestart: true, Cwd: t.TempDir()}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	var got []string
	waitFor(t, 8*time.Second, "all three lines in the backlog", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		lines, errCh, err := c.StreamLogs(ctx, &v1.Selector{Names: []string{name}}, 10, true, true, false)
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		got = got[:0]
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					return len(got) == 3
				}
				got = append(got, string(l.GetData()))
			case e := <-errCh:
				if e != nil && !strings.Contains(e.Error(), "EOF") {
					t.Fatalf("stream error: %v", e)
				}
				for l := range lines {
					got = append(got, string(l.GetData()))
				}
				return len(got) == 3
			}
		}
	})
	if want := []string{"out-1", "err-1", "out-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backlog order = %v, want %v", got, want)
	}
}
