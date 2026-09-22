// Package proc owns process lifecycle at the platform level: spawning app
// processes, tracking their exit via a dedicated reaper, and terminating
// whole process trees through one of three kill paths (cgroup.kill,
// cgroup freeze-drain, or pgid fallback).
//
// This is the M0 platform spike. It knows nothing about apps, state
// machines, or persistence — those live in internal/daemon (M1+).
//
// Invariants implemented here (plan table, M0 subset):
//
//	I1  after Stop, zero live processes remain in the tree
//	I5  termination completes within kill_timeout + fixed grace, even for
//	    trees that ignore the graceful signal
//	I6  fallback-path signals never hit a reused pid: every signal that
//	    targets a pid recorded earlier is gated on /proc/<pid>/stat
//	    starttime verification
package proc

import (
	"errors"
	"time"
)

// Mode is the effective kill-path strategy for a launcher/handle.
type Mode string

const (
	// ModeAuto asks Detect to pick the best available mode.
	ModeAuto Mode = "auto"
	// ModeCgroupKill: per-app cgroup v2 subtree, kernel >= 5.14.
	// Force path is the atomic cgroup.kill.
	ModeCgroupKill Mode = "cgroup-kill"
	// ModeCgroupFreeze: per-app cgroup v2 subtree, kernel 5.2..5.13.
	// Force path is freeze -> SIGKILL drain of cgroup.procs -> unfreeze.
	ModeCgroupFreeze Mode = "cgroup-freeze"
	// ModePgid: process-group fallback (cgroup v1 hosts, no delegation).
	// Force path is verified kill(-pgid, SIGKILL) + reparented-orphan sweep.
	ModePgid Mode = "pgid"
)

// Fixed grace added after kill_timeout before the kill path gives up
// (invariant I5: kill_signal -> kill_timeout -> SIGKILL -> grace). The
// grace covers SIGKILL delivery, exit, and the empty-verification polls.
const GracePeriod = 2 * time.Second

// Defaults mirror PM2 (docs/compat.md rows 16/17). Keep in sync with
// internal/config defaults and the compat table.
const (
	DefaultKillSignal  = "SIGINT"
	DefaultKillTimeout = 1600 * time.Millisecond
)

// Sentinel errors surfaced by the kill paths.
var (
	// ErrStalePid means the pid a signal was about to target no longer
	// carries the starttime recorded for it (died, or was reused). The
	// signal was NOT sent (invariant I6).
	ErrStalePid = errors.New("proc: stale pid (starttime mismatch), signal not sent")
	// ErrNoCgroup means a cgroup operation failed where the mode required
	// it (missing delegation, controller absent, kernel too old).
	ErrNoCgroup = errors.New("proc: cgroup operation unavailable")
)

// Spec describes one process to launch. M0 keeps it minimal: fork mode,
// direct executable. Interpreter resolution lands with config (M4).
type Spec struct {
	Name    string   // app name; also used as cgroup dir name (sanitized)
	BinPath string   // absolute path to the executable
	Args    []string // argv for the executable
	// Env is the child environment. If empty, os.Environ() is used plus
	// the attribution marker. The marker is always appended (last write
	// wins) so tree members can be attributed for the orphan sweep.
	Env []string
	Cwd string
	// KillSignal is sent to the tree at Stop. 0 => SIGINT (PM2 default).
	KillSignal int // syscall.Signal value; int here to keep this file portable
	// KillTimeout bounds the graceful phase. 0 => 1600ms (PM2 default).
	KillTimeout time.Duration
	// OutFile / ErrFile are append-mode sinks for the child's stdout and
	// stderr (M2 log capture, compat.md rows 26/27). Empty => /dev/null
	// (M0 behavior). Both set to the same path realizes merge_logs (row 29).
	OutFile string
	ErrFile string
}

// ExitInfo is the terminal status of a reaped process, delivered through
// Handle.Done/ExitResult. It mirrors what waitpid reported.
type ExitInfo struct {
	Pid      int
	ExitCode int  // valid when Signaled == false
	Signaled bool // terminated by a signal
	TermSig  int  // signal number when Signaled
	// OomKill is true when the tree died by kernel OOM kill (divergence 6,
	// M5): a SIGKILL death whose app cgroup's memory.events oom_kill counter
	// advanced past the launch baseline. Only detectable in cgroup modes;
	// pgid mode leaves it false (a bare SIGKILL is ambiguous — it may be a
	// manual kill). Additive observability, never drives policy by itself.
	OomKill bool
	// Vanished marks synthesized exits for adopted trees whose leader is
	// NOT a child of this process (daemon died and restarted): the reaper
	// cannot observe such exits, a poller watches /proc instead and reports
	// the disappearance. ExitCode is 0 because the real status is
	// unobservable — a documented M2 recovery-path approximation.
	Vanished bool
}

// String renders ExitInfo for logs.
func (e ExitInfo) String() string {
	if e.Signaled {
		return "pid exited-by-signal"
	}
	return "pid exited"
}
