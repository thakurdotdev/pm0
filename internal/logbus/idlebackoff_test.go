//go:build linux

package logbus

// Idle-backoff tests (perf queue: idle CPU at scale). The pump must
// stay correct across cadence changes: capture latency stays bounded by
// maxIdlePoll while idle, snapping back to the base cadence on data or
// on subscriber attach.

import (
	"path/filepath"
	"testing"
	"time"
)

func newIdleTestRoute(t *testing.T, path string) *Route {
	t.Helper()
	return NewRoute(Options{
		OutPath:      path,
		PollInterval: 10 * time.Millisecond,
		// rotation off: this file only exercises pump cadence
		RotateMaxBytes: 0,
	})
}

func backlogHas(t *testing.T, r *Route, needle string) bool {
	t.Helper()
	for _, e := range r.Backlog(64, true) {
		if e.Data == needle {
			return true
		}
	}
	return false
}

// TestPumpCapturesAcrossBackoff: the sink is created after the route
// (pump sees ENOENT and backs off), goes silent long enough to widen
// past the base cadence, then produces lines. Capture must still happen
// — bounded by maxIdlePoll, not by the backoff depth of "never".
func TestPumpCapturesAcrossBackoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	r := newIdleTestRoute(t, path)
	defer r.Close()

	// Deep idle: silence (and a missing file) for well over the widening
	// window.
	time.Sleep(1300 * time.Millisecond)

	mustAppend(t, path, "after-idle-1\n")
	deadline := time.Now().Add(maxIdlePoll + 500*time.Millisecond)
	for time.Now().Before(deadline) {
		if backlogHas(t, r, "after-idle-1") {
			// Snapped back: the next line must arrive at base cadence.
			mustAppend(t, path, "after-idle-2\n")
			deadline2 := time.Now().Add(200 * time.Millisecond)
			for time.Now().Before(deadline2) {
				if backlogHas(t, r, "after-idle-2") {
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			t.Fatal("second line not captured at snapped-back cadence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("line written after deep idle was not captured")
}

// TestPumpWakeOnSubscribe: a subscriber attaching during deep idle
// snaps the pump back, so the first line arrives promptly instead of
// after up to maxIdlePoll.
func TestPumpWakeOnSubscribe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	r := newIdleTestRoute(t, path)
	defer r.Close()

	time.Sleep(1300 * time.Millisecond) // deep idle

	_ = r.Subscribe() // wake signal fires
	mustAppend(t, path, "wake-line\n")

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if backlogHas(t, r, "wake-line") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("line not captured promptly after subscriber attach (wake missed)")
}

// TestPumpActiveStaysFast: while data flows, cadence stays at the base —
// a stream of lines is captured promptly throughout (guards against a
// regression where backoff engages mid-traffic).
func TestPumpActiveStaysFast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.log")
	r := newIdleTestRoute(t, path)
	defer r.Close()
	sub := r.Subscribe()
	count := 0
	deadline := time.Now().Add(2 * time.Second)
	for count < 50 && time.Now().Before(deadline) {
		mustAppend(t, path, "tick\n")
		count++
		select {
		case <-sub.C():
		case <-time.After(300 * time.Millisecond):
			t.Fatalf("line %d not captured within 300ms of active flow", count)
		}
	}
}

// TestSplitCompletePrealloc — behavioral parity after the prealloc
// change: exact line splits, trailing partial preserved, empty input.
func TestSplitCompletePrealloc(t *testing.T) {
	lines, partial := SplitComplete([]byte("a\nbb\nccc\n"))
	if len(lines) != 3 || lines[0] != "a" || lines[1] != "bb" || lines[2] != "ccc" || len(partial) != 0 {
		t.Fatalf("got (%v, %q)", lines, partial)
	}
	lines, partial = SplitComplete([]byte("a\nbb"))
	if len(lines) != 1 || lines[0] != "a" || string(partial) != "bb" {
		t.Fatalf("got (%v, %q)", lines, partial)
	}
	if lines, partial = SplitComplete(nil); len(lines) != 0 || len(partial) != 0 {
		t.Fatalf("empty input: got (%v, %q)", lines, partial)
	}
}
