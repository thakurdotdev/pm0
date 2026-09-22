// Package ops implements the M5 operations layer (docs/compat.md rows
// 15/18/20/21, divergences 6/7): the daemon-side enforcers that watch each
// app and restart it on schedule or on resource pressure —
//
//   - max_memory_restart: tree RSS above the limit (checked on the pm2
//     monitoring cadence, 30s) restarts the app (row 15);
//   - cron_restart: vixie-cron expression fires a restart when the wall
//     clock enters a matching minute (row 20);
//   - health checks: a pm0 extension — a probe command run through
//     /bin/sh -c while the app is online; N consecutive failures restart
//     it, any success resets the count (divergence 7);
//   - watch: fsnotify on the app cwd (recursive, ignoring node_modules,
//     .git, *.swp, *.tmp per row 22) restarts online/errored apps after a
//     short debounce (row 21).
//
// Architecture: the daemon syncs a Reconciler every tick with the current
// registry views; the reconciler keeps one runner goroutine per app that
// has at least one feature configured and drops runners for apps that
// vanish (delete) or that have nothing configured. Restarts funnel through
// the same supervisor.Restart used by `pm0 restart` — machine-level
// manual-restart semantics (resetState, restart_time++), exactly like
// pm2's God.restartProcessId that its own monitor/watch/cron paths call.
//
// Concurrency: runner state lives on the runner goroutine; the Reconciler
// map is guarded by its mutex; Restart/RSS/Status callbacks are supplied
// by the daemon and must be safe for concurrent use.
package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/cron"
	"github.com/pm0/pm0/internal/machine"
)

// View is the per-app state the daemon hands the reconciler each sync.
type View struct {
	ID int
	// Serial is the app's registration serial (supervisor-assigned, unique
	// per app incarnation). pm_ids are reused after the registry empties;
	// the serial is what tells a deleted app apart from its successor, so a
	// stale runner at a recycled id is retired instead of silently keeping
	// the new app's features off.
	Serial uint64
	Cfg    config.App
}

// Reconciler owns the per-app runners.
type Reconciler struct {
	// Restart restarts one app (manual-restart semantics). Supplied by the
	// daemon (supervisor.Restart under an id-string conversion).
	Restart func(id int) error
	// RSS returns the app's tree RSS in bytes (0 when not measurable).
	RSS func(id int) int64
	// Status returns the app's current status string (machine.Status) and
	// whether the app exists.
	Status func(id int) (string, bool)
	// Logf receives operational lines (daemon log).
	Logf func(format string, args ...any)

	// Now overrides time.Now (tests).
	Now func() time.Time
	// Tick is the sync + runner step cadence (default 1s).
	Tick time.Duration
	// MemInterval is the max_memory_restart check cadence
	// (default 30s — pm2's monitoring interval, compat.md M5 pin).
	MemInterval time.Duration
	// Debounce is the watch quiet window before a restart fires
	// (default 500ms).
	Debounce time.Duration

	mu      sync.Mutex
	runners map[int]*runner
	stopped bool
}

// New builds a Reconciler with the pinned defaults.
func New(restart func(int) error, rss func(int) int64, status func(int) (string, bool), logf func(string, ...any)) *Reconciler {
	return &Reconciler{
		Restart:     restart,
		RSS:         rss,
		Status:      status,
		Logf:        logf,
		Now:         time.Now,
		Tick:        time.Second,
		MemInterval: 30 * time.Second,
		Debounce:    500 * time.Millisecond,
		runners:     make(map[int]*runner),
	}
}

// Sync reconciles the runner set against the current views: runners start
// for apps with at least one feature configured and stop for apps that
// vanished or have nothing to enforce. Cheap to call every tick.
func (r *Reconciler) Sync(views []View) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	seen := make(map[int]bool, len(views))
	for _, v := range views {
		seen[v.ID] = true
		if cur := r.runners[v.ID]; cur != nil {
			if cur.serial == v.Serial {
				continue // same app incarnation: runner stays
			}
			// pm_id reused by a new registration: retire the stale runner
			// (its stop is async — its goroutine may be mid-restart) and
			// build a fresh one for the new app below.
			cur.stopAsync()
			delete(r.runners, v.ID)
		}
		if featuresActive(v.Cfg) {
			rn := r.newRunner(v)
			r.runners[v.ID] = rn
			go rn.loop()
		}
	}
	for id, rn := range r.runners {
		if !seen[id] {
			rn.stopAsync()
			delete(r.runners, id)
		}
	}
}

