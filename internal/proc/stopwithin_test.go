//go:build linux

package proc

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestStopWithinAdoptedLeaderNoExitEvent: an ADOPTED leader (daemon
// restarted; the tree reparented away) is invisible to wait4, so its exit
// arrives only from the daemon's 1s poller. The kill path must not wait on
// that event: it checks the leader's /proc identity itself.
//
// Simulated by hand-building a Handle whose done channel never closes —
// exactly what an adopted non-child leader looks like to StopWithin.
func TestStopWithinAdoptedLeaderNoExitEvent(t *testing.T) {
	l := newTestLauncher(t, ModePgid)
	name := uniqueAppName("adopt-sim")
	idFile := filepath.Join(t.TempDir(), "leader.id")
	launch(t, l, name, "--report-pid", idFile)
	pid, starttime := readIdentity(t, idFile, 5*time.Second)

	h := &Handle{
		pid:         pid,
		starttime:   starttime,
		mode:        ModePgid,
		killSignal:  syscall.SIGINT,
		killTimeout: testKillTimeout,
		done:        make(chan struct{}), // never closed: no exit event
	}

	start := time.Now()
	if err := h.StopWithin(time.Now().Add(testKillTimeout)); err != nil {
		t.Fatalf("StopWithin: %v", err)
	}
	// fakechild exits on SIGINT. Pre-fix, the missing exit event stalled
	// the graceful phase for the whole kill_timeout (600ms) and the
	// post-force wait for GracePeriod (2s); the /proc identity check
	// returns in tens of ms.
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("adopted stop took %v; the kill path waited on an exit event that cannot arrive", elapsed)
	}
}

// TestStopWithinPastDeadlineForces: a deadline already elapsed skips the
// graceful window and drives the force path. This is the primitive the
// reload path uses to spend ONE kill_timeout on the node-IPC 'shutdown'
// handoff and the signal kill together instead of one budget per phase.
func TestStopWithinPastDeadlineForces(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			name := uniqueAppName("stopwithin-past")
			idFile := filepath.Join(t.TempDir(), "leader.id")
			h := launch(t, l, name, "--report-pid", idFile)
			_, _ = readIdentity(t, idFile, 5*time.Second)

			start := time.Now()
			if err := h.StopWithin(time.Now().Add(-time.Second)); err != nil {
				t.Fatalf("StopWithin: %v", err)
			}
			// Post-deadline stops must not re-open a fresh graceful window.
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("StopWithin(past deadline) took %v; killed lazily", elapsed)
			}
			assertTreeEmpty(t, h)
		})
	}
}

// TestStopWithinReturnsOnExit: a far-future deadline still returns as soon
// as the child dies — the early-exit half of the contract that keeps a
// cooperating app's reload in milliseconds.
func TestStopWithinReturnsOnExit(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			name := uniqueAppName("stopwithin-exit")
			idFile := filepath.Join(t.TempDir(), "leader.id")
			h := launch(t, l, name, "--report-pid", idFile)
			_, _ = readIdentity(t, idFile, 5*time.Second)

			start := time.Now()
			// fakechild exits on SIGINT, so this 30s window must not be
			// consumed: the exit select has to fire on h.done.
			if err := h.StopWithin(time.Now().Add(30 * time.Second)); err != nil {
				t.Fatalf("StopWithin: %v", err)
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Fatalf("StopWithin returned in %v; did not observe the child exit", elapsed)
			}
			assertTreeEmpty(t, h)
		})
	}
}
