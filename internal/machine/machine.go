//go:build linux

// Package machine implements the per-app supervision state machine: the
// PM2 six-state status vocabulary, the mailbox actor that serializes all
// commands and events for one app, and the restart policy (min_uptime
// stability, max_restarts crash-loop budget, restart_delay, exponential
// backoff).
//
// Architecture: one goroutine (the actor) owns every state transition for
// its app. Callers only send messages into the mailbox and wait for reply
// channels; exit events are observed by selecting on proc.Handle.Done().
// Nothing else mutates state, so no locks are needed on the transition
// paths — a single mutex guards the published Snapshot for concurrent
// readers (jlist-style listing from M2).
//
// State vocabulary is exactly docs/compat.md §3.1 (load-bearing strings;
// scripts parse `jlist`):
//
//	launching  spawn in flight
//	online     process tree alive
//	stopping   kill path started, not finished
//	stopped    intentionally not running (user stop, autorestart=false exit)
//	waiting    parked between restarts (restart_delay / backoff sleep)
//	errored    crash-loop exhausted (max_restarts unstable restarts hit)
//
// Restart policy (compat.md §1 rows 10-14) — re-derived in M2 from the
// observed pm2 source (God.handleExit, byte-identical in 5.4.2 and 7.0.4):
//
//   - A crash counts as UNSTABLE only when both windows hold:
//     (now - created_at) < min_uptime*max_restarts  (the outer window —
//     created_at = registration time, reset by manual restart/reload), and
//     (now - pm_uptime) < min_uptime (the inner window — this run was
//     short). A clean exit-0 inside min_uptime still counts (compat.md §1
//     emphasis). Outside the outer window the app restarts forever and
//     never parks — that is real pm2 behavior, kept deliberately.
//   - unstable_restarts >= max_restarts parks the app errored; observed
//     pm2 then RESETS unstable_restarts to 0 and sets created_at = null
//     (the jlist shows unstable 0 + null created_at on an errored app).
//   - Auto relaunch waits restart_delay (default 0 = immediate), or — when
//     exp_backoff_restart_delay is configured — a delay starting at that
//     value and growing x1.5 per crash, capped at 15000ms (observed pm2;
//     no jitter). The parked status is the two-word "waiting restart".
//   - Exit codes listed in stop_exit_codes are intentional stops: the app
//     parks stopped, no restart.
//   - Manual start/restart applies pm2's God.resetState: created_at = now,
//     unstable_restarts = 0, backoff curve reset — and always relaunches,
//     even from errored/waiting.
//
// restart_time counts every relaunch (auto and manual) — corrected in M1
// to match observed pm2 behavior (the ↺ column counts crash restarts too);
// see docs/compat.md §2.2.
package machine

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/cron"
	"github.com/pm0/pm0/internal/proc"
)

// Status is one of the six load-bearing jlist status strings (§3.1).
type Status string

const (
	StatusLaunching Status = "launching"
	StatusOnline    Status = "online"
	StatusStopping  Status = "stopping"
	StatusStopped   Status = "stopped"
	StatusWaiting   Status = "waiting restart"
	StatusErrored   Status = "errored"
)

// Valid reports whether s is in the pinned vocabulary.
func (s Status) Valid() bool {
	switch s {
	case StatusLaunching, StatusOnline, StatusStopping,
		StatusStopped, StatusWaiting, StatusErrored:
		return true
	}
	return false
}

// Snapshot is the runtime half of a jlist element (compat.md §2.2 runtime
// keys; config-derived keys ride along the immutable config.App).
type Snapshot struct {
	Name string // display name (config echo)

	Status           Status
	Pid              int // 0 when not running
	RestartTime      int // every relaunch (auto + manual), §2.2 as corrected
	UnstableRestarts int // consecutive restarts faster than min_uptime
	CreatedAtMs      int64
	// CreatedAtIsNull: observed pm2 nulls created_at when a crash loop
	// parks the app errored (God.handleExit) — JSON renders null.
	CreatedAtIsNull bool
	PmUptimeMs      int64 // epoch ms of last start; 0 when never started
	ExitCode        *int  // nil while running; nil for signal deaths (pm2 emits null)
	LastExitSig     int   // signal number of the last signal death (0 = none); pm0 extension, not in jlist
	// LastExitReason is "oom" when the last death was a kernel OOM kill
	// (divergence 6) and "" otherwise. pm0 extension: describe-only,
	// additive, never parsed by pm2 scripts.
	LastExitReason string
	// TreeMode / CgroupPath / LeaderStart describe the LIVE tree for the
	// monitoring paths (daemon list/monit attribution over a shared /proc
	// snapshot): kill mode, cgroup dir in cgroup modes, and the leader's
	// spawn-time starttime (the pid-reuse anchor). Zero when not running.
	// Immutable per handle; published at launch/adopt, cleared with it.
	TreeMode    string
	CgroupPath  string
	LeaderStart uint64
}