// Stop tears every runner down (tests, daemon shutdown). Idempotent.
func (r *Reconciler) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	runners := make([]*runner, 0, len(r.runners))
	for id, rn := range r.runners {
		runners = append(runners, rn)
		delete(r.runners, id)
	}
	r.mu.Unlock()
	for _, rn := range runners {
		rn.stopAndWait()
	}
}

// featuresActive reports whether any ops feature is configured.
func featuresActive(cfg config.App) bool {
	return cfg.CronRestart != "" || cfg.MaxMemoryRestart > 0 ||
		cfg.HealthCheckCmd != "" || cfg.Watch
}

// runner drives one app's features. All mutable state belongs to the
// runner goroutine (loop/step); stop/done coordinate shutdown.
type runner struct {
	id     int
	serial uint64 // registration serial (View.Serial); identity check
	cfg    config.App
	r      *Reconciler

	sched *cron.Schedule // nil when cron_restart unset

	stop chan struct{}
	done chan struct{}

	// feature state (runner goroutine only)
	cronNext  time.Time   // next cron fire; zero until computed
	lastMem   time.Time   // last max_memory_restart sample
	lastProbe time.Time   // last health probe dispatch
	fails     int         // consecutive health failures
	inflight  atomic.Bool // one health probe at a time
	probeCh   chan error  // probe result (buffered 1)
	w         *watcher    // nil when watch off or watcher failed
	root      string      // resolved app cwd (watch root, probe dir)
}

func (r *Reconciler) newRunner(v View) *runner {
	rn := &runner{
		id:      v.ID,
		serial:  v.Serial,
		cfg:     v.Cfg,
		r:       r,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
		probeCh: make(chan error, 1),
	}
	if v.Cfg.CronRestart != "" {
		if s, err := cron.Parse(v.Cfg.CronRestart); err == nil {
			rn.sched = &s
		} else {
			// machine.buildApp validated at registration; a bad dump edit
			// surfaces here without killing the other features.
			r.Logf("ops: app %s (pm_id %d): cron_restart %q: %v", v.Cfg.Name, v.ID, v.Cfg.CronRestart, err)
		}
	}
	return rn
}

// loop is the runner body: start the watcher (when configured), then step
// the feature state on every tick until stopped.
func (rn *runner) loop() {
	defer close(rn.done)
	rn.root = rn.appRoot()

	if rn.sched != nil {
		rn.cronNext = rn.sched.Next(rn.r.Now())
	}
	if rn.cfg.Watch {
		w, err := startWatcher(rn.root, rn.r.Debounce, rn.onWatchEvent, rn.r.Logf)
		if err != nil {
			rn.r.Logf("ops: app %s (pm_id %d): watch disabled: %v", rn.cfg.Name, rn.id, err)
		} else {
			// Published under the reconciler lock: tests and (future)
			// diagnostics read rn.w concurrently with this startup.
			rn.r.mu.Lock()
			rn.w = w
			rn.r.mu.Unlock()
		}
	}
	rn.r.mu.Lock()
	w := rn.w
	rn.r.mu.Unlock()
	if w != nil {
		defer w.close()
	}

	tick := time.NewTicker(rn.r.Tick)
	defer tick.Stop()
	for {
		select {
		case <-rn.stop:
			return
		case <-tick.C:
			rn.step(rn.r.Now())
		}
	}
}

// step runs one scheduling pass. Runs on the runner goroutine; a blocking
// Restart (bounded by kill_timeout + grace) only delays this app's ops.
func (rn *runner) step(now time.Time) {
	// cron_restart (row 20): fire when the clock enters a matching minute.
	// Missed slots (daemon busy/suspended) fire ONCE, then resync forward —
	// cron semantics, not a catch-up burst.
	if rn.sched != nil && !rn.cronNext.IsZero() && !now.Before(rn.cronNext) {
		rn.triggerRestart(fmt.Sprintf("cron_restart %q", rn.cfg.CronRestart))
		rn.cronNext = rn.sched.Next(now)
	}

	// max_memory_restart (row 15): sample tree RSS on the monitoring
	// cadence, only meaningful while online.
	if rn.cfg.MaxMemoryRestart > 0 && now.Sub(rn.lastMem) >= rn.r.MemInterval {
		rn.lastMem = now
		if rn.online() {
			if rss := rn.r.RSS(rn.id); rss > 0 && rss > rn.cfg.MaxMemoryRestart {
				rn.triggerRestart(fmt.Sprintf("max_memory_restart: tree RSS %d bytes > limit %d",
					rss, rn.cfg.MaxMemoryRestart))
			}
		}
	}

	// Health checks (divergence 7): probe while online, restart after
	// retries consecutive failures, success resets the counter.
	if rn.cfg.HealthCheckCmd != "" {
		iv := healthInterval(rn.cfg)
		if now.Sub(rn.lastProbe) >= iv {
			rn.lastProbe = now
			if rn.online() && rn.inflight.CompareAndSwap(false, true) {
				go func() {
					err := rn.runProbe(healthTimeout(rn.cfg))
					rn.probeCh <- err // buffered(1); single in-flight probe
					rn.inflight.Store(false)
				}()
			}
		}
		for drain := true; drain; {
			select {
			case err := <-rn.probeCh:
				retries := healthRetries(rn.cfg)
				if err == nil {
					if rn.fails > 0 {
						rn.r.Logf("ops: app %s (pm_id %d): health check recovered", rn.cfg.Name, rn.id)
					}
					rn.fails = 0
				} else {
					rn.fails++
					rn.r.Logf("ops: app %s (pm_id %d): health check failed (%d/%d): %v",
						rn.cfg.Name, rn.id, rn.fails, retries, err)
					if rn.fails >= retries {
						rn.fails = 0
						rn.triggerRestart(fmt.Sprintf("health check failed %d time(s)", retries))
					}
				}
			default:
				drain = false
			}
		}
	}
}

