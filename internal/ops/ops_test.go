//go:build linux

package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
)

// fakeSup records restarts and supplies controllable RSS/status.
type fakeSup struct {
	mu       sync.Mutex
	restarts []int
	statuses map[int]string
	rss      map[int]int64
	logs     []string
}

func (f *fakeSup) Restart(id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts = append(f.restarts, id)
	return nil
}

func (f *fakeSup) RSS(id int) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rss[id]
}

func (f *fakeSup) Status(id int) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st, ok := f.statuses[id]
	return st, ok
}

func (f *fakeSup) Logf(format string, a ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, fmt.Sprintf(format, a...))
}

func (f *fakeSup) restartCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.restarts)
}

func newTestReconciler(f *fakeSup) *Reconciler {
	return New(f.Restart, f.RSS, f.Status, f.Logf)
}

// waitFor polls cond until it passes or the deadline expires.
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testView(id int, mutate func(*config.App)) View {
	cfg := config.DefaultFor(fmt.Sprintf("app-%d", id), "/bin/sleep")
	if mutate != nil {
		mutate(&cfg)
	}
	return View{ID: id, Cfg: cfg}
}

// cron_restart: fires when the (faked) clock enters a matching minute,
// exactly once per slot, and restarts even apps in other statuses (pm2
// restartProcessId semantics).
func TestCronRestartFiresOnMinute(t *testing.T) {
	f := &fakeSup{statuses: map[int]string{1: "online"}, rss: map[int]int64{}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond

	var mu sync.Mutex
	now := time.Date(2026, 5, 1, 12, 0, 30, 0, time.UTC)
	r.Now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	setNow := func(t time.Time) {
		mu.Lock()
		defer mu.Unlock()
		now = t
	}

	r.Sync([]View{testView(1, func(c *config.App) { c.CronRestart = "* * * * *" })})

	// Before the boundary: nothing.
	time.Sleep(80 * time.Millisecond)
	if f.restartCount() != 0 {
		t.Fatalf("cron fired before the boundary (%d restarts)", f.restartCount())
	}

	// Cross into 12:01.
	setNow(time.Date(2026, 5, 1, 12, 1, 1, 0, time.UTC))
	waitFor(t, 2*time.Second, "first cron restart", func() bool { return f.restartCount() >= 1 })

	// Stay inside the same slot: no repeat.
	setNow(time.Date(2026, 5, 1, 12, 1, 59, 0, time.UTC))
	time.Sleep(80 * time.Millisecond)
	if f.restartCount() != 1 {
		t.Fatalf("cron re-fired within the same minute (%d restarts)", f.restartCount())
	}

	// Next slot fires again.
	setNow(time.Date(2026, 5, 1, 12, 2, 2, 0, time.UTC))
	waitFor(t, 2*time.Second, "second cron restart", func() bool { return f.restartCount() >= 2 })

	r.Stop()
}

// max_memory_restart: online app over the limit restarts on the sampling
// cadence; unknown RSS (0) never triggers.
func TestMaxMemoryRestart(t *testing.T) {
	f := &fakeSup{
		statuses: map[int]string{7: "online", 8: "online"},
		rss:      map[int]int64{7: 5000, 8: 0},
	}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond
	r.MemInterval = 50 * time.Millisecond

	r.Sync([]View{
		testView(7, func(c *config.App) { c.MaxMemoryRestart = 1000 }),
		testView(8, func(c *config.App) { c.MaxMemoryRestart = 1000 }),
	})

	waitFor(t, 2*time.Second, "memory restart for app 7", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.restarts) >= 1 && f.restarts[0] == 7
	})

	// App 8 (RSS unmeasurable) must never restart.
	time.Sleep(200 * time.Millisecond)
	f.mu.Lock()
	total, bad := len(f.restarts), false
	for _, id := range f.restarts {
		if id == 8 {
			bad = true
		}
	}
	f.mu.Unlock()
	if bad || total > 4 {
		t.Fatalf("unexpected restarts: %v", f.restarts)
	}
	r.Stop()
}

