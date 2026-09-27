//go:build linux

package machine

import (
	"testing"
	"time"

	"github.com/pm0/pm0/test/harness"
)

// TestReloadHandoffGating: the reload IPC 'shutdown' handoff is armed only
// for cluster workers — the only children that can read NODE_CHANNEL_FD and
// act on it. Arming it for fork apps (Bun, Python, Go, shell, plain node)
// made every roll wait a full kill_timeout for a frame nobody reads.
func TestReloadHandoffGating(t *testing.T) {
	l := harness.NewLauncher(t)

	forkCfg := harness.SleepConfig(harness.UniqueName("gate-fork"))
	forkCfg.ExecMode = "fork"
	fork, err := New(forkCfg, l, Options{ID: 0})
	if err != nil {
		t.Fatalf("New(fork): %v", err)
	}
	t.Cleanup(func() { retireActor(fork) })
	if fork.ipcHandoff {
		t.Fatalf("fork app armed the IPC handoff; it can never be read")
	}

	clusterCfg := harness.SleepConfig(harness.UniqueName("gate-cluster"))
	clusterCfg.ExecMode = "cluster"
	cluster, err := New(clusterCfg, l, Options{ID: 1})
	if err != nil {
		t.Fatalf("New(cluster): %v", err)
	}
	t.Cleanup(func() { retireActor(cluster) })
	if !cluster.ipcHandoff {
		t.Fatalf("cluster app did not arm the IPC handoff")
	}
}

// TestDeleteGracefulForkIsFast: a fork app must not pay the IPC handoff
// wait. With a 5s kill_timeout the old double-budget path spent >=5s on the
// handoff alone before signaling; the shared-budget path signals
// immediately, and fakechild (exits on SIGINT) is gone in milliseconds.
func TestDeleteGracefulForkIsFast(t *testing.T) {
	l := harness.NewLauncher(t)
	cfg := harness.SleepConfig(harness.UniqueName("fast-fork"))
	cfg.KillTimeout = 5 * time.Second
	cfg, barrier := harness.WithSignalBarrier(cfg)

	a, err := New(cfg, l, Options{ID: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { retireActor(a) })
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	harness.WaitBarrier(t, barrier, 5*time.Second)

	start := time.Now()
	if err := a.DeleteGraceful(); err != nil {
		t.Fatalf("DeleteGraceful: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("fork DeleteGraceful took %s; the IPC handoff wait was not skipped", elapsed)
	}
}

// TestDeleteGracefulClusterDrains: a cluster worker DOES get the shared
// window for the 'shutdown' handoff. fakechild ignores the frame, so the
// budget is consumed, then the signal finishes the kill — the drain window
// is preserved for the apps that can use it.
func TestDeleteGracefulClusterDrains(t *testing.T) {
	l := harness.NewLauncher(t)
	cfg := harness.SleepConfig(harness.UniqueName("drain-cluster"))
	cfg.ExecMode = "cluster"
	cfg.KillTimeout = 400 * time.Millisecond
	cfg, barrier := harness.WithSignalBarrier(cfg)

	a, err := New(cfg, l, Options{ID: 0})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { retireActor(a) })
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	harness.WaitBarrier(t, barrier, 5*time.Second)

	start := time.Now()
	if err := a.DeleteGraceful(); err != nil {
		t.Fatalf("DeleteGraceful: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond {
		t.Fatalf("cluster DeleteGraceful returned in %s; the drain window was skipped", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("cluster DeleteGraceful took %s; not bounded by the shared budget", elapsed)
	}
}

// retireActor tears an actor down if it is still running (Delete blocks on
// a retired actor's mailbox, so guard on Done).
func retireActor(a *App) {
	select {
	case <-a.Done():
	default:
		_ = a.Delete()
	}
}
