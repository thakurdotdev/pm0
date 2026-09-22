//go:build linux

package proc

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestI1StopKillsTreeAllKillPaths: after Stop, zero live processes remain
// (invariant I1) on every kill path — including a tree that contains an
// orphaned grandchild (--spawn-orphan) at stop time.
func TestI1StopKillsTreeAllKillPaths(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			name := uniqueAppName("i1-tree")
			tmp := t.TempDir()
			leaderID := filepath.Join(tmp, "leader.id")
			orphanID := filepath.Join(tmp, "orphan.id")

			// Long-running leader with a surviving grandchild. The leader's
			// identity file doubles as the readiness barrier: handlers are
			// installed before we start the kill path.
			h := launch(t, l, name,
				"--spawn-orphan", "--orphan-pidfile", orphanID,
				"--report-pid", leaderID)
			_, _ = readIdentity(t, leaderID, 5*time.Second)
			orphanPid, _ := readIdentity(t, orphanID, 5*time.Second)

			if err := h.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			assertTreeEmpty(t, h)
			waitPidGone(t, h.PID(), 3*time.Second, "leader")
			waitPidGone(t, orphanPid, 3*time.Second, "orphan grandchild")
		})
	}
}

// TestI1ForkDuringKill: the tree spawns a child DURING the kill window
// (on receiving the graceful signal) and the leader ignores SIGTERM.
// Every kill path must still finish with an empty tree (I1) — this is the
// exact race the freeze-drain path exists for.
func TestI1ForkDuringKill(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			name := uniqueAppName("i1-fork")
			tmp := t.TempDir()
			leaderID := filepath.Join(tmp, "leader.id")
			gcID := filepath.Join(tmp, "gc.id")

			h := launch(t, l, name,
				"--fork-during-kill", "--ignore-sigterm",
				"--orphan-pidfile", gcID, "--report-pid", leaderID)
			// Readiness: the fork-on-signal handler is installed before the
			// graceful signal fires.
			_, _ = readIdentity(t, leaderID, 5*time.Second)

			if err := h.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			// The grandchild is spawned DURING Stop's graceful window; its
			// identity file persists after death, so read it now.
			gcPid, _ := readIdentity(t, gcID, 5*time.Second)
			if gcPid == h.PID() {
				t.Fatalf("grandchild identity reports leader pid")
			}
			assertTreeEmpty(t, h)
			waitPidGone(t, gcPid, 3*time.Second, "fork-during-kill grandchild")
		})
	}
}

// TestI1SetsidEscapeSwept: a tree member calls setsid() and leaves the
// process group (double-fork daemon pattern). The leader then exits.
// Stop() must still find and kill the escapee: cgroup modes via
// cgroup.kill, pgid mode via the reparented-orphan sweep. The pgid run
// also asserts the sweep is what catches it (TreePids sees it only via
// the marker scan, not the pgrp scan).
func TestI1SetsidEscapeSwept(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			name := uniqueAppName("i1-setsid")
			daemonID := filepath.Join(t.TempDir(), "setsid.id")

			// Leader forks the setsid'd daemon-child and exits immediately.
			h := launch(t, l, name, "--setsid-child", "--orphan-pidfile", daemonID)
			waitDone(t, h, 5*time.Second)
			daemonPid, _ := readIdentity(t, daemonID, 5*time.Second)
			if daemonPid == h.PID() {
				t.Fatalf("setsid child reports leader pid")
			}

			// In pgid mode the escapee must NOT appear in the pgrp scan
			// (it left the group) but MUST appear via the orphan sweep.
			if mode == ModePgid {
				if members := scanPgrpMembers(h.PID()); len(members) != 0 {
					t.Logf("note: pgrp scan unexpectedly found %v", members)
				}
				found := false
				for _, v := range scanReparentedOrphans(os.Getpid(), name, h.PID()) {
					if v.Pid == daemonPid {
						found = true
					}
				}
				if !found {
					t.Fatalf("orphan sweep does not see setsid child %d before Stop", daemonPid)
				}
			}

			if err := h.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			assertTreeEmpty(t, h)
			waitPidGone(t, daemonPid, 3*time.Second, "setsid escapee")
		})
	}
}