// Health checks (divergence 7): retries consecutive failures restart the
// app; a passing probe resets the count so it never accumulates across
// recoveries.
func TestHealthCheckFailureGating(t *testing.T) {
	f := &fakeSup{statuses: map[int]string{3: "online"}, rss: map[int]int64{}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond

	cfg := testView(3, func(c *config.App) {
		c.HealthCheckCmd = "exit 1" // always fails
		c.HealthCheckInterval = 30 * time.Millisecond
		c.HealthCheckTimeout = 5 * time.Second
		c.HealthCheckRetries = 2
	})
	r.Sync([]View{cfg})

	waitFor(t, 3*time.Second, "health restart after 2 failures", func() bool {
		return f.restartCount() >= 1
	})
	if f.restartCount() > 3 {
		t.Fatalf("health restarts ran away: %d", f.restartCount())
	}
	r.Stop()
}

func TestHealthCheckSuccessResets(t *testing.T) {
	f := &fakeSup{statuses: map[int]string{4: "online"}, rss: map[int]int64{}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond

	cfg := testView(4, func(c *config.App) {
		c.HealthCheckCmd = "exit 0" // always passes
		c.HealthCheckInterval = 30 * time.Millisecond
		c.HealthCheckRetries = 1
	})
	r.Sync([]View{cfg})

	time.Sleep(300 * time.Millisecond)
	if f.restartCount() != 0 {
		t.Fatalf("healthy app restarted %d times", f.restartCount())
	}
	r.Stop()
}

// Health checks skip non-online apps (a stopped app must not be restarted
// by probe failures).
func TestHealthCheckSkipsStoppedApps(t *testing.T) {
	f := &fakeSup{statuses: map[int]string{5: "stopped"}, rss: map[int]int64{}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond

	cfg := testView(5, func(c *config.App) {
		c.HealthCheckCmd = "exit 1"
		c.HealthCheckInterval = 30 * time.Millisecond
		c.HealthCheckRetries = 1
	})
	r.Sync([]View{cfg})

	time.Sleep(250 * time.Millisecond)
	if f.restartCount() != 0 {
		t.Fatalf("stopped app restarted %d times by health checks", f.restartCount())
	}
	r.Stop()
}

// Watch (rows 21/22): a file change in the app cwd restarts the online
// app after the debounce; node_modules churn is ignored entirely.
func TestWatchRestartsOnChange(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSup{statuses: map[int]string{9: "online"}, rss: map[int]int64{}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond
	r.Debounce = 80 * time.Millisecond

	cfg := testView(9, func(c *config.App) {
		c.Watch = true
		c.Cwd = dir
	})
	r.Sync([]View{cfg})

	// Wait for the runner's watcher to attach (async loop start).
	waitFor(t, 2*time.Second, "watcher attached", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.runners[9] != nil && r.runners[9].w != nil
	})

	if err := os.WriteFile(filepath.Join(dir, "app.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "watch restart", func() bool { return f.restartCount() >= 1 })

	// Ignored subtree: node_modules churn must not restart.
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "index.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if f.restartCount() != 1 {
		t.Fatalf("node_modules churn restarted the app (%d restarts)", f.restartCount())
	}

	// Ignored suffix: *.tmp writes never fire.
	if err := os.WriteFile(filepath.Join(dir, "scratch.tmp"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if f.restartCount() != 1 {
		t.Fatalf(".tmp churn restarted the app (%d restarts)", f.restartCount())
	}
	r.Stop()
}

// Watch on a deliberately STOPPED app must not resurrect it.
func TestWatchSkipsStoppedApps(t *testing.T) {
	dir := t.TempDir()
	f := &fakeSup{statuses: map[int]string{11: "stopped"}, rss: map[int]int64{}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond
	r.Debounce = 60 * time.Millisecond

	cfg := testView(11, func(c *config.App) { c.Watch = true; c.Cwd = dir })
	r.Sync([]View{cfg})
	waitFor(t, 2*time.Second, "watcher attached", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.runners[11] != nil && r.runners[11].w != nil
	})

	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	if f.restartCount() != 0 {
		t.Fatalf("stopped app restarted by watch (%d times)", f.restartCount())
	}
	r.Stop()
}

// Sync removes runners for deleted apps; Stop tears everything down and is
// idempotent; apps with no features never get runners.
func TestReconcilerLifecycle(t *testing.T) {
	f := &fakeSup{statuses: map[int]string{1: "online"}, rss: map[int]int64{}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond

	plain := testView(2, nil) // no ops features
	watched := testView(1, func(c *config.App) { c.CronRestart = "@daily" })
	r.Sync([]View{plain, watched})
	time.Sleep(50 * time.Millisecond)
	r.mu.Lock()
	n := len(r.runners)
	r.mu.Unlock()
	if n != 1 {
		t.Fatalf("runner count = %d, want 1 (featureless apps get none)", n)
	}

	r.Sync([]View{plain}) // watched app deleted
	waitFor(t, 2*time.Second, "runner removed", func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.runners) == 0
	})

	r.Stop()
	r.Stop() // idempotent
}

// watchIgnore pins the row-22 superset semantics.
func TestWatchIgnore(t *testing.T) {
	yes := []string{
		"/app/node_modules", "/app/node_modules/pkg/index.js", "/app/.git/objects",
		"/app/.git", "node_modules", "sub/.git", "x.swp", "a/b/c.tmp", "swap/.swp",
	}
	no := []string{"/app/main.go", "/app/src/x.js", "/app/.github", "/app/temp", "/app/swp"}
	for _, p := range yes {
		if !watchIgnore(p) {
			t.Errorf("watchIgnore(%q) = false, want true", p)
		}
	}
	for _, p := range no {
		if watchIgnore(p) {
			t.Errorf("watchIgnore(%q) = true, want false", p)
		}
	}
}

// healthTimeout clamps to the interval (a probe must never outlive its slot).
func TestHealthTimeoutClamp(t *testing.T) {
	cfg := config.Default()
	cfg.HealthCheckInterval = 2 * time.Second
	cfg.HealthCheckTimeout = 9 * time.Second
	if got := healthTimeout(cfg); got != 2*time.Second {
		t.Fatalf("healthTimeout = %v, want clamped to interval 2s", got)
	}
	cfg.HealthCheckInterval = 0 // legacy zero lifts to the 30s default
	if got := healthTimeout(cfg); got != 9*time.Second {
		t.Fatalf("healthTimeout = %v, want 9s (fits under lifted 30s interval)", got)
	}
	cfg.HealthCheckTimeout = 0 // unset timeout lifts to the default
	if got := healthTimeout(cfg); got != config.DefaultHealthTimeout {
		t.Fatalf("healthTimeout = %v, want default %v", got, config.DefaultHealthTimeout)
	}
}

// Regression: pm_ids are reused after the registry empties (§3.2). A stale
// runner from a deleted app at the same pm_id must be retired and a fresh
// one built for the successor — otherwise the new app's features silently
// never run (live-smoke M5 bug: memory app's stale runner masked cron).
func TestSyncRetiresStaleRunnerOnIDReuse(t *testing.T) {
	f := &fakeSup{statuses: map[int]string{0: "online"}, rss: map[int]int64{0: 5000}}
	r := newTestReconciler(f)
	r.Tick = 10 * time.Millisecond
	r.MemInterval = 30 * time.Millisecond

	// First incarnation: pm_id 0, memory trigger.
	r.Sync([]View{{ID: 0, Serial: 1, Cfg: testView(0, func(c *config.App) { c.MaxMemoryRestart = 1000 }).Cfg}})
	waitFor(t, 2*time.Second, "first incarnation memory restart", func() bool {
		return f.restartCount() >= 1
	})

	// Successor at the SAME pm_id with cron instead of memory. Until the
	// cron slot crosses, nothing may fire from the stale memory runner.
	r.Sync([]View{{ID: 0, Serial: 2, Cfg: testView(0, func(c *config.App) { c.CronRestart = "* * * * *" }).Cfg}})
	time.Sleep(120 * time.Millisecond)
	if n := f.restartCount(); n != 1 {
		t.Fatalf("stale memory runner kept firing after id reuse (%d restarts)", n)
	}

	// The cron runner is live: crossing a minute boundary fires.
	r.mu.Lock()
	rn := r.runners[0]
	r.mu.Unlock()
	if rn == nil || rn.serial != 2 {
		t.Fatalf("successor runner missing or stale (serial mismatch)")
	}
	// Drive the clock: the reconciler's Now is time.Now by default — swap in
	// a mutable one is not possible post-hoc here; instead wait for the real
	// next minute (bounded by the cron slot).
	deadline := time.Now().Add(70 * time.Second)
	fired := false
	for time.Now().Before(deadline) {
		if f.restartCount() >= 2 {
			fired = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !fired {
		t.Fatal("successor cron runner never fired after id reuse")
	}
	r.Stop()
}