// ErrNoScript is returned when an app is constructed without one.
var ErrNoScript = errors.New("machine: app config needs a Script")

// Options tune the actor for tests. Zero value is production-ready.
type Options struct {
	// ID is the pm_id (supervisor allocates; §3.2). Diagnostic only here.
	ID int
	// Now overrides time.Now (tests).
	Now func() time.Time
	// After overrides time.After for the restart-park timer (tests).
	After func(time.Duration) <-chan time.Time
	// Rand drives backoff jitter (tests seed it). nil = global rand.
	Rand *rand.Rand
	// InitialRestarts seeds restart_time (cluster reload: PM2 counts the
	// replacement as restart_time+1 of the worker it replaces).
	InitialRestarts int
	// ClusterPreloadPath is the cluster preload (internal/cluster) written
	// into PM0_HOME. Used only for ExecMode cluster launches; empty in
	// unit tests skips the injection (fork apps never touch it).
	ClusterPreloadPath string
}

// App is one supervised application: the mailbox actor plus its published
// snapshot. Methods Start/Stop/Restart/Delete/Signal are the command
// surface; each blocks until the actor has fully processed it (Stop/Restart
// include the kill path, bounded by kill_timeout + grace).
type App struct {
	cfg      config.App
	id       int
	launchFn func(proc.Spec) (*proc.Handle, error)
	attrib   string // unique attribution name for proc (cgroup dir + orphan marker)

	now   func() time.Time
	after func(time.Duration) <-chan time.Time
	rand  *rand.Rand

	mbox chan msg
	done chan struct{} // closed when the actor exits (delete)

	// cluster preload path (M6): --require'd before the script for
	// ExecMode cluster launches. Empty for fork apps and bare unit tests.
	preloadPath string

	mu            sync.Mutex // guards everything below (snapshot readers)
	status        Status
	handle        *proc.Handle
	pid           int
	restarts      int
	unstable      int
	crashSeries   time.Time
	created       time.Time
	uptime        time.Time // last start; zero = never started
	exitCode      *int
	exitSig       int
	exitReason    string           // "" | "oom" (divergence 6)
	createdIsNull bool             // created_at null (errored park, §2.2)
	prevBackoff   time.Duration    // last backoff sleep (pm2 prev_restart_delay)
	park          <-chan time.Time // non-nil while parked in waiting
	readyTimer    <-chan time.Time // wait_ready listen_timeout deadline (launching only)

	// Tree descriptor mirror (mu-guarded): the live handle's immutable
	// identity for snapshot-based monitoring attribution. Zero when not
	// running. Written only on the actor goroutine.
	treeMode        string
	treeCgPath      string
	treeLeaderStart uint64
}

// New validates cfg, builds the actor, and starts its goroutine. The app
// registers as stopped (never started) — call Start to launch it.
func New(cfg config.App, l *proc.Launcher, opts Options) (*App, error) {
	a, err := buildApp(cfg, l, opts)
	if err != nil {
		return nil, err
	}
	go a.loop()
	return a, nil
}

// Adopt registers an already-RUNNING tree under a new App actor: the
// daemon update path (re-exec preserves the children — the new process
// image adopts them) and resurrect-after-crash recovery. h is the handle
// rebuilt by proc.AdoptTree; startedAt is the leader's real process start
// time (restores pm_uptime truthfully, compat.md §2.2). The actor resumes
// full ownership: exit events drive the normal restart policy from here on.
func Adopt(cfg config.App, l *proc.Launcher, opts Options, h *proc.Handle, startedAt time.Time) (*App, error) {
	a, err := buildApp(cfg, l, opts)
	if err != nil {
		return nil, err
	}
	a.handle = h
	a.pid = h.PID()
	a.uptime = startedAt
	a.exitCode = nil // running: §2.2 null
	a.exitSig = 0
	a.status = StatusOnline
	a.mu.Lock()
	a.publishTreeLocked(h)
	a.mu.Unlock()
	go a.loop()
	return a, nil
}

