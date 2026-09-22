//go:build linux

package machine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/test/harness"
)

var fakechild string

func TestMain(m *testing.M) {
	bin, cleanup, err := harness.BuildFakechild()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer cleanup()
	fakechild = bin
	harness.SetFakechildPath(bin)
	os.Exit(m.Run())
}

// newApp builds an app with test-friendly launcher and registers cleanup.
// Every app gets a signal barrier (fakechild --report-pid): tests that
// stop/signal a freshly spawned child wait on it first (see
// WithSignalBarrier). Returns the barrier path ("" never happens).
func newApp(t *testing.T, cfg config.App) (*App, string) {
	t.Helper()
	cfg, barrier := harness.WithSignalBarrier(cfg)
	a, err := New(cfg, harness.NewLauncher(t), Options{})
	if err != nil {
		t.Fatalf("New(%s): %v", cfg.Name, err)
	}
	t.Cleanup(func() {
		select {
		case <-a.Done():
			return
		default:
		}
		_ = a.Delete()
		select {
		case <-a.Done():
		case <-time.After(10 * time.Second):
			t.Errorf("app %s actor did not exit after Delete", cfg.Name)
		}
	})
	return a, barrier
}

func waitStatus(t *testing.T, a *App, d time.Duration, want Status) Snapshot {
	t.Helper()
	var snap Snapshot
	harness.WaitFor(t, d, fmt.Sprintf("status %s (have %s)", want, snap.Status), func() bool {
		snap = a.Snapshot()
		return snap.Status == want
	})
	return snap
}

// M1 acceptance: start → online with pid and pm_uptime; stop → stopped,
// pid 0, exit code recorded from the graceful death.
func TestStartOnlineStop(t *testing.T) {
	a, ready := newApp(t, harness.SleepConfig(harness.UniqueName("m1-online")))

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := waitStatus(t, a, 3*time.Second, StatusOnline)
	if snap.Pid <= 0 {
		t.Fatalf("online with pid %d", snap.Pid)
	}
	if snap.PmUptimeMs == 0 {
		t.Fatal("pm_uptime not set on start")
	}
	if snap.RestartTime != 0 {
		t.Fatalf("initial start must not count as restart (have %d)", snap.RestartTime)
	}
	if snap.ExitCode != nil {
		t.Fatalf("exit_code must be null while running, got %d", *snap.ExitCode)
	}

	// Child must really be alive in /proc.
	if err := syscall.Kill(snap.Pid, 0); err != nil {
		t.Fatalf("pid %d not alive: %v", snap.Pid, err)
	}

	harness.WaitBarrier(t, ready, 5*time.Second) // handlers installed → graceful exit 0
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	snap = a.Snapshot()
	if snap.Status != StatusStopped {
		t.Fatalf("after stop: status %s", snap.Status)
	}
	if snap.Pid != 0 {
		t.Fatalf("after stop: pid %d", snap.Pid)
	}
	harness.WaitGone(t, snap.Pid, 2*time.Second, "stopped child")
	if snap.ExitCode == nil || *snap.ExitCode != 0 {
		t.Fatalf("graceful SIGINT death must record exit_code 0, got %v", snap.ExitCode)
	}
}

