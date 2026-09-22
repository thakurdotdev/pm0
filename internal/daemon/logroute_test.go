//go:build linux

// M3 log-router integration: real fakechild floods through a real daemon
// and the I8 guarantees are asserted end to end — bounded ring, counted
// evictions, no silent loss, ring continuity across app restarts.
package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/logbus"
	"github.com/pm0/pm0/pkg/client"
	"github.com/pm0/pm0/test/harness"
)

// routeOf fetches the server's route for a pm_id (same package: direct
// map access under the server lock).
func routeOf(srv *Server, id int32) *logbus.Route {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	return srv.routes[int(id)]
}

// waitForRoute polls cond until it holds or the deadline expires.
func waitForRoute(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// collectStream attaches a StreamLogs client and drains lines until
// stopWhen holds (then cancels) or the deadline hits.
func collectStream(t *testing.T, c *client.Client, sel *v1.Selector, lines int32, stderr, follow bool, stopWhen func(got []string) bool, maxWait time.Duration) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), maxWait)
	defer cancel()
	ch, errCh, err := c.StreamLogs(ctx, sel, lines, stderr, true, follow)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var got []string
	deadline := time.After(maxWait)
	for {
		if stopWhen(got) {
			cancel()
			return got
		}
		select {
		case line, ok := <-ch:
			if !ok {
				return got
			}
			got = append(got, string(line.GetData()))
		case e := <-errCh:
			if e != nil && !strings.Contains(e.Error(), "EOF") && !strings.Contains(e.Error(), "context") {
				t.Fatalf("stream error: %v", e)
			}
			// The stream ended; the producer may still hold buffered
			// lines in ch — drain them before returning.
			for {
				select {
				case line, ok := <-ch:
					if !ok {
						return got
					}
					got = append(got, string(line.GetData()))
				default:
					return got
				}
			}
		case <-deadline:
			return got
		}
	}
}

// TestDaemonLogFloodRingBacklog (I8 end to end): 20k flooded lines leave
// the ring bounded with evictions counted, the newest lines obtainable
// via StreamLogs backlog, and the FILES carrying the full history (the
// authoritative sink) — nothing silently lost anywhere.
func TestDaemonLogFloodRingBacklog(t *testing.T) {
	srv, c := startServer(t)
	name := harness.UniqueName("flood")
	const total = 20000
	id := startViaClient(t, c, name, "--flood", strconv.Itoa(total), "--exit-after", "60000")

	v, ok := srv.sup.Describe(fmt.Sprint(id))
	if !ok {
		t.Fatal("app not registered")
	}

	// The file sink must hold the FULL history.
	waitForRoute(t, 15*time.Second, "all flood lines in file", func() bool {
		data, err := os.ReadFile(v.Config.OutFile)
		if err != nil {
			return false
		}
		return strings.Count(string(data), "\n") >= total
	})

	// Ring catches up to the tail of the flood (pump poll + bounded cap).
	waitForRoute(t, 10*time.Second, "ring holds the flood tail", func() bool {
		r := routeOf(srv, id)
		if r == nil {
			return false
		}
		bl := r.BacklogAll(true)
		return len(bl) > 0 && bl[len(bl)-1].Data == fmt.Sprintf("out-%d", total-1)
	})

	r := routeOf(srv, id)
	if st := r.Stats(); st.Evicted == 0 {
		t.Fatal("evictions = 0: ring did not bound itself")
	}
	if bl := r.BacklogAll(true); len(bl) > logbus.DefaultRingLines {
		t.Fatalf("ring holds %d entries, cap %d", len(bl), logbus.DefaultRingLines)
	}

	// StreamLogs backlog serves the bounded tail: 50 lines, all from the
	// newest flood window.
	got := collectStream(t, c, &v1.Selector{PmIds: []int32{id}}, 50, false, false,
		func(got []string) bool { return len(got) >= 50 }, 10*time.Second)
	if len(got) < 50 {
		t.Fatalf("backlog short: %d lines", len(got))
	}
	re := regexp.MustCompile(`^out-(\d+)$`)
	minIdx := total
	for _, line := range got[:50] {
		m := re.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("non-flood backlog line %q", line)
		}
		idx, _ := strconv.Atoi(m[1])
		if idx < minIdx {
			minIdx = idx
		}
	}
	// Ring cap 1000 + requested 50: the deepest reachable line is bounded.
	if minIdx < total-logbus.DefaultRingLines-50 {
		t.Fatalf("backlog reached too deep (out-%d): ring not bounding backlog", minIdx)
	}
}