// buildApp validates cfg and builds the App struct without starting the
// actor goroutine (New and Adopt share it; each starts the loop itself).
func buildApp(cfg config.App, l *proc.Launcher, opts Options) (*App, error) {
	if cfg.Script == "" {
		return nil, ErrNoScript
	}
	if cfg.Name == "" {
		return nil, errors.New("machine: app config needs a Name")
	}
	if !cfg.Treekill {
		// Row 23 divergence: accepted for compat, always tree-kill. Refuse
		// silently flipping it off at this layer.
		cfg.Treekill = true
	}
	if cfg.CronRestart != "" {
		// Row 20: an invalid cron_restart fails the START loudly (compat
		// rule: drift is loud at invocation time), never silently no-ops
		// in the daemon loop.
		if _, err := cron.Parse(cfg.CronRestart); err != nil {
			return nil, fmt.Errorf("machine: app %s: cron_restart: %w", cfg.Name, err)
		}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.After == nil {
		opts.After = time.After
	}
	id := opts.ID
	a := &App{
		cfg:         cfg,
		id:          id,
		launchFn:    l.Launch,
		attrib:      fmt.Sprintf("%s-%d", proc.SanitizeName(cfg.Name), id),
		now:         opts.Now,
		after:       opts.After,
		rand:        opts.Rand,
		mbox:        make(chan msg),
		done:        make(chan struct{}),
		created:     opts.Now(),
		status:      StatusStopped,
		restarts:    opts.InitialRestarts,
		preloadPath: opts.ClusterPreloadPath,
	}
	return a, nil
}

// message types ---------------------------------------------------------

type msg interface{ handle(a *App) }

type startMsg struct{ reply chan error }
type stopMsg struct {
	reply       chan error
	gracefulIPC bool // reload stop: 'shutdown' node-IPC message first
}
type restartMsg struct{ reply chan error }
type deleteMsg struct {
	reply       chan error
	gracefulIPC bool // reload delete: 'shutdown' node-IPC message first
}
type signalMsg struct {
	sig   syscall.Signal
	reply chan error
}
type treeMsg struct{ reply chan []int }
type updateEnvMsg struct {
	env   map[string]string
	reply chan error
}

func (startMsg) handle(*App)     {}
func (stopMsg) handle(*App)      {}
func (restartMsg) handle(*App)   {}
func (deleteMsg) handle(*App)    {}
func (signalMsg) handle(*App)    {}
func (treeMsg) handle(*App)      {}
func (updateEnvMsg) handle(*App) {}

// actor loop ------------------------------------------------------------

// loop is the actor body. Single owner of all transitions: it selects over
// the mailbox, the current handle's Done (exit events), and the restart
// park timer. Nil channels (not running / not parked) block in select —
// the standard single-goroutine state machine idiom.
func (a *App) loop() {
	for {
		a.mu.Lock()
		st := a.status
		var exitCh, readyCh <-chan struct{}
		var readyTimer <-chan time.Time
		if a.handle != nil && (st == StatusLaunching || st == StatusOnline) {
			exitCh = a.handle.Done()
		}
		if st == StatusLaunching && a.handle != nil {
			// wait_ready gate (row 19): launching until the child sends
			// the node-IPC string "ready" or listen_timeout elapses
			// (observed pm2 forces online at the deadline). Both channels
			// are nil when not launching — select never fires them.
			readyCh = a.handle.Ready()
			readyTimer = a.readyTimer
		}
		park := a.park
		a.mu.Unlock()

		select {
		case m := <-a.mbox:
			switch m := m.(type) {
			case startMsg:
				m.reply <- a.onStart()
			case stopMsg:
				m.reply <- a.onStop(m.gracefulIPC)
			case restartMsg:
				m.reply <- a.onRestart()
			case deleteMsg:
				m.reply <- a.onDelete(m.gracefulIPC)
				close(a.done)
				return
			case signalMsg:
				m.reply <- a.onSignal(m.sig)
			case treeMsg:
				m.reply <- a.currentTreePids()
			case updateEnvMsg:
				m.reply <- a.onUpdateEnv(m.env)
			}
		case <-exitCh:
			a.onExit()
		case <-readyCh:
			a.onReady() // child sent process.send('ready')
		case <-readyTimer:
			a.onReadyTimeout() // listen_timeout elapsed: force online
		case <-park:
			a.mu.Lock()
			a.park = nil
			a.mu.Unlock()
			a.relaunch() // restart_delay / backoff elapsed
		}
	}
}

// commands (run inside the actor) ----------------------------------------

// onStart starts a stopped/errored app; idempotent no-op while online;
// from waiting it cancels the park and relaunches immediately.
func (a *App) onStart() error {
	a.mu.Lock()
	st := a.status
	park := a.park
	a.mu.Unlock()
	a.clearPark(park)

	switch st {
	case StatusOnline, StatusLaunching, StatusStopping:
		return nil // idempotent (daemon-level start creates new apps)
	case StatusWaiting:
		a.relaunch() // user start during backoff/delay window
		return nil
	default: // stopped, errored
		a.mu.Lock()
		a.resetStateLocked() // pm2 God.resetState: fresh crash epoch
		a.mu.Unlock()
		return a.launch()
	}
}

// onStop stops a running app; no-op success when already stopped (pm2
// stop on a stopped app is success). The kill path runs here, bounded by
// kill_timeout + grace (I5). gracefulIPC (cluster reload stop): the
// 'shutdown' node-IPC message is delivered first and the app gets the
// grace window to exit on its own before the kill path starts (PM2
// God/Reload.js softCleanDeleteProcess).
func (a *App) onStop(gracefulIPC bool) error {
	a.clearPark(a.parkSnapshot())
	if gracefulIPC {
		a.shutdownPhase()
	}
	return a.stopTree()
}

// onRestart is pm2 restart semantics: stop if running, then (re)launch —
// from any state, including errored and waiting.
func (a *App) onRestart() error {
	a.clearPark(a.parkSnapshot())
	if err := a.stopTree(); err != nil {
		return err
	}
	a.mu.Lock()
	a.resetStateLocked() // pm2 God.resetState: created_at=now, budget reset
	a.mu.Unlock()
	return a.relaunch() // manual restart counts in restart_time
}

// resetStateLocked applies pm2's God.resetState (manual restart/reload):
// created_at = now (clears the errored null), unstable_restarts = 0, and
// the backoff curve restarts from its configured base. Caller holds a.mu.
func (a *App) resetStateLocked() {
	a.unstable = 0
	a.created = a.now()
	a.createdIsNull = false
	a.prevBackoff = 0
}

// onDelete stops the app and terminates the actor. The supervisor removes
// the registry entry (and frees the pm_id) after the reply.
func (a *App) onDelete(gracefulIPC bool) error {
	a.clearPark(a.parkSnapshot())
	if gracefulIPC {
		a.shutdownPhase()
	}
	if err := a.stopTree(); err != nil {
		// Even on a failed kill path the app is being removed: report the
		// error but still tear the actor down.
		a.mu.Lock()
		a.status = StatusErrored
		a.mu.Unlock()
		return err
	}
	a.mu.Lock()
	a.status = StatusStopped
	a.mu.Unlock()
	return nil
}

// shutdownPhase delivers the node-IPC 'shutdown' message and waits up to
// kill_timeout for a self-exit (cluster reload handoff). Runs inside the
// actor: blocking here is bounded and mirrors how stopTree blocks. The
// child exit event stays queued — stopTree's cleanup makes the loop's
// exitCh case a no-op afterwards.
func (a *App) shutdownPhase() {
	a.mu.Lock()
	h := a.handle
	timeout := a.cfg.KillTimeout
	a.mu.Unlock()
	if h == nil {
		return
	}
	if timeout <= 0 {
		timeout = 1600 * time.Millisecond
	}
	if err := h.SendShutdown(); err != nil {
		return // no live channel: the kill path takes it from here
	}
	select {
	case <-h.Done(): // app handled 'shutdown' and exited
	case <-a.after(timeout): // fall through to the signal kill path
	}
}

func (a *App) onSignal(sig syscall.Signal) error {
	a.mu.Lock()
	h := a.handle
	a.mu.Unlock()
	if h == nil {
		return fmt.Errorf("app %q is not running", a.cfg.Name)
	}
	return h.Signal(sig)
}

// stopTree runs the kill path on a live tree. status → stopping first so
// concurrent snapshots observe the pinned vocabulary mid-flight.
func (a *App) stopTree() error {
	a.mu.Lock()
	h := a.handle
	if h != nil {
		a.status = StatusStopping
	}
	a.mu.Unlock()

	if h == nil {
		// Not running (stopped, or parked in waiting, or parked errored):
		// a user stop parks the app stopped unconditionally.
		a.mu.Lock()
		a.status = StatusStopped
		a.mu.Unlock()
		return nil
	}

	err := h.Stop()
	a.mu.Lock()
	a.handle = nil
	a.pid = 0
	a.publishTreeLocked(nil)
	if err != nil {
		// Kill path failed with survivors (I1 violation reported by proc).
		// Truthful status: errored, and surface the error.
		a.status = StatusErrored
	} else {
		a.status = StatusStopped
		recordExitLocked(a, h)
	}
	a.mu.Unlock()
	return err
}

// recordExitLocked copies the last exit info into the snapshot fields.
// Callers hold a.mu. Used by stopTree (the graceful SIGINT death of a user
// stop is pm2's observable "last exit code") — onExit has its own inline
// copy since it also drives the restart policy.
func recordExitLocked(a *App, h *proc.Handle) {
	select {
	case <-h.Done():
		info := h.ExitResult()
		// Observed pm2 records exit_code = code || 0 — signal deaths read
		// as 0, never null (null/absent only while running). LastExitSig
		// keeps the signal visible (pm0 extension).
		code := info.ExitCode
		if info.Signaled {
			code = 0
			a.exitSig = info.TermSig
		} else {
			a.exitSig = 0
		}
		if info.OomKill {
			a.exitReason = "oom"
		} else {
			a.exitReason = ""
		}
		a.exitCode = &code
	default:
		// Leader not reaped yet (rare): leave the previous exit fields.
	}
}

// launch spawns the process. Caller sets unstable=0 for manual paths.
// restart_time increments on every relaunch (auto + manual) per the
// corrected §2.2 semantics — the initial start is not a restart.
func (a *App) launch() error {
	// Resolved exec target: interpreter (or script itself) plus interpreter
	// args prefix; falls back to the script when ExecPath is empty
	// (binaries and shebang scripts — the kernel resolves the shebang).
	bin := a.cfg.ExecPath
	if bin == "" {
		bin = a.cfg.Script
	}
	argv := a.cfg.Args
	if len(a.cfg.ExecArgs) > 0 {
		argv = append(append([]string{}, a.cfg.ExecArgs...), a.cfg.Args...)
	}
	env := buildEnv(a.cfg.Env)
	if a.cfg.ExecMode == "cluster" {
		// M6 cluster instance: the reuseport preload rides before the
		// script (ExecArgs = [interpArgs..., script]) and the instance
		// index lands in the env the PM2 way (NODE_APP_INSTANCE).
		if a.preloadPath != "" && len(argv) > 0 {
			head := append([]string{}, argv[:len(argv)-1]...)
			argv = append(head, "--require", a.preloadPath, argv[len(argv)-1])
		}
		if env == nil {
			// buildEnv returns nil when the app has no extra env and proc
			// would fall back to os.Environ(); materialize it here so the
			// instance pair can ride along (proc still appends the
			// attribution marker last).
			env = os.Environ()
		}
		env = append(env, "NODE_APP_INSTANCE="+strconv.Itoa(a.cfg.Instance))
	}
	spec := proc.Spec{
		Name:        a.attrib,
		BinPath:     bin,
		Args:        argv,
		Env:         env,
		Cwd:         a.cfg.Cwd,
		KillSignal:  int(parseKillSignal(a.cfg.KillSignal)),
		KillTimeout: a.cfg.KillTimeout,
		OutFile:     a.cfg.OutFile, // rows 26-29: log/pid sinks; empty = /dev/null
		ErrFile:     a.cfg.ErrorFile,
	}

	a.mu.Lock()
	a.status = StatusLaunching
	a.mu.Unlock()

	h, err := a.launchFn(spec)

	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		// Spawn failure (missing binary, cgroup creation failure): pm2
		// parks the app errored and does not spin on fork failures.
		a.status = StatusErrored
		a.handle = nil
		a.pid = 0
		a.publishTreeLocked(nil)
		return fmt.Errorf("start %s: %w", a.cfg.Name, err)
	}
	a.handle = h
	a.pid = h.PID()
	a.publishTreeLocked(h)
	a.uptime = a.now()
	a.exitCode = nil // §2.2: null while running
	a.exitSig = 0
	a.exitReason = "" // fresh run, fresh reason
	if a.cfg.WaitReady {
		// Row 19: hold launching until the child sends the node-IPC
		// string "ready" (process.send) or listen_timeout elapses —
		// whichever first (observed pm2 ForkMode ready gate). Exit
		// during launching still drives the normal restart policy.
		a.status = StatusLaunching
		a.readyTimer = a.after(a.cfg.ListenTimeout)
	} else {
		a.status = StatusOnline
	}
	a.writePidFileLocked()
	return nil
}