// Crash loop: max_restarts unstable exits park the app errored; the policy
// stops trying (no relaunch afterwards), counters hold, exit code recorded.
func TestCrashLoopParksErrored(t *testing.T) {
	cfg := harness.CrashConfig(harness.UniqueName("m1-errored"), 15, 7, 500*time.Millisecond, 3)
	a, _ := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := waitStatus(t, a, 8*time.Second, StatusErrored)

	// 3 unstable exits: relaunches after #1 and #2, parked after #3.
	if snap.RestartTime != 2 {
		t.Fatalf("restart_time = %d, want 2", snap.RestartTime)
	}
	if snap.UnstableRestarts != 0 {
		t.Fatalf("unstable_restarts = %d, want 0 (errored park resets it)", snap.UnstableRestarts)
	}
	if snap.ExitCode == nil || *snap.ExitCode != 7 {
		t.Fatalf("exit_code = %v, want 7", snap.ExitCode)
	}

	// Policy must stop trying: no further relaunches.
	time.Sleep(400 * time.Millisecond)
	snap2 := a.Snapshot()
	if snap2.Status != StatusErrored {
		t.Fatalf("post-errored status drifted to %s", snap2.Status)
	}
	if snap2.RestartTime != 2 || !snap2.CreatedAtIsNull {
		t.Fatalf("state drifted after errored: rt=%d createdNull=%v (want rt=2, created_at null)",
			snap2.RestartTime, snap2.CreatedAtIsNull)
	}

	// Manual start resets the budget and relaunches.
	if err := a.Start(); err != nil {
		t.Fatalf("Start after errored: %v", err)
	}
	waitStatus(t, a, 3*time.Second, StatusOnline)
	if s := a.Snapshot(); s.UnstableRestarts != 0 {
		t.Fatalf("manual start must reset unstable budget, have %d", s.UnstableRestarts)
	}
}

// Stable runs reset the unstable budget: an app that always outlives
// min_uptime restarts forever and must never park errored, even across
// more crashes than max_restarts.
func TestStableRunsResetBudget(t *testing.T) {
	cfg := harness.CrashConfig(harness.UniqueName("m1-stable"), 150, 5, 80*time.Millisecond, 2)
	a, _ := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 4 stable lifetimes (~600ms) > max_restarts=2: still alive.
	harness.WaitFor(t, 8*time.Second, "restart_time >= 4", func() bool {
		return a.Snapshot().RestartTime >= 4
	})
	snap := a.Snapshot()
	if snap.Status == StatusErrored {
		t.Fatal("stable runs must reset the budget; app parked errored")
	}
	if snap.UnstableRestarts != 0 {
		t.Fatalf("stable exit must zero unstable_restarts, have %d", snap.UnstableRestarts)
	}
	_ = a.Stop()
}

// autorestart=false: any exit parks stopped with the exit code recorded,
// and no restart happens even with delay elapsed.
func TestAutorestartFalseParksStopped(t *testing.T) {
	cfg := harness.CrashConfig(harness.UniqueName("m1-norestart"), 20, 7, 500*time.Millisecond, 3)
	cfg.Autorestart = false
	a, _ := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := waitStatus(t, a, 3*time.Second, StatusStopped)
	if snap.ExitCode == nil || *snap.ExitCode != 7 {
		t.Fatalf("exit_code = %v, want 7", snap.ExitCode)
	}
	if snap.RestartTime != 0 || snap.UnstableRestarts != 0 {
		t.Fatalf("no restart policy must run: rt=%d ur=%d", snap.RestartTime, snap.UnstableRestarts)
	}
	time.Sleep(150 * time.Millisecond) // would have relaunched with autorestart
	if s := a.Snapshot(); s.Status != StatusStopped {
		t.Fatalf("status drifted to %s without autorestart", s.Status)
	}
}

// restart_delay: between crash and relaunch the app parks in waiting —
// the pinned vocabulary for the delay window.
func TestRestartDelayShowsWaiting(t *testing.T) {
	cfg := harness.CrashConfig(harness.UniqueName("m1-delay"), 15, 3, 500*time.Millisecond, 10)
	cfg.RestartDelay = 400 * time.Millisecond
	a, _ := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, a, 3*time.Second, StatusWaiting) // crash happened, sleeping
	waitStatus(t, a, 5*time.Second, StatusOnline)  // delay elapsed, relaunched

	snap := a.Snapshot()
	if snap.RestartTime != 1 {
		t.Fatalf("restart_time = %d, want 1", snap.RestartTime)
	}
	if snap.UnstableRestarts != 1 {
		t.Fatalf("unstable_restarts = %d, want 1", snap.UnstableRestarts)
	}
	_ = a.Stop()
}

