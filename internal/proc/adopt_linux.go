//go:build linux

// Adoption (M2): rebuilding Handles around process trees that already run,
// so a daemon can re-exec itself (syscall.Exec keeps the children attached
// to the same PID) or recover after a crash without killing or duplicating
// its apps.
//
// Attribution is the same marker the pgid orphan sweep uses: every tree
// member carries _PM0_APP=<attrib> in its environment (launcher appends
// it, children inherit it). The adopted leader is the OLDEST matching
// process — the launcher spawned it before any child could exist, so the
// minimum starttime identifies it robustly even when mid-tree members were
// reparented to the daemon.
//
// Exit observation for an adopted leader depends on parenthood:
//
//   - leader is still our child (update/re-exec flow): the reaper owns it,
//     plain Register works;
//   - leader was orphaned to init (daemon died and a new daemon adopted
//     the tree): wait4 will never report it, so watchNonChild polls /proc
//     and synthesizes a Vanished ExitInfo when the starttime disappears.
package proc

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"time"
)

// AdoptTree rebuilds a Handle around the running tree attributed to
// attrib (the launcher spec.Name: sanitized app name + pm id). mode and
// cgRoot come from the adopting process's own launcher; killSignal and
// killTimeout from the persisted app config. Returns an error when no
// matching live tree exists (caller decides whether to start fresh).
func AdoptTree(attrib string, mode Mode, cgRoot string, killSignal int, killTimeout time.Duration) (*Handle, error) {
	leader, members := findTree(attrib)
	if leader == 0 {
		return nil, fmt.Errorf("adopt: no live tree carries marker %q", attrib)
	}

	h := &Handle{
		spec: Spec{
			Name:        attrib,
			KillSignal:  killSignal,
			KillTimeout: killTimeout,
		},
		pid:         leader,
		mode:        mode,
		killSignal:  syscall.Signal(killSignal),
		killTimeout: killTimeout,
		done:        make(chan struct{}),
	}
	if h.killTimeout <= 0 {
		h.killTimeout = DefaultKillTimeout
	}
	if h.killSignal == 0 {
		h.killSignal = syscall.SIGINT
	}

	switch mode {
	case ModeCgroupKill, ModeCgroupFreeze:
		h.cgPath = cgroupDirFor(cgRoot, attrib)
		if _, err := os.Stat(h.cgPath + "/" + cgroupProcsFile); err != nil {
			// The cgroup dir is gone while marker-carrying processes are
			// alive. A cgroup-mode tree is NOT a process-group leader
			// (spawns into the daemon's group), so pgid kill semantics
			// would be wrong — refuse rather than mismanage.
			return nil, fmt.Errorf("adopt: cgroup dir %s missing for live tree %q: %w",
				h.cgPath, attrib, ErrNoCgroup)
		}
	case ModePgid:
		// Leader must still own its process group (the launcher created it
		// as group leader). A leader that lost the group is adopted anyway —
		// the kill paths re-verify identity before every signal (I6).
	default:
		return nil, fmt.Errorf("adopt: unknown mode %q", mode)
	}

	var err error
	if h.starttime, err = StartTime(h.pid); err != nil {
		return nil, fmt.Errorf("adopt: starttime of pid %d: %w", h.pid, err)
	}

	// Exit delivery: reaper when the leader is our child, poller otherwise.
	if ppid, _, perr := statPPidGrp(h.pid); perr == nil && ppid == os.Getpid() {
		exitCh := Register(h.pid)
		go func() {
			info := <-exitCh
			h.setExit(info)
		}()
	} else {
		go watchNonChild(h)
	}

	_ = members // members are an audit convenience; the kill paths re-scan live
	return h, nil
}

// findTree scans /proc for the attributed tree. Returns the leader (oldest
// matching starttime) and all matching live pids. Zombies are skipped —
// a dead leader must not shadow a live tree (nor vice versa).
func findTree(attrib string) (leader int, members []int) {
	var bestStart uint64
	self := os.Getpid()
	for _, pid := range procPids() {
		if pid == self {
			continue
		}
		if st, err := procState(pid); err != nil || st == 'Z' {
			continue
		}
		env, err := os.ReadFile(procEnvironPath(pid))
		if err != nil || !envHasMarker(env, EnvMarkerKey, attrib) {
			continue
		}
		st, err := StartTime(pid)
		if err != nil {
			continue
		}
		members = append(members, pid)
		if leader == 0 || st < bestStart {
			leader = pid
			bestStart = st
		}
	}
	return leader, members
}

// watchNonChild synthesizes exit delivery for an adopted leader this
// process cannot wait4. Polls the identity (starttime) once a second;
// when it stops matching, the process exited (or its pid was reused —
// both mean our tree's leader is gone). The synthesized exit is marked
// Vanished; the machine's restart policy treats it like any other exit.
func watchNonChild(h *Handle) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		st, err := StartTime(h.pid)
		if err == nil && st == h.starttime {
			continue
		}
		h.setExit(ExitInfo{Pid: h.pid, Vanished: true})
		return
	}
}

// cgroupDirFor mirrors createCgroup's naming (root/<sanitized attrib>) for
// adoption lookups.
func cgroupDirFor(root, attrib string) string {
	return root + "/" + SanitizeName(attrib)
}

// procEnvironPath is split out for tests.
func procEnvironPath(pid int) string { return "/proc/" + strconv.Itoa(pid) + "/environ" }

// AdoptedTreePids reports every live pid carrying the attribution marker —
// a read-only diagnostic used by the daemon after adoption to sanity-check
// that the registry matches reality.
func AdoptedTreePids(attrib string) []int {
	_, members := findTree(attrib)
	return members
}