// onReady marks the app online after the child's process.send('ready')
// (wait_ready, row 19). Runs inside the actor; ignored unless launching
// (a stale frame after a stop/exit must not resurrect the status).
func (a *App) onReady() {
	a.mu.Lock()
	if a.status == StatusLaunching {
		a.status = StatusOnline
		a.readyTimer = nil
	}
	a.mu.Unlock()
}

// onReadyTimeout is the listen_timeout deadline (row 18): a wait_ready app
// that never sent 'ready' is forced online, exactly like pm2's fork-mode
// ready timer. Runs inside the actor; ignored unless still launching.
func (a *App) onReadyTimeout() {
	a.mu.Lock()
	if a.status == StatusLaunching {
		a.status = StatusOnline
		a.readyTimer = nil
	}
	a.mu.Unlock()
}

// writePidFileLocked records the leader pid in the app's pid file (row 28,
// best effort — pm2 also treats pid files as advisory). Caller holds a.mu.
func (a *App) writePidFileLocked() {
	if a.cfg.PidFile == "" || a.pid <= 0 {
		return
	}
	if err := os.WriteFile(a.cfg.PidFile, []byte(strconv.Itoa(a.pid)+"\n"), 0o644); err != nil {
		// Advisory only: never fail a launch over the pid file.
		_ = err
	}
}

