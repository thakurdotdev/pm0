//go:build linux

package proc

import (
	"errors"
	"testing"
	"time"
)

// TestI6StaleStarttimeGuard: the pgid fallback's core safety property —
// a signal targeting a recorded (pid, starttime) pair is never delivered
// when the starttime no longer matches (pid died or was recycled).
func TestI6StaleStarttimeGuard(t *testing.T) {
	l := newTestLauncher(t, ModePgid)
	name := uniqueAppName("i6-victim")
	victimID := t.TempDir() + "/victim.id"

	// --pid-reuse-target: reports identity, ignores SIGTERM, sleeps
	// forever — the "must never be hit by a stale signal" victim.
	h := launch(t, l, name, "--pid-reuse-target", victimID)
	victimPid, victimStart := readIdentity(t, victimID, 5*time.Second)
	if victimPid != h.PID() {
		t.Fatalf("victim identity pid %d != handle pid %d", victimPid, h.PID())
	}

	// Wrong starttime (off by one): must refuse to signal (ErrStalePid)
	// and the victim must survive untouched.
	err := killVerified(victimPid, victimStart+1, sigTerm())
	if !errors.Is(err, ErrStalePid) {
		t.Fatalf("guard allowed signal with wrong starttime: %v", err)
	}
	if st, serr := procState(victimPid); serr != nil || st != 'S' {
		t.Fatalf("victim disturbed by refused signal (state=%c err=%v)", st, serr)
	}

	// Correct starttime: delivered. Use SIGKILL so cleanup is certain.
	if err := killVerified(victimPid, victimStart, sigKill()); err != nil {
		t.Fatalf("guard refused a legitimate signal: %v", err)
	}
	waitPidGone(t, victimPid, 3*time.Second, "victim (legitimate kill)")
}

// TestI6StaleHandleSignal: signaling a handle whose leader already exited
// returns ErrStalePid instead of blasting a possibly-reused pid.
func TestI6StaleHandleSignal(t *testing.T) {
	l := newTestLauncher(t, ModePgid)
	name := uniqueAppName("i6-stale-handle")

	h := launch(t, l, name, "--exit-after", "50")
	waitDone(t, h, 5*time.Second)

	err := h.Signal(sigTerm())
	if !errors.Is(err, ErrStalePid) {
		t.Fatalf("Signal on dead leader: want ErrStalePid, got %v", err)
	}

	// Tree is empty and stays empty; Stop on an already-dead tree is a
	// no-op that still returns nil (idempotent force paths).
	if pids := h.TreePids(); len(pids) != 0 {
		t.Fatalf("dead leader's tree not empty: %v", pids)
	}
	if err := h.Stop(); err != nil {
		t.Fatalf("Stop after death: %v", err)
	}
}

// TestI6GroupKillOnlyWhileLeaderOurs: with a live leader, Signal() works
// (group kill). After the leader is reaped, the same group number must
// never be signaled blind — covered by the guard above; here we assert
// the live path does deliver.
func TestI6GroupKillOnlyWhileLeaderOurs(t *testing.T) {
	l := newTestLauncher(t, ModePgid)
	name := uniqueAppName("i6-live-group")
	leaderID := t.TempDir() + "/leader.id"

	// Ignorer: the graceful signal arrives but is ignored — leader stays
	// alive, so the signal must have been actually delivered (no error).
	h, err := l.Launch(Spec{
		Name:        name,
		BinPath:     fakechildPath,
		Args:        []string{"--ignore-sigterm", "--report-pid", leaderID},
		KillTimeout: testKillTimeout,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() { _ = h.Signal(sigKill()) })

	// Readiness: ignore installed before we signal.
	_, _ = readIdentity(t, leaderID, 5*time.Second)

	if err := h.Signal(sigTerm()); err != nil {
		t.Fatalf("group signal to live leader failed: %v", err)
	}

	// Leader still alive after SIGTERM (it ignores it): identity intact.
	if st, serr := procState(h.PID()); serr != nil || st == 'Z' {
		t.Fatalf("leader died from ignored signal (state=%c err=%v)", st, serr)
	}

	if err := h.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	assertTreeEmpty(t, h)
}

// TestDetectConsistency: detection reports a coherent picture on any host
// (M0 acceptance: kernel/cgroup mode detection and reporting).
func TestDetectConsistency(t *testing.T) {
	d := Detect()
	switch d.Mode {
	case ModeCgroupKill, ModeCgroupFreeze, ModePgid:
	default:
		t.Fatalf("invalid mode %q", d.Mode)
	}
	if d.KernelRelease == "" {
		t.Fatalf("empty kernel release")
	}
	major, minor, err := kernelVersion(d.KernelRelease)
	if err != nil {
		t.Fatalf("kernel release %q unparseable: %v", d.KernelRelease, err)
	}
	if major < 2 || major > 10 {
		t.Fatalf("implausible kernel version %d.%d from %q", major, minor, d.KernelRelease)
	}
	if d.Mode != ModePgid && !d.CgroupUsable {
		t.Fatalf("mode %s without a usable cgroup root", d.Mode)
	}
	if (d.Mode == ModeCgroupKill) && (major < 5 || (major == 5 && minor < 14)) {
		t.Fatalf("cgroup-kill mode on kernel %d.%d (< 5.14)", major, minor)
	}
	if (d.Mode == ModeCgroupFreeze) && (major < 5 || (major == 5 && minor < 2)) {
		t.Fatalf("cgroup-freeze mode on kernel %d.%d (< 5.2)", major, minor)
	}
	if d.Mode == ModePgid && d.CgroupV2 && !d.CgroupUsable && d.Warning == "" {
		t.Fatalf("pgid fallback on a v2 host must carry a warning for doctor")
	}
}

// TestSanitizeName: cgroup dir names can't carry slashes or wildcards.
func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"my-app":     "my-app",
		"../../evil": ".._.._evil", // slashes gone: no path separators survive
		"..":         "app",        // traversal never survives
		".":          "app",
		"web/node":   "web_node",
		"":           "app",
		"a b/c*d":    "a_b_c_d",
	}
	for in, want := range cases {
		if got := SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}
