//go:build linux

package proc

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Launcher spawns processes and hands out Handles. One Launcher per
// daemon (or per test). NewLauncher starts the process-wide reaper, so
// every process this binary spawns through a Launcher is reaped centrally.
type Launcher struct {
	mode   Mode
	cgRoot string
	warn   string
	self   string // this executable, used as the cgroup attach wrapper
}

// NewLauncher with ModeAuto runs the host probe; an explicit mode forces
// a kill path (used by tests and the doctor command).
func NewLauncher(mode Mode) (*Launcher, error) {
	if err := StartReaper(); err != nil {
		return nil, fmt.Errorf("subreaper: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve self for wrapper: %w", err)
	}
	l := &Launcher{self: self}
	d := Detect()
	l.cgRoot = d.CgroupRoot
	if root := os.Getenv("PM0_CGROUP_ROOT"); root != "" {
		l.cgRoot = root
	}
	if mode == ModeAuto || mode == "" {
		l.mode = d.Mode
		l.warn = d.Warning
		return l, nil
	}
	l.mode = mode
	// Forced cgroup modes surface real errors at first use (probe result
	// is advisory only — see forceCgroupKillPath fallback).
	return l, nil
}

// Mode reports the launcher's effective kill path.
func (l *Launcher) Mode() Mode { return l.mode }

// CgroupRoot reports the delegated cgroup v2 root used for app subtrees
// (adoption needs the same root the launcher created cgroups under).
func (l *Launcher) CgroupRoot() string { return l.cgRoot }

// Warning reports detection degradation notes (doctor / Ping.cgroup_warning).
func (l *Launcher) Warning() string { return l.warn }

