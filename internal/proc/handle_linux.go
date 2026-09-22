//go:build linux

package proc

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Handle owns one launched process tree. Contract: exactly one goroutine
// (the future M1 actor mailbox) drives Signal/Stop on a handle; reading
// Done/ExitResult is safe from anywhere. Handle is not safe for
// concurrent Stop calls — the mailbox serializes in M1.
type Handle struct {
	spec        Spec
	pid         int
	mode        Mode
	cgPath      string
	starttime   uint64 // leader starttime at spawn (I6 anchor)
	killSignal  syscall.Signal
	killTimeout time.Duration
	ipc         *ipcChannel // node-IPC parent side (always-on, M5)
	oomBase     uint64      // memory.events oom_kill at launch (divergence 6)

	mu   sync.Mutex
	exit ExitInfo
	done chan struct{} // closed once, when the leader's exit is delivered
}

func (h *Handle) PID() int              { return h.pid }
func (h *Handle) Mode() Mode            { return h.mode }
func (h *Handle) CgroupPath() string    { return h.cgPath }
func (h *Handle) Done() <-chan struct{} { return h.done }

// TreeInfo is the immutable tree descriptor the daemon's monitoring path
// needs to attribute this app's pids from a shared /proc snapshot: kill
// mode, cgroup dir (cgroup modes), leader pid and its spawn-time starttime
// (the pgid attribution key; pid reuse is detected by comparing the
// snapshot's starttime against it). All fields are fixed at launch/adopt —
// no locking needed.
type TreeInfo struct {
	Mode        Mode
	CgroupPath  string
	LeaderPid   int
	LeaderStart uint64
}

func (h *Handle) TreeInfo() TreeInfo {
	return TreeInfo{Mode: h.mode, CgroupPath: h.cgPath, LeaderPid: h.pid, LeaderStart: h.starttime}
}

// Ready returns a channel that closes when the child sends the node-IPC
// string "ready" — the wait_ready gate (row 19). nil for adopted handles
// (their channel died with the previous daemon image: adoption marks the
// app online immediately, so no gate is needed).
func (h *Handle) Ready() <-chan struct{} {
	if h.ipc == nil {
		return nil
	}
	return h.ipc.ready
}

// SendShutdown delivers the string "shutdown" over the node-IPC channel —
// PM2's cluster-reload handoff to the old worker (God/Reload.js: the app
// is expected to close its server and exit on it; apps that never handle
// it fall through to the normal kill path). Best effort: a child without
// a live channel (or one that already died) just skips the graceful phase.
func (h *Handle) SendShutdown() error {
	if h.ipc == nil {
		return fmt.Errorf("ipc: no channel")
	}
	return h.ipc.sendMsg(`"shutdown"`)
}

// ExitResult is valid only after Done closes.
func (h *Handle) ExitResult() ExitInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exit
}

func (h *Handle) setExit(info ExitInfo) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.exit = info
	close(h.done)
}

// KillSignalName returns the configured graceful signal (PM2 kill_signal).
func (h *Handle) KillSignalName() string {
	return syscall.Signal(h.spec.KillSignal).String()
}

// Signal delivers sig to the whole tree, best effort:
//
//   - cgroup modes: every pid currently in cgroup.procs (pids read live
//     from the kernel — no reuse risk, I6 trivially holds).
//   - pgid mode: kill(-pgid) after verifying the group leader's starttime
//     (ErrStalePid otherwise — I6). A dead leader means no group signal,
//     ever: the pgid number may already be reused.
func (h *Handle) Signal(sig syscall.Signal) error {
	switch h.mode {
	case ModeCgroupKill, ModeCgroupFreeze:
		procs, err := cgroupProcs(h.cgPath)
		if err != nil {
			if os.IsNotExist(err) {
				return nil // cgroup gone: tree gone
			}
			return err
		}
		var firstErr error
		for _, pid := range procs {
			if err := unix.Kill(pid, sig); err != nil && firstErr == nil {
				firstErr = err // ESRCH is normal for exiting members
			}
		}
		return firstErr
	case ModePgid:
		st, err := StartTime(h.pid)
		if err != nil || st != h.starttime {
			return fmt.Errorf("%w: leader pid %d (stat err %v)", ErrStalePid, h.pid, err)
		}
		return unix.Kill(-h.pid, sig)
	default:
		return fmt.Errorf("unknown mode %q", h.mode)
	}
}