// Attrib returns the app's unique attribution marker (the launcher spec
// name: sanitized name + pm id — cgroup dir and orphan-scan marker).
// Immutable after construction.
func (a *App) Attrib() string { return a.attrib }

// publishTreeLocked mirrors the handle's immutable tree descriptor into
// the snapshot fields. Caller holds a.mu.
func (a *App) publishTreeLocked(h *proc.Handle) {
	if h == nil {
		a.treeMode = ""
		a.treeCgPath = ""
		a.treeLeaderStart = 0
		return
	}
	ti := h.TreeInfo()
	a.treeMode = string(ti.Mode)
	a.treeCgPath = ti.CgroupPath
	a.treeLeaderStart = ti.LeaderStart
}

// relaunch counts a restart and spawns. Auto-restart paths and manual
// start/restart all funnel here.
func (a *App) relaunch() error {
	a.mu.Lock()
	a.restarts++
	a.mu.Unlock()
	return a.launch()
}

// onExit consumes the leader's Done event. Only exits observed while the
// app was launching/online drive the restart policy; exits during a
// user-initiated stop are bookkeeping only.
func (a *App) onExit() {
	a.mu.Lock()
	h := a.handle
	if h == nil {
		a.mu.Unlock()
		return
	}
	info := h.ExitResult()
	a.handle = nil
	a.pid = 0
	a.publishTreeLocked(nil)
	a.readyTimer = nil // stale gate deadline never fires into the next run
	// Observed pm2: exit_code = code || 0 — signal deaths record 0, not
	// null (null appears only while running / before any exit).
	code := info.ExitCode
	if info.Signaled {
		code = 0
		a.exitSig = info.TermSig
	} else {
		a.exitSig = 0
	}
	if info.OomKill {
		a.exitReason = "oom" // divergence 6: kernel OOM kill observed
	} else {
		a.exitReason = ""
	}
	a.exitCode = &code
	st := a.status
	a.mu.Unlock()

	// Late deliveries after a stop path already handled the tree.
	if st != StatusLaunching && st != StatusOnline {
		return
	}

	now := a.now()

	// Intentional stops (observed pm2 stopping condition): autorestart off,
	// or an exit code listed in stop_exit_codes. Park stopped.
	a.mu.Lock()
	stopExit := false
	for _, c := range a.cfg.StopExitCodes {
		if !info.Signaled && info.ExitCode == c {
			stopExit = true
			break
		}
	}
	if !a.cfg.Autorestart || stopExit {
		// Row 10: autorestart=false parks stopped on ANY exit, crash or
		// clean, with the exit code recorded.
		a.status = StatusStopped
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()

	// Stability bookkeeping — crashes within min_uptime or early error exits
	// increment the unstable counter. Once max_restarts crashes accumulate
	// within the active crash series window, the app parks errored.
	a.mu.Lock()
	uptime := now.Sub(a.uptime)
	outerWindow := a.cfg.MinUptime * time.Duration(a.cfg.MaxRestarts)
	if a.cfg.MinUptime >= time.Second && outerWindow < 60*time.Second {
		outerWindow = 60 * time.Second
	}
	if a.createdIsNull {
		outerWindow = 0 // created_at null: no window, never counts
	}

	isUnstable := uptime < a.cfg.MinUptime
	if a.cfg.MinUptime >= time.Second && info.ExitCode != 0 && uptime < 5*time.Second {
		isUnstable = true
	}

	if isUnstable {
		if a.unstable == 0 || a.crashSeries.IsZero() {
			a.crashSeries = now
		}
		refTime := a.created
		if !a.crashSeries.IsZero() && a.crashSeries.After(refTime) {
			refTime = a.crashSeries
		}
		if outerWindow > 0 && now.Sub(refTime) < outerWindow {
			a.unstable++
		} else {
			a.crashSeries = now
			a.unstable = 1
		}
	} else if uptime >= a.cfg.MinUptime {
		a.unstable = 0
		a.crashSeries = time.Time{}
	}

	overBudget := a.unstable >= a.cfg.MaxRestarts
	if overBudget {
		// Observed pm2: parking errored RESETS the displayed counter and
		// nulls created_at (§2.2). A manual restart restores both.
		a.unstable = 0
		a.createdIsNull = true
		a.crashSeries = time.Time{}
	}
	a.mu.Unlock()

	if overBudget {
		a.mu.Lock()
		a.status = StatusErrored
		a.mu.Unlock()
		return
	}

	delay, isBackoff := NextRestartDelay(a.cfg, a.prevBackoff)
	if isBackoff {
		a.mu.Lock()
		a.prevBackoff = delay
		a.mu.Unlock()
	}
	if delay > 0 {
		a.mu.Lock()
		a.status = StatusWaiting
		a.park = a.after(delay)
		a.mu.Unlock()
		return
	}
	_ = a.relaunch()
}

// snapshot / internal accessors -------------------------------------------

func (a *App) parkSnapshot() <-chan time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.park
}

