// Package config holds the PM2-compatible app configuration model and
// the pinned defaults from docs/compat.md §1.
//
// Contract rule (compat.md §1): the defaults here must stay byte-identical
// to the compat table. Every field carries a comment naming its table row;
// changing a default without changing the table (and, from M2, the golden
// suite) is a contract violation.
//
// Construction model: call Default() (or DefaultFor) and mutate fields —
// do NOT build an App from a zero struct. Several PM2 defaults are the Go
// zero value of their type (autorestart true, max_restarts 16), so zero
// values cannot mean "unset" here; explicit construction keeps that
// ambiguity out of the codebase.
package config

import "time"

// PM2-pinned scalar defaults (compat.md §1 rows 10-19). Exposed as
// constants so machine/proc/supervisor can reference the same numbers.
const (
	DefaultMaxRestarts = 16
	DefaultMinUptime   = 1000 * time.Millisecond
	// RestartDelay 0 = immediate restart (row 13): PM2's default and a
	// deliberate contract emphasis — a crashing app is relaunched with no
	// delay until max_restarts unstable exits accumulate.
	DefaultRestartDelay = time.Duration(0)
	// Cap for exp_backoff delays: PM2 stops growing the backoff at 15s.
	// Not a PM2-facing key (PM2 hardcodes it); pm0 pins it here so tests
	// can shrink it.
	ExpBackoffCap = 15000 * time.Millisecond
	// Row 18.
	DefaultListenTimeout = 3000 * time.Millisecond
)

// Health-check defaults (divergence 7, M5 — pm0 extension; PM2 has no
// equivalent). Off unless HealthCheckCmd is set; the other knobs lift to
// these values when left zero (0 is also what legacy dumps carry).
const (
	DefaultHealthInterval = 30 * time.Second
	DefaultHealthTimeout  = 5 * time.Second
	DefaultHealthRetries  = 3
)

// Log-rotation defaults (compat.md divergence 12 — the M4 config surface).
// Mirrors internal/logbus; declared here so ecosystem files and the dump
// round-trip reference one source of truth.
const (
	// DefaultRotateMax is the per-sink size that triggers copytruncate
	// rotation (10 MiB).
	DefaultRotateMax = int64(10 << 20)
	// DefaultRotateRetain is how many rotated copies are kept per sink.
	DefaultRotateRetain = 30
)

// DefaultSignal is the kill_signal default (row 16).
const DefaultSignal = "SIGINT"

// DefaultKillTimeout is the kill_timeout default (row 17).
const DefaultKillTimeout = 1600 * time.Millisecond