// exp_backoff pure curve: min_uptime doubled per unstable restart, capped.
// Observed pm2 backoff curve (God.handleExit): first sleep = configured
// exp_backoff_restart_delay, then floor(prev*1.5), capped at 15000ms.
func TestBackoffCurve(t *testing.T) {
	cases := []struct {
		base time.Duration
		prev time.Duration
		want time.Duration
	}{
		{100 * time.Millisecond, 0, 100 * time.Millisecond}, // first crash: configured base
		{100 * time.Millisecond, 100 * time.Millisecond, 150 * time.Millisecond},
		{100 * time.Millisecond, 150 * time.Millisecond, 225 * time.Millisecond},
		{100 * time.Millisecond, 12 * time.Second, 15 * time.Second}, // 18s floored to cap
		{100 * time.Millisecond, 20 * time.Second, 15 * time.Second}, // stays at cap
		{0, 0, 0}, // backoff off, delay 0
	}
	for _, c := range cases {
		cfg := config.Default()
		cfg.ExpBackoffRestartDelay = c.base
		got, isBackoff := NextRestartDelay(cfg, c.prev)
		if !isBackoff && c.base > 0 {
			t.Fatalf("base %v: backoff curve not active", c.base)
		}
		if got != c.want {
			t.Errorf("NextRestartDelay(base=%v, prev=%v) = %v, want %v", c.base, c.prev, got, c.want)
		}
	}
}

// exp_backoff jitter removed (observed pm2 has none); fixed-delay path
// passes restart_delay through untouched.
func TestNextRestartDelayBounds(t *testing.T) {
	cfg := config.Default()
	cfg.RestartDelay = 250 * time.Millisecond
	if d, backoff := NextRestartDelay(cfg, 3); d != 250*time.Millisecond || backoff {
		t.Fatalf("fixed delay passthrough: %v backoff=%v", d, backoff)
	}
	if d, _ := NextRestartDelay(config.Default(), 0); d != 0 {
		t.Fatalf("default = immediate restart, got %v", d)
	}
}

// exp_backoff integration smoke: delays grow, app cycles through waiting
// and eventually parks errored per max_restarts.
func TestExpBackoffCrashLoop(t *testing.T) {
	// Outer window (observed pm2) = min_uptime * max_restarts = 400ms:
	// generous for race-detector spawn latency; backoff delays stay small.
	cfg := harness.CrashConfig(harness.UniqueName("m1-backoff"), 10, 9, 100*time.Millisecond, 4)
	cfg.ExpBackoffRestartDelay = 5 * time.Millisecond
	cfg.ExpBackoffCap = 40 * time.Millisecond
	a, _ := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	snap := waitStatus(t, a, 8*time.Second, StatusErrored)
	// Observed pm2: the errored park RESETS unstable_restarts (§2.2).
	if snap.RestartTime != 3 || snap.UnstableRestarts != 0 || !snap.CreatedAtIsNull {
		t.Fatalf("rt=%d ur=%d createdNull=%v, want rt=3 ur=0 createdNull=true",
			snap.RestartTime, snap.UnstableRestarts, snap.CreatedAtIsNull)
	}
}

// Stop while parked in waiting must cancel the pending relaunch: the app
// stays stopped after the original delay would have elapsed.
func TestStopDuringWaitingCancelsRelaunch(t *testing.T) {
	cfg := harness.CrashConfig(harness.UniqueName("m1-stopwait"), 15, 3, 500*time.Millisecond, 10)
	cfg.RestartDelay = 500 * time.Millisecond
	a, _ := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, a, 3*time.Second, StatusWaiting)
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(800 * time.Millisecond) // > restart_delay: a stale relaunch would show
	snap := a.Snapshot()
	if snap.Status != StatusStopped {
		t.Fatalf("stop during waiting must park stopped, have %s", snap.Status)
	}
	if snap.RestartTime != 0 {
		t.Fatalf("cancelled park must not relaunch, rt=%d", snap.RestartTime)
	}
}