func (a *App) clearPark(park <-chan time.Time) {
	if park == nil {
		return
	}
	a.mu.Lock()
	if a.park == park { // only clear if not already replaced
		a.park = nil
	}
	a.mu.Unlock()
}

// Snapshot copies the runtime state (jlist runtime keys).
func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	return Snapshot{
		Name:             a.cfg.Name,
		Status:           a.status,
		Pid:              a.pid,
		RestartTime:      a.restarts,
		UnstableRestarts: a.unstable,
		CreatedAtMs:      a.created.UnixMilli(),
		CreatedAtIsNull:  a.createdIsNull,
		PmUptimeMs:       a.uptime.UnixMilli(), // 0 when never started
		ExitCode:         a.exitCode,
		LastExitSig:      a.exitSig,
		LastExitReason:   a.exitReason,
		TreeMode:         a.treeMode,
		CgroupPath:       a.treeCgPath,
		LeaderStart:      a.treeLeaderStart,
	}
}

// Config returns the app's resolved configuration.
func (a *App) Config() config.App { return a.cfg }

// ID returns the pm_id.
func (a *App) ID() int { return a.id }

// Done closes when the actor exits (after Delete).
func (a *App) Done() <-chan struct{} { return a.done }

// public command surface (mailbox senders) -------------------------------

