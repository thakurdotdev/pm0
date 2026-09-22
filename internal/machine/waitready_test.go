//go:build linux

package machine

import (
	"syscall"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/test/harness"
)

// waitReadyConfig returns a wait_ready app running fakechild with args.
func waitReadyConfig(name string, args ...string) config.App {
	cfg := config.DefaultFor(name, fakechild)
	cfg.Args = args
	cfg.WaitReady = true
	return cfg
}

// M5 row 19: a wait_ready app holds `launching` after spawn and flips to
// `online` only when the child sends the node-IPC string "ready".
func TestWaitReadyReadyUnlocksOnline(t *testing.T) {
	a, barrier := newApp(t, waitReadyConfig(harness.UniqueName("m5-ready"), "--send-ready"))

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	harness.WaitBarrier(t, barrier, 5*time.Second) // handlers installed; ready sent right after

	// Launch() returned while the gate was still armed: status must have
	// been launching at that point — and MUST NOT be stuck there now that
	// the child sent 'ready'.
	waitStatus(t, a, 3*time.Second, StatusOnline)

	// The gate is one-shot: status stays online (no flapping when the child
	// exits later, and the readyTimer no longer exists).
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if s := a.Snapshot(); s.Status != StatusStopped {
		t.Fatalf("after stop: %s", s.Status)
	}
}

// M5 rows 18/19: a wait_ready app that never sends 'ready' is forced
// online when listen_timeout elapses (observed pm2 fork-mode ready timer).
func TestWaitReadyForcedOnlineAtListenTimeout(t *testing.T) {
	cfg := waitReadyConfig(harness.UniqueName("m5-timeout")) // no --send-ready
	cfg.ListenTimeout = 400 * time.Millisecond
	a, _ := newApp(t, cfg)

	start := time.Now()
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Well inside the window: still launching.
	time.Sleep(150 * time.Millisecond)
	if s := a.Snapshot(); s.Status != StatusLaunching {
		t.Fatalf("status %s after 150ms, want launching (gate must hold)", s.Status)
	}

	waitStatus(t, a, 3*time.Second, StatusOnline)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("forced online after %v, want ~listen_timeout (400ms)", d)
	}

	// Forced online is terminal for the gate: the child keeps running.
	if err := syscall.Kill(a.Snapshot().Pid, 0); err != nil {
		t.Fatalf("child not alive after force-online: %v", err)
	}
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

// M5 row 19 interplay with the restart policy: a crash DURING the ready
// wait is a crash like any other — onExit fires from launching and the
// crash-loop budget applies (max_restarts unstable exits -> errored).
func TestWaitReadyCrashWhileLaunching(t *testing.T) {
	cfg := waitReadyConfig(harness.UniqueName("m5-crash"),
		"--exit-after", "120", "--exit-code", "9")
	cfg.ListenTimeout = 10 * time.Second // never reached: exits first
	cfg.MinUptime = time.Second
	cfg.MaxRestarts = 2
	a, _ := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := waitStatus(t, a, 8*time.Second, StatusErrored)
	// relaunch after crash #1, parked at #2 (max_restarts=2): restart_time 1.
	if snap.RestartTime != 1 {
		t.Fatalf("restart_time = %d, want 1", snap.RestartTime)
	}
	if snap.ExitCode == nil || *snap.ExitCode != 9 {
		t.Fatalf("exit_code = %v, want 9", snap.ExitCode)
	}
}

// A wait_ready app stopped while still launching parks stopped (the stop
// path must not hang waiting for ready or the deadline).
func TestWaitReadyStopDuringLaunching(t *testing.T) {
	cfg := waitReadyConfig(harness.UniqueName("m5-stop"))
	cfg.ListenTimeout = 10 * time.Second
	a, barrier := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	harness.WaitBarrier(t, barrier, 5*time.Second)
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop during launching: %v", err)
	}
	if s := a.Snapshot(); s.Status != StatusStopped {
		t.Fatalf("status %s, want stopped", s.Status)
	}
}