// TestDaemonRotationUnderFlood: rotation ships ON through the daemon's
// route factory (compat divergence 12, fixed 10 MB constants until M4).
// Flooding past the threshold must produce a rotated copy holding the
// pre-rotation history, truncate the live file, keep tailing, and keep
// the flood tail reachable via the ring.
func TestDaemonRotationUnderFlood(t *testing.T) {
	srv, c := startServer(t)
	name := harness.UniqueName("rotflood")
	const total = 1200000 // ~11 MB of "out-<i>" lines > 10 MB threshold
	id := startViaClient(t, c, name, "--flood", strconv.Itoa(total), "--exit-after", "120000")

	v, ok := srv.sup.Describe(fmt.Sprint(id))
	if !ok {
		t.Fatal("app not registered")
	}

	// Rotation fires on a pump tick once the sink crosses the threshold.
	waitForRoute(t, 30*time.Second, "rotation under flood", func() bool {
		r := routeOf(srv, id)
		return r != nil && r.Stats().Rotations >= 1
	})

	// A rotated copy exists and holds real content.
	entries, err := os.ReadDir(filepath.Dir(v.Config.OutFile))
	if err != nil {
		t.Fatal(err)
	}
	rotated := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), filepath.Base(v.Config.OutFile)+".") {
			rotated++
			if st, ierr := e.Info(); ierr != nil || st.Size() == 0 {
				t.Fatalf("rotated file %s empty/unreadable", e.Name())
			}
		}
	}
	if rotated == 0 {
		t.Fatal("no rotated file after rotation")
	}

	// The live file was truncated: below the threshold even though the
	// app wrote ~11 MB in total.
	if st, err := os.Stat(v.Config.OutFile); err != nil {
		t.Fatal(err)
	} else if st.Size() >= int64(logbus.DefaultRotateMax) {
		t.Fatalf("live file has %d bytes, rotation did not truncate", st.Size())
	}

	// The ring stays bounded and still holds flood lines. Note the
	// tail is NOT asserted to be out-<total-1>: copytruncate's loss
	// window (bytes appended between the final read and the truncate,
	// divergence 12) can swallow the very end of the flood — the
	// post-rotation capture property is covered deterministically by
	// the logbus unit tests.
	waitForRoute(t, 15*time.Second, "bounded ring after flood", func() bool {
		r := routeOf(srv, id)
		if r == nil {
			return false
		}
		bl := r.BacklogAll(true)
		if len(bl) == 0 || len(bl) > logbus.DefaultRingLines {
			return false
		}
		re := regexp.MustCompile(`^out-\d+$`)
		for _, e := range bl {
			if !re.MatchString(e.Data) {
				return false
			}
		}
		return true
	})
}

// TestDaemonRingSpansRestart: the route lifecycle binds to the registry
// entry, not the child process — after a restart the streamed backlog
// still contains the previous generation's lines.
func TestDaemonRingSpansRestart(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("ringrestart")

	// Each run prints generation-<n> with n from a counter file, so the
	// post-restart generation is distinguishable from the first.
	dir := t.TempDir()
	script := filepath.Join(dir, "gen.sh")
	cnt := filepath.Join(dir, "cnt")
	body := "#!/bin/sh\nn=$(cat \"$0.cnt\" 2>/dev/null || echo 0); n=$((n+1)); echo $n > \"$0.cnt\"; echo generation-$n; exec sleep 60\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = cnt

	sel := &v1.Selector{Names: []string{name}}
	if _, err := c.Start([]*v1.ProcessSpec{{
		Name:   name,
		Script: script,
		Cwd:    dir,
	}}); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitForRoute(t, 10*time.Second, "first generation captured", func() bool {
		got := collectStream(t, c, sel, 50, false, false,
			func(got []string) bool {
				return strings.Contains(strings.Join(got, "\n"), "generation-1")
			}, 3*time.Second)
		return strings.Contains(strings.Join(got, "\n"), "generation-1")
	})

	if _, err := c.Restart(sel, nil); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// After the restart, one backlog attach must show BOTH generations:
	// the pre-restart line from the surviving ring, the post-restart line
	// from the new process.
	waitForRoute(t, 15*time.Second, "ring spans restart", func() bool {
		got := collectStream(t, c, sel, 100, false, false,
			func(got []string) bool {
				joined := strings.Join(got, "\n")
				return strings.Contains(joined, "generation-1") && strings.Contains(joined, "generation-2")
			}, 4*time.Second)
		joined := strings.Join(got, "\n")
		return strings.Contains(joined, "generation-1") && strings.Contains(joined, "generation-2")
	})
}