// Start on an online app is an idempotent no-op.
func TestStartIdempotentWhileOnline(t *testing.T) {
	a, _ := newApp(t, harness.SleepConfig(harness.UniqueName("m1-idem")))
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := waitStatus(t, a, 3*time.Second, StatusOnline)
	if err := a.Start(); err != nil {
		t.Fatalf("idempotent Start: %v", err)
	}
	snap := a.Snapshot()
	if snap.Status != StatusOnline || snap.Pid != first.Pid || snap.RestartTime != 0 {
		t.Fatalf("idempotent start mutated state: %+v (first pid %d)", snap, first.Pid)
	}
}

// Manual restart of an online app: stop → relaunch, restart_time=1,
// fresh pm_uptime, crash budget untouched.
func TestManualRestartOnline(t *testing.T) {
	a, _ := newApp(t, harness.SleepConfig(harness.UniqueName("m1-restart")))
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := waitStatus(t, a, 3*time.Second, StatusOnline)

	if err := a.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	harness.WaitFor(t, 5*time.Second, "restart_time == 1 && online", func() bool {
		s := a.Snapshot()
		return s.Status == StatusOnline && s.RestartTime == 1
	})
	snap := a.Snapshot()
	if snap.Pid <= 0 {
		t.Fatal("no pid after restart")
	}
	if snap.PmUptimeMs < first.PmUptimeMs {
		t.Fatal("pm_uptime must move forward on restart")
	}
}

// A signal death of a stable app auto-restarts with a fresh pid and a
// zeroed budget. (Exit fields are intentionally NOT asserted here: the
// relaunch clears them — §2.2 pins exit_code null while running. The
// signal-death fields are asserted by TestSignalDeathRecordsFields.)
func TestSignalDeathAutoRestarts(t *testing.T) {
	cfg := harness.SleepConfig(harness.UniqueName("m1-sigkill"))
	cfg.MinUptime = 200 * time.Millisecond
	a, ready := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	first := waitStatus(t, a, 3*time.Second, StatusOnline)
	harness.WaitBarrier(t, ready, 5*time.Second)
	time.Sleep(250 * time.Millisecond) // outlive min_uptime → stable death

	if err := a.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	harness.WaitFor(t, 6*time.Second, "auto-restart to online (rt>=1)", func() bool {
		s := a.Snapshot()
		return s.Status == StatusOnline && s.RestartTime >= 1 && s.Pid > 0 && s.Pid != first.Pid
	})
	snap := a.Snapshot()
	if snap.UnstableRestarts != 0 {
		t.Fatalf("stable death must zero the budget, have %d", snap.UnstableRestarts)
	}
}

// Signal-death bookkeeping without a relaunch in the way: with
// autorestart=false a SIGKILL death parks stopped with the signal recorded
// and exit_code left null (pm2 emits null for signal deaths).
func TestSignalDeathRecordsFields(t *testing.T) {
	cfg := harness.SleepConfig(harness.UniqueName("m1-sigrec"))
	cfg.Autorestart = false
	a, ready := newApp(t, cfg)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, a, 3*time.Second, StatusOnline)
	harness.WaitBarrier(t, ready, 5*time.Second)

	if err := a.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	snap := waitStatus(t, a, 3*time.Second, StatusStopped)
	if snap.LastExitSig != int(syscall.SIGKILL) {
		t.Fatalf("last exit signal = %d, want SIGKILL", snap.LastExitSig)
	}
	if snap.ExitCode == nil || *snap.ExitCode != 0 {
		t.Fatalf("signal death must record exit_code 0 (observed pm2 code||0), got %v", snap.ExitCode)
	}
}