// onWatchEvent fires after the watcher's debounce window (watch, row 21).
// Only online/errored apps restart: a deliberately stopped app must not be
// resurrected by file churn; a restarting app is left to its path.
func (rn *runner) onWatchEvent() {
	st, ok := rn.r.Status(rn.id)
	if !ok {
		return
	}
	if st != string(machine.StatusOnline) && st != string(machine.StatusErrored) {
		return
	}
	rn.triggerRestart(fmt.Sprintf("watch: file change under %s", rn.root))
}

// online reports whether the app is currently online (runner goroutine).
func (rn *runner) online() bool {
	st, ok := rn.r.Status(rn.id)
	return ok && st == string(machine.StatusOnline)
}

// triggerRestart logs and restarts through the supervisor (manual-restart
// semantics: resetState + restart_time++, pm2 God.restartProcessId shape).
func (rn *runner) triggerRestart(reason string) {
	rn.r.Logf("ops: restarting app %s (pm_id %d): %s", rn.cfg.Name, rn.id, reason)
	if err := rn.r.Restart(rn.id); err != nil {
		rn.r.Logf("ops: app %s (pm_id %d): restart failed: %v", rn.cfg.Name, rn.id, err)
	}
}

// runProbe executes one health check through /bin/sh -c with a timeout.
// Env carries the app identity; cwd matches the child's cwd resolution.
func (rn *runner) runProbe(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", rn.cfg.HealthCheckCmd)
	cmd.Dir = rn.root
	cmd.Env = append(os.Environ(),
		"PM0_APP_NAME="+rn.cfg.Name,
		"PM0_PM_ID="+strconv.Itoa(rn.id),
	)
	return cmd.Run()
}

// appRoot resolves the app cwd the same way the child sees it: explicit
// cfg.Cwd, else the daemon's working directory (proc.Spec.Cwd empty
// inherits it). Falls back to the daemon dir when getwd fails.
func (rn *runner) appRoot() string {
	if rn.cfg.Cwd != "" {
		return rn.cfg.Cwd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// healthInterval / healthTimeout / healthRetries lift zero knobs to the
// pinned defaults (config.Default carries them, but legacy dumps and
// direct API users can carry zeros here). The probe timeout is clamped to
// the interval so a hung probe never stacks past its own slot.
func healthInterval(cfg config.App) time.Duration {
	if cfg.HealthCheckInterval <= 0 {
		return config.DefaultHealthInterval
	}
	return cfg.HealthCheckInterval
}

func healthTimeout(cfg config.App) time.Duration {
	t := cfg.HealthCheckTimeout
	if t <= 0 {
		t = config.DefaultHealthTimeout
	}
	if iv := healthInterval(cfg); t > iv {
		t = iv
	}
	return t
}

func healthRetries(cfg config.App) int {
	if cfg.HealthCheckRetries <= 0 {
		return config.DefaultHealthRetries
	}
	return cfg.HealthCheckRetries
}

// stopAsync / stopAndWait coordinate shutdown without blocking Sync.
func (rn *runner) stopAsync() {
	select {
	case <-rn.stop:
	default:
		close(rn.stop)
	}
}

func (rn *runner) stopAndWait() {
	rn.stopAsync()
	select {
	case <-rn.done:
	case <-time.After(10 * time.Second):
		// Runner wedged in a Restart callback; leak it rather than hang
		// the caller (daemon shutdown path).
	}
}