// Start launches the app (no-op when already running). It blocks until the
// actor processes the command.
func (a *App) Start() error {
	reply := make(chan error, 1)
	a.mbox <- startMsg{reply: reply}
	return <-reply
}

// Stop runs the kill path; no-op success when not running.
func (a *App) Stop() error {
	reply := make(chan error, 1)
	a.mbox <- stopMsg{reply: reply}
	return <-reply
}

// Restart stops (if running) and relaunches, resetting the crash-loop
// budget.
func (a *App) Restart() error {
	reply := make(chan error, 1)
	a.mbox <- restartMsg{reply: reply}
	return <-reply
}

// Delete stops the app and retires the actor. The App is unusable after.
func (a *App) Delete() error {
	reply := make(chan error, 1)
	a.mbox <- deleteMsg{reply: reply}
	return <-reply
}

// StopGraceful is Stop with the cluster-reload handoff: the node-IPC
// 'shutdown' message goes first, then the kill path.
func (a *App) StopGraceful() error {
	reply := make(chan error, 1)
	a.mbox <- stopMsg{reply: reply, gracefulIPC: true}
	return <-reply
}

// DeleteGraceful is Delete with the cluster-reload handoff (see
// StopGraceful). The App is unusable after.
func (a *App) DeleteGraceful() error {
	reply := make(chan error, 1)
	a.mbox <- deleteMsg{reply: reply, gracefulIPC: true}
	return <-reply
}

// Signal forwards a signal to the running tree (pgid mode verifies the
// leader's starttime — invariant I6).
func (a *App) Signal(sig syscall.Signal) error {
	reply := make(chan error, 1)
	a.mbox <- signalMsg{sig: sig, reply: reply}
	return <-reply
}

// TreePids lists the pids currently considered part of the app's tree
// (cgroup.procs in cgroup modes; pgrp members + attributed orphans in
// pgid mode). nil when not running. Asked via the mailbox so the handle
// stays actor-owned.
func (a *App) TreePids() []int {
	reply := make(chan []int, 1)
	a.mbox <- treeMsg{reply: reply}
	return <-reply
}

// UpdateEnv replaces the app's extra environment (restart --update-env:
// the CLI re-reads the current shell and the merged env is dumped into
// pm2_env.env on the next snapshot). Runs on the actor.
func (a *App) UpdateEnv(env map[string]string) error {
	reply := make(chan error, 1)
	a.mbox <- updateEnvMsg{env: env, reply: reply}
	return <-reply
}