// App is one application's resolved configuration. Field order follows
// compat.md §1 for reviewability. Only fork mode exists in M1 (row 6);
// cluster lands in M6.
type App struct {
	Name string // row 1 (used with Script as the identity; display-only elsewhere)
	// Script is the path of the app (pm_exec_path). The daemon absolutizes
	// it at registration; the resolved exec target lives in ExecPath/ExecArgs.
	Script string
	Args   []string // row 2

	// Interpreter policy (rows 3-5). Interpreter "" = auto (shebang, then
	// extension map); "none" = direct exec; anything else is the interpreter
	// binary (resolved to an absolute path by Resolve). Resolution happens
	// once at registration: the daemon fills ExecPath/ExecArgs (below) and
	// echoes the effective interpreter here.
	Interpreter     string // effective interpreter ("node", "python3", "none", ...)
	InterpreterArgs []string

	// ExecPath/ExecArgs are the RESOLVED exec target: what machine hands to
	// proc.Spec. Empty ExecPath means "exec Script directly" (binaries and
	// shebang scripts — the kernel resolves the shebang). With an
	// interpreter, ExecPath is the interpreter binary and ExecArgs is
	// [interpreterArgs..., scriptPath] — the script rides as the first
	// argument. jlist echoes Script/Interpreter, never these (§2.2).
	ExecPath string
	ExecArgs []string

	ExecMode  string // row 6: "fork" | "cluster" (M6: SO_REUSEPORT instance sharing)
	Instances int    // row 7: 1 in v1 fork families; cluster groups resolve at start
	// Instance is this process's index inside a cluster group (0-based,
	// NODE_APP_INSTANCE for the child; PM2 parity). Fork apps and
	// fork-mode instance families keep 0 (they use <name>-<i> names).
	Instance  int
	Namespace string // row 8

	Cwd string // row 9: caller cwd by default (left "" → child inherits daemon cwd)

	// Restart policy (rows 10-14). See machine for the exact transition
	// rules; the numbers here are the contract. The transition model was
	// re-derived from observed pm2 source (God.handleExit, identical in
	// 5.4.2 and 7.0.4) during M2 — see compat.md §2.2/§3 changelog.
	Autorestart  bool          // row 10
	MaxRestarts  int           // row 11: unstable-restart budget
	MinUptime    time.Duration // row 12: run length that marks a start "stable"
	RestartDelay time.Duration // row 13: fixed delay between auto restarts
	// ExpBackoffRestartDelay (row 14, corrected): pm2's actual option is a
	// NUMBER — exp_backoff_restart_delay — not the boolean the M0 plan
	// assumed. First backoff sleep = this value; later sleeps grow x1.5
	// capped at ExpBackoffCap (15000ms, observed pm2). 0 = off.
	ExpBackoffRestartDelay time.Duration
	ExpBackoffCap          time.Duration // observed pm2 hardcodes the 15000ms ceiling

	// StopExitCodes (observed pm2 stop_exit_codes): exits with these codes
	// are intentional stops — the app parks stopped, no restart.
	StopExitCodes []int

	MaxMemoryRestart int64 // row 15: 0 = disabled (enforced in M5)

	KillSignal    string        // row 16
	KillTimeout   time.Duration // row 17
	ListenTimeout time.Duration // row 18 (used in M5)
	WaitReady     bool          // row 19 (used in M5)

	CronRestart string // row 20 (M5)
	Watch       bool   // row 21 (M5)

	// Health check (divergence 7, M5 — pm0 extension, off by default;
	// NOT echoed in pm2_env, like the rotation knobs). When Cmd is set,
	// the daemon runs it through /bin/sh -c every Interval while the app
	// is online; Retries consecutive failures restart the app, any
	// success resets the count. Timeout bounds one probe (clamped to
	// Interval at use time).
	HealthCheckCmd      string
	HealthCheckInterval time.Duration
	HealthCheckTimeout  time.Duration
	HealthCheckRetries  int

	// Row 23 divergence: treekill is accepted for ecosystem compat but v1
	// always tree-kills; config keeps the field so describe can echo true.
	Treekill bool

	Time          bool   // row 24
	LogDateFormat string // row 25; "" = no timestamps

	// Log/pid sinks (rows 26-28, path divergence: ~/.pm0). Empty here;
	// the daemon fills concrete paths at registration (M2/M3).
	OutFile   string
	ErrorFile string
	PidFile   string

	MergeLogs bool // row 29

	// Built-in rotation knobs (divergence 12 config surface, M4 — pm0
	// extension; NOT echoed in pm2_env). In-memory semantics: 0 = rotation
	// off, >0 = max bytes per sink. config.Default() pins
	// DefaultRotateMax/DefaultRotateRetain, so "leave the default" and
	// "explicitly off" are distinct values here. The wire/dump encodings
	// map onto this: proto/dump 0 = default, -1 = off (proto3 and legacy
	// dumps cannot carry "explicit off" as 0).
	LogRotateMaxBytes int64
	LogRotateRetain   int

	// Env is the extra environment handed to the child (row 30). The full
	// child env (os.Environ + Env) is what the daemon dumps into
	// pm2_env.env at runtime; --redact-env filtering happens at the JSON
	// layer (M2), never here — the child always gets real values.
	Env map[string]string
}

// Default returns the pinned PM2 defaults (compat.md §1) with Name and
// Script empty. Mutate fields from here; never zero-construct an App.
func Default() App {
	return App{
		Args:            []string{},
		InterpreterArgs: []string{},
		ExecMode:        "fork",
		Instances:       1,
		Instance:        0,
		Namespace:       "default",

		Autorestart:            true,
		MaxRestarts:            DefaultMaxRestarts,
		MinUptime:              DefaultMinUptime,
		RestartDelay:           DefaultRestartDelay,
		ExpBackoffRestartDelay: 0,
		ExpBackoffCap:          ExpBackoffCap,
		StopExitCodes:          []int{},

		MaxMemoryRestart: 0,

		KillSignal:    DefaultSignal,
		KillTimeout:   DefaultKillTimeout,
		ListenTimeout: DefaultListenTimeout,
		WaitReady:     false,

		Treekill: true, // row 23: always true in v1

		HealthCheckCmd:      "",
		HealthCheckInterval: DefaultHealthInterval,
		HealthCheckTimeout:  DefaultHealthTimeout,
		HealthCheckRetries:  DefaultHealthRetries,

		Time:          false,
		LogDateFormat: "",

		MergeLogs: false,

		LogRotateMaxBytes: DefaultRotateMax,
		LogRotateRetain:   DefaultRotateRetain,

		Env: map[string]string{},
	}
}

// DefaultFor returns the pinned defaults with the required identity filled.
func DefaultFor(name, script string) App {
	a := Default()
	a.Name = name
	a.Script = script
	return a
}