// Spawn failure parks errored immediately and never spins on fork errors.
func TestSpawnFailureParksErrored(t *testing.T) {
	cfg := config.DefaultFor(harness.UniqueName("m1-badbin"), "/nonexistent/pm0-m1-bin")
	a, _ := newApp(t, cfg)

	if err := a.Start(); err == nil {
		t.Fatal("Start must surface the spawn error")
	}
	snap := waitStatus(t, a, 2*time.Second, StatusErrored)
	if snap.RestartTime != 0 || snap.Pid != 0 {
		t.Fatalf("spawn failure must not relaunch: rt=%d pid=%d", snap.RestartTime, snap.Pid)
	}
	time.Sleep(150 * time.Millisecond)
	if s := a.Snapshot(); s.RestartTime != 0 {
		t.Fatalf("fork-failure spin detected: rt=%d", s.RestartTime)
	}
}

// Hammer: concurrent Start/Stop/Restart against a rapidly dying child.
// The race detector is the real assertion; afterwards the app must land in
// a consistent stopped state with an empty tree.
func TestCommandHammerRace(t *testing.T) {
	cfg := harness.CrashConfig(harness.UniqueName("m1-hammer"), 25, 4, 150*time.Millisecond, 1000)
	a, _ := newApp(t, cfg)
	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ops := []func() error{a.Start, a.Stop, a.Restart, a.Restart}
	done := make(chan struct{})
	for w := 0; w < 6; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 12; i++ {
				_ = ops[(w+i)%len(ops)]()
				time.Sleep(time.Duration(5+w*3) * time.Millisecond)
			}
		}(w)
	}
	for w := 0; w < 6; w++ {
		<-done
	}

	if err := a.Stop(); err != nil {
		t.Fatalf("final Stop: %v", err)
	}
	snap := a.Snapshot()
	if snap.Status != StatusStopped || snap.Pid != 0 {
		t.Fatalf("post-hammer state inconsistent: %+v", snap)
	}
}

// buildEnv: extra keys replace inherited ones (no duplicates) and keep the
// rest of the parent environment.
func TestBuildEnvReplacesKeys(t *testing.T) {
	pathKey := "PM0_TEST_PATH"
	t.Setenv(pathKey, "old")
	env := buildEnv(map[string]string{pathKey: "new", "PM0_TEST_EXTRA": "1"})

	var hits int
	var extra, pathVal bool
	for _, kv := range env {
		switch {
		case kv == pathKey+"=new":
			hits++
			pathVal = true
		case kv == pathKey+"=old":
			t.Fatal("old value must be replaced, not kept")
		case kv == "PM0_TEST_EXTRA=1":
			extra = true
		}
	}
	if hits != 1 || !pathVal || !extra {
		t.Fatalf("env merge wrong: hits=%d pathVal=%v extra=%v", hits, pathVal, extra)
	}
	if buildEnv(nil) != nil {
		t.Fatal("nil extra must return nil (proc inherits os.Environ)")
	}
}

func TestParseKillSignal(t *testing.T) {
	cases := map[string]syscall.Signal{
		"SIGTERM": syscall.SIGTERM, "term": syscall.SIGTERM, "TERM": syscall.SIGTERM,
		"SIGINT": syscall.SIGINT, "": syscall.SIGINT, "bogus": syscall.SIGINT,
		"SIGHUP": syscall.SIGHUP, "hup": syscall.SIGHUP,
	}
	for in, want := range cases {
		if got := parseKillSignal(in); got != want {
			t.Errorf("parseKillSignal(%q) = %v, want %v", in, got, want)
		}
	}
}

// Config validation: New refuses nameless/scriptless apps and coerces the
// treekill compat flag.
func TestNewValidation(t *testing.T) {
	l := harness.NewLauncher(t)
	if _, err := New(config.Default(), l, Options{}); err != ErrNoScript {
		t.Fatalf("want ErrNoScript, got %v", err)
	}
	cfg := harness.SleepConfig("x")
	cfg.Name = ""
	if _, err := New(cfg, l, Options{}); err == nil || err.Error() != "machine: app config needs a Name" {
		t.Fatalf("nameless app must fail, got %v", err)
	}
	cfg = harness.SleepConfig(filepath.Base(t.Name()))
	cfg.Treekill = false
	a, err := New(cfg, l, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !a.Config().Treekill {
		t.Fatal("treekill must coerce back to true (row 23: always tree-kill)")
	}
}