// TreePids lists pids considered part of this app's tree right now. This
// is what the invariant checker (I1: "tree empty after stop") inspects.
func (h *Handle) TreePids() []int {
	switch h.mode {
	case ModeCgroupKill, ModeCgroupFreeze:
		procs, err := cgroupProcs(h.cgPath)
		if err != nil {
			return nil
		}
		return procs
	case ModePgid:
		var out []int
		for _, v := range scanPgrpMembers(h.pid) {
			out = append(out, v.Pid)
		}
		self := os.Getpid()
		for _, v := range scanReparentedOrphans(self, h.spec.Name, h.pid) {
			out = append(out, v.Pid)
		}
		return out
	default:
		return nil
	}
}

// Stop runs the full kill path (PM2 stop semantics):
//
//	kill_signal to tree -> wait kill_timeout -> force path -> verify
//	empty within GracePeriod (I1) -> cleanup cgroup dir.
//
// A leader that exited early does not shortcut the force path: its
// children may still be alive (orphans), and they must die too (I1).
func (h *Handle) Stop() error {
	deadline := time.Now().Add(h.killTimeout)

	// Graceful phase (best effort: stale tree members are fine here).
	_ = h.Signal(h.killSignal)

	select {
	case <-h.done:
	case <-time.After(time.Until(deadline)):
	}

	// Force phase.
	var err error
	switch h.mode {
	case ModeCgroupKill:
		err = forceCgroupKillPath(h.cgPath, GracePeriod)
	case ModeCgroupFreeze:
		err = forceFreezeDrainPath(h.cgPath, GracePeriod)
	case ModePgid:
		err = h.forcePgid()
	default:
		err = fmt.Errorf("unknown mode %q", h.mode)
	}

	// Leader exit delivery: force paths killed it, give the reaper a beat.
	select {
	case <-h.done:
	case <-time.After(GracePeriod):
	}

	// Release the IPC reader: the tree is gone (or about to be), nothing
	// else will drain the channel. Pump's blocked read returns via
	// shutdown+close; its goroutine exits with the done close.
	if h.ipc != nil {
		h.ipc.close()
	}

	if h.cgPath != "" {
		if rmErr := removeCgroup(h.cgPath); rmErr != nil && err == nil {
			err = fmt.Errorf("cgroup cleanup: %w", rmErr)
		}
	}
	return err
}

// forcePgid is the pgid-mode force phase: SIGKILL everything attributable
// to the tree, re-verifying identity before every kill (I6), then verify
// emptiness within grace (I1).
//
// Three sources, because pgid mode has real escape hatches:
//
//  1. kill(-pgid) — only while the leader is alive with a matching
//     starttime (group number reuse makes it poison after death).
//  2. pgrp == pgid scan — members that outlived the leader; each pid is
//     killed individually with a fresh starttime check.
//  3. reparented-orphan scan (setsid/double-fork escape) — children
//     reparented to this daemon (subreaper) carrying our attribution
//     marker, killed individually with fresh starttime checks.
func (h *Handle) forcePgid() error {
	deadline := time.Now().Add(GracePeriod)
	for {
		// (1) group kill while the leader identity still holds.
		if st, err := StartTime(h.pid); err == nil && st == h.starttime {
			_ = unix.Kill(-h.pid, unix.SIGKILL)
		}
		// (2) remaining group members (zombies excluded — the reaper owns
		// those and a zombie cannot run).
		victims := scanPgrpMembers(h.pid)
		// (3) setsid escapees reparented to us.
		victims = append(victims, scanReparentedOrphans(os.Getpid(), h.spec.Name, h.pid)...)
		killVictimsVerified(victims, unix.SIGKILL)

		if h.treeEmpty() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pgid tree of app %q has survivors after grace: %v",
				h.spec.Name, h.TreePids())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// treeEmpty is the pgid-mode emptiness verdict (I1): no live leader, no
// group members, no attributed orphans. Zombies are the reaper's business.
func (h *Handle) treeEmpty() bool {
	if st, err := procState(h.pid); err == nil && st != 'Z' {
		return false
	}
	if len(scanPgrpMembers(h.pid)) > 0 {
		return false
	}
	if len(scanReparentedOrphans(os.Getpid(), h.spec.Name, h.pid)) > 0 {
		return false
	}
	return true
}