func (a *App) onUpdateEnv(env map[string]string) error {
	if env == nil {
		env = map[string]string{}
	}
	a.mu.Lock()
	a.cfg.Env = env
	a.mu.Unlock()
	return nil
}

// currentTreePids runs inside the actor.
func (a *App) currentTreePids() []int {
	a.mu.Lock()
	h := a.handle
	a.mu.Unlock()
	if h == nil {
		return nil
	}
	return h.TreePids()
}

// restart policy ---------------------------------------------------------

// NextRestartDelay computes the sleep before the next auto relaunch
// (compat.md rows 13/14, observed pm2 curve):
//
//   - exp_backoff_restart_delay unset: fixed restart_delay (default 0 =
//     immediate).
//   - exp_backoff_restart_delay set: the FIRST sleep is the configured
//     value, then each subsequent sleep grows x1.5 over the previous one,
//     floored, capped at ExpBackoffCap (15000ms — observed pm2
//     prev_restart_delay = Math.floor(Math.min(15000, prev * 1.5)); no
//     jitter). prev == 0 means no crash since the last (re)start epoch.
//
// The bool result reports that the backoff curve is active (the caller
// tracks the growing previous delay).
func NextRestartDelay(cfg config.App, prev time.Duration) (time.Duration, bool) {
	if cfg.ExpBackoffRestartDelay > 0 {
		if prev <= 0 {
			return cfg.ExpBackoffRestartDelay, true
		}
		next := prev * 3 / 2 // floor(prev*1.5)
		if next > cfg.ExpBackoffCap {
			next = cfg.ExpBackoffCap
		}
		return next, true
	}
	return cfg.RestartDelay, false
}

// env / signal helpers ----------------------------------------------------

// buildEnv merges cfg.Env over the parent environment. Keys are replaced
// (never duplicated — first-match-wins getenv would keep the stale value)
// and the merged pairs are appended sorted for deterministic child env.
func buildEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return nil // proc falls back to os.Environ() + marker
	}
	base := os.Environ()
	drop := make(map[string]struct{}, len(extra))
	for k := range extra {
		drop[k] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(extra))
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := drop[k]; ok {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	return out
}

// parseKillSignal converts the config string ("SIGINT", "INT", "sigint")
// into its syscall value; unknown names fall back to the pinned default
// (config validation should have caught them earlier — M4 CLI layer).
func parseKillSignal(name string) syscall.Signal {
	if sig, ok := signalByName(name); ok {
		return sig
	}
	return syscall.SIGINT
}

// ParseKillSignal is the exported form for the daemon layer: the second
// result reports whether the name was recognized at all.
func ParseKillSignal(name string) (syscall.Signal, bool) {
	return signalByName(name)
}

var signalTable = map[string]syscall.Signal{
	"SIGABRT": syscall.SIGABRT, "SIGALRM": syscall.SIGALRM, "SIGBUS": syscall.SIGBUS,
	"SIGCHLD": syscall.SIGCHLD, "SIGCONT": syscall.SIGCONT, "SIGFPE": syscall.SIGFPE,
	"SIGHUP": syscall.SIGHUP, "SIGILL": syscall.SIGILL, "SIGINT": syscall.SIGINT,
	"SIGIO": syscall.SIGIO, "SIGIOT": syscall.SIGIOT, "SIGKILL": syscall.SIGKILL,
	"SIGPIPE": syscall.SIGPIPE, "SIGPROF": syscall.SIGPROF, "SIGQUIT": syscall.SIGQUIT,
	"SIGSEGV": syscall.SIGSEGV, "SIGSTOP": syscall.SIGSTOP, "SIGSYS": syscall.SIGSYS,
	"SIGTERM": syscall.SIGTERM, "SIGTRAP": syscall.SIGTRAP, "SIGTSTP": syscall.SIGTSTP,
	"SIGTTIN": syscall.SIGTTIN, "SIGTTOU": syscall.SIGTTOU, "SIGURG": syscall.SIGURG,
	"SIGUSR1": syscall.SIGUSR1, "SIGUSR2": syscall.SIGUSR2, "SIGVTALRM": syscall.SIGVTALRM,
	"SIGWINCH": syscall.SIGWINCH, "SIGXCPU": syscall.SIGXCPU, "SIGXFSZ": syscall.SIGXFSZ,
}

func signalByName(name string) (syscall.Signal, bool) {
	s := strings.ToUpper(strings.TrimSpace(name))
	if s == "" {
		return 0, false
	}
	if sig, ok := signalTable[s]; ok {
		return sig, true
	}
	if !strings.HasPrefix(s, "SIG") {
		sig, ok := signalTable["SIG"+s]
		return sig, ok
	}
	return 0, false
}