// Launch starts spec and returns a Handle. The process tree is either
// placed in a fresh cgroup (v2 modes) or made a process-group leader
// (pgid mode) before the first app instruction runs.
func (l *Launcher) Launch(spec Spec) (*Handle, error) {
	if spec.BinPath == "" {
		return nil, fmt.Errorf("spec.BinPath required")
	}
	if spec.Name == "" {
		spec.Name = "app"
	}
	if spec.KillSignal == 0 {
		spec.KillSignal = int(syscall.SIGINT)
	}
	if spec.KillTimeout == 0 {
		spec.KillTimeout = DefaultKillTimeout
	}

	base := spec.Env
	if base == nil {
		base = os.Environ()
	}
	// Attribution marker: strip any inherited one, then append ours so the
	// orphan sweep can attribute tree members to this app.
	env := filterEnvKey(base, EnvMarkerKey)
	env = append(env, EnvMarkerKey+"="+spec.Name)

	var (
		cmd    *exec.Cmd
		cgPath string
		err    error
	)

	// IPC channel: every fork child gets a node-IPC socketpair (see
	// ipc_linux.go). Always-on, like pm2's fork mode: wait_ready (row 19)
	// needs the 'ready' frame, and arbitrary node apps may process.send
	// even without wait_ready (in pm2 they always have a channel).
	ipc, childEnd, err := newIPCChannel()
	if err != nil {
		if cgPath != "" {
			_ = removeCgroup(cgPath)
		}
		return nil, err
	}
	env = append(filterEnvKey(env, NodeChannelFdEnv),
		NodeChannelFdEnv+"="+strconv.Itoa(nodeChannelFd))

	switch l.mode {
	case ModeCgroupKill, ModeCgroupFreeze:
		cgPath, err = createCgroup(l.cgRoot, spec.Name)
		if err != nil {
			ipc.close()
			_ = childEnd.Close()
			return nil, err
		}
		// Re-exec wrapper: the child attaches ITSELF to the cgroup before
		// exec'ing the target. This closes the spawn race where the child
		// forks a grandchild before the parent could move it (the
		// grandchild would land in the daemon's cgroup, outside the app's
		// kill scope).
		argv := append([]string{spec.BinPath}, spec.Args...)
		argvJSON, _ := json.Marshal(argv)
		wenv := append(env,
			envExecPath+"="+spec.BinPath,
			envExecArgv+"="+string(argvJSON),
			envExecProcs+"="+filepath.Join(cgPath, cgroupProcsFile),
		)
		cmd = exec.Command(l.self)
		cmd.Args = []string{l.self}
		cmd.Env = wenv
		cmd.Dir = spec.Cwd

	case ModePgid:
		cmd = exec.Command(spec.BinPath, spec.Args...)
		cmd.Env = env
		cmd.Dir = spec.Cwd
		// Child becomes its own process-group leader: pgid == pid. This
		// must happen at fork (SysProcAttr), not after, or there is a
		// window where the child spawns its own group.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	default:
		ipc.close()
		_ = childEnd.Close()
		return nil, fmt.Errorf("unknown mode %q", l.mode)
	}

	// Child stdio: append-mode log files when configured (M2 capture,
	// compat.md rows 26-29), /dev/null otherwise. Files are opened O_APPEND
	// before fork so the kernel tees child output directly — no userland
	// copy goroutine, no drop window (the M3 ring buffer adds observability
	// on top of this, it does not replace the file sinks).
	if spec.OutFile != "" || spec.ErrFile != "" {
		if spec.OutFile != "" {
			f, ferr := os.OpenFile(spec.OutFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if ferr != nil {
				if cgPath != "" {
					_ = removeCgroup(cgPath)
				}
				_ = childEnd.Close()
				return nil, fmt.Errorf("open out log %s: %w", spec.OutFile, ferr)
			}
			cmd.Stdout = f
			defer f.Close() // after Start the kernel holds the dup; close our copy
		}
		if spec.ErrFile != "" {
			f, ferr := os.OpenFile(spec.ErrFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if ferr != nil {
				if cgPath != "" {
					_ = removeCgroup(cgPath)
				}
				_ = childEnd.Close()
				return nil, fmt.Errorf("open err log %s: %w", spec.ErrFile, ferr)
			}
			cmd.Stderr = f
			defer f.Close()
		}
	}

	// Child IPC fd: ExtraFiles index 0 -> fd 3 (NODE_CHANNEL_FD=3). In
	// cgroup modes the exec.Cmd is the pm0 wrapper; fd 3 and the env
	// var survive the wrapper's re-exec into the real target.
	cmd.ExtraFiles = append(cmd.ExtraFiles, childEnd)

	if err := cmd.Start(); err != nil {
		_ = childEnd.Close() // child never ran; drop our copy of the pair
		if cgPath != "" {
			_ = removeCgroup(cgPath)
		}
		ipc.close()
		return nil, fmt.Errorf("spawn %s: %w", spec.BinPath, err)
	}
	_ = childEnd.Close() // the child owns its dup at fd 3 now

	h := &Handle{
		spec:        spec,
		pid:         cmd.Process.Pid,
		mode:        l.mode,
		cgPath:      cgPath,
		killSignal:  syscall.Signal(spec.KillSignal),
		killTimeout: spec.KillTimeout,
		done:        make(chan struct{}),
		ipc:         ipc,
	}
	if h.starttime, err = StartTime(h.pid); err != nil {
		// Process died between fork and now — still hand out the handle;
		// the exit event will arrive. Guarded paths degrade to ErrStalePid.
		h.starttime = 0
	}
	if cgPath != "" {
		// OOM baseline (divergence 6): the counter is cumulative per
		// cgroup dir and dirs are reused across restarts, so the delta
		// is taken against THIS launch's baseline, read after creation.
		h.oomBase = cgroupOomKills(cgPath)
	}
	go ipc.pump()
	// Register with the reaper immediately: Register checks the cache
	// under the reaper lock, so an exit that raced us is never lost.
	exitCh := Register(h.pid)
	go func() {
		info := <-exitCh
		if info.Signaled && info.TermSig == int(unix.SIGKILL) && h.cgPath != "" {
			// SIGKILL death: distinguish kernel OOM from a manual kill
			// -9 by the cgroup's cumulative oom_kill counter. Read
			// before setExit so a racing relaunch (which recreates the
			// cgroup) can not reset the counter under us.
			if n := cgroupOomKills(h.cgPath); n > h.oomBase {
				info.OomKill = true
			}
		}
		h.setExit(info)
	}()
	return h, nil
}

// filterEnvKey returns env without any KEY= entry for key.
func filterEnvKey(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == key {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// wrapperHook runs in init() of every binary importing this package. When
// the environment carries the wrapper markers, this process IS a wrapper:
// attach to the requested cgroup, then exec the real target. Never returns.
func wrapperHook() {
	execPath := os.Getenv(envExecPath)
	if execPath == "" {
		return
	}
	if procs := os.Getenv(envExecProcs); procs != "" {
		if err := os.WriteFile(procs, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "pm0-wrapper: cgroup attach to %s failed: %v\n", procs, err)
			os.Exit(126)
		}
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(envExecArgv)), &argv); err != nil || len(argv) == 0 {
		fmt.Fprintf(os.Stderr, "pm0-wrapper: bad exec argv: %v\n", err)
		os.Exit(126)
	}
	env := os.Environ()
	for _, k := range []string{envExecPath, envExecArgv, envExecProcs} {
		env = filterEnvKey(env, k)
	}
	if err := syscall.Exec(execPath, argv, env); err != nil {
		fmt.Fprintf(os.Stderr, "pm0-wrapper: exec %s: %v\n", execPath, err)
		os.Exit(127)
	}
}

func init() { wrapperHook() }