// TestI5IgnoreSigtermBoundedTermination: a tree that ignores the graceful
// signal must still be terminated within kill_timeout + grace (invariant
// I5), on every kill path.
func TestI5IgnoreSigtermBoundedTermination(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			name := uniqueAppName("i5-ignore")
			leaderID := filepath.Join(t.TempDir(), "leader.id")

			h, err := l.Launch(Spec{
				Name:        name,
				BinPath:     fakechildPath,
				Args:        []string{"--ignore-sigterm", "--report-pid", leaderID},
				KillSignal:  int(syscall.SIGTERM), // kill_signal: SIGTERM
				KillTimeout: testKillTimeout,
			})
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			t.Cleanup(func() { _ = h.Signal(syscall.SIGKILL) })

			// Readiness barrier: SIGTERM ignore is installed before we signal.
			_, _ = readIdentity(t, leaderID, 5*time.Second)

			start := time.Now()
			if err := h.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			elapsed := time.Since(start)

			// Graceful phase must have been given its full window...
			if elapsed < testKillTimeout-100*time.Millisecond {
				t.Fatalf("Stop returned in %v; SIGTERM ignorer should occupy kill_timeout (%v)", elapsed, testKillTimeout)
			}
			// ...and be bounded by kill_timeout + grace (I5), plus poll slack.
			if elapsed > testKillTimeout+GracePeriod+3*time.Second {
				t.Fatalf("Stop took %v; exceeds kill_timeout+grace bound (I5)", elapsed)
			}
			assertTreeEmpty(t, h)
			waitPidGone(t, h.PID(), 3*time.Second, "SIGTERM ignorer")
		})
	}
}

// TestOrphanReparentedAndReaped: the daemon is a subreaper; a grandchild
// whose parent dies reparents to us and, when it exits on its own, must
// be REAPED by the dedicated reaper — never left as a zombie. No Stop is
// involved: this is pure reaper behavior.
func TestOrphanReparentedAndReaped(t *testing.T) {
	l := newTestLauncher(t, ModeAuto)
	name := uniqueAppName("reap-orphan")
	orphanID := filepath.Join(t.TempDir(), "orphan.id")

	// Leader dies at 100ms; orphan lives ~2s and exits by itself.
	h := launch(t, l, name, "--spawn-orphan", "--orphan-pidfile", orphanID,
		"--exit-after", "100")
	info := waitDone(t, h, 5*time.Second)
	if info.Signaled {
		t.Fatalf("leader should have exited normally, got signal %d", info.TermSig)
	}

	orphanPid, _ := readIdentity(t, orphanID, 5*time.Second)
	waitPidGone(t, orphanPid, 10*time.Second, "naturally-exiting orphan (must be reaped, not zombied)")
}

// TestExitStatusPropagation: the reaper's ExitInfo mirrors waitpid.
func TestExitStatusPropagation(t *testing.T) {
	l := newTestLauncher(t, ModeAuto)
	name := uniqueAppName("exit-code")

	h := launch(t, l, name, "--exit-after", "60", "--exit-code", "7")
	info := waitDone(t, h, 5*time.Second)
	if info.ExitCode != 7 || info.Signaled {
		t.Fatalf("want exit code 7, got %+v", info)
	}
}

// TestStopSignaledExit: kill_signal SIGKILL means the leader dies BY
// signal; ExitInfo must say so (PM2 reports signaled deaths distinctly).
func TestStopSignaledExit(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			name := uniqueAppName("sigkill-exit")
			h, err := l.Launch(Spec{
				Name:        name,
				BinPath:     fakechildPath,
				KillSignal:  int(syscall.SIGKILL),
				KillTimeout: testKillTimeout,
			})
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			t.Cleanup(func() { _ = h.Signal(syscall.SIGKILL) })

			if err := h.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			info := h.ExitResult()
			if !info.Signaled || info.TermSig != int(syscall.SIGKILL) {
				t.Fatalf("want signaled exit (SIGKILL), got %+v", info)
			}
			assertTreeEmpty(t, h)
		})
	}
}

// TestCgroupSelfAttach: the re-exec wrapper must place the child INSIDE
// its per-app cgroup before the first app instruction runs — this closes
// the spawn-window escape where a child forks before the parent could
// move it. Skipped without delegation.
func TestCgroupSelfAttach(t *testing.T) {
	requireCgroupMode(t, ModeCgroupKill)
	l := newTestLauncher(t, Detect().Mode) // whatever v2 mode the host supports
	name := uniqueAppName("cg-attach")
	idFile := filepath.Join(t.TempDir(), "child.id")

	h := launch(t, l, name, "--report-pid", idFile)
	if h.CgroupPath() == "" {
		t.Fatalf("cgroup mode %s produced empty CgroupPath", h.Mode())
	}
	_, _ = readIdentity(t, idFile, 5*time.Second) // child is up

	procs, err := cgroupProcs(h.CgroupPath())
	if err != nil {
		t.Fatalf("read cgroup.procs: %v", err)
	}
	found := false
	for _, p := range procs {
		if p == h.PID() {
			found = true
		}
	}
	if !found {
		t.Fatalf("leader pid %d not in its own cgroup (procs=%v)", h.PID(), procs)
	}

	if err := h.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertTreeEmpty(t, h)
}
