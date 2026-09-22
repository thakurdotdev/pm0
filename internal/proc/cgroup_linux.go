//go:build linux

package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// cgroupKillFile is kernel >= 5.14: writing "1" kills every task in the
// subtree atomically (no signal-by-signal race, no escape between procs
// read and kill).
const cgroupKillFile = "cgroup.kill"
const cgroupFreezeFile = "cgroup.freeze"
const cgroupProcsFile = "cgroup.procs"

// createCgroup makes a fresh per-app cgroup under root and verifies it is
// a real v2 cgroup (cgroup.procs must exist). Caller is responsible for
// RemoveCgroup cleanup.
func createCgroup(root, name string) (string, error) {
	dir := filepath.Join(root, SanitizeName(name))
	if err := os.Mkdir(dir, 0o755); err != nil {
		if os.IsExist(err) {
			// Stale from a previous crashed daemon: safe to reuse only if
			// empty. Try to remove; if it still has procs, refuse.
			if procs, perr := cgroupProcs(dir); perr == nil && len(procs) == 0 {
				if rm := os.Remove(dir); rm != nil {
					return "", fmt.Errorf("%w: stale cgroup %s not removable: %v", ErrNoCgroup, dir, rm)
				}
				if err = os.Mkdir(dir, 0o755); err != nil {
					return "", fmt.Errorf("%w: recreate %s: %v", ErrNoCgroup, dir, err)
				}
			} else {
				return "", fmt.Errorf("%w: cgroup %s already exists with live procs", ErrNoCgroup, dir)
			}
		} else {
			return "", fmt.Errorf("%w: mkdir %s: %v", ErrNoCgroup, dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, cgroupProcsFile)); err != nil {
		return "", fmt.Errorf("%w: %s is not a cgroup v2 dir: %v", ErrNoCgroup, dir, err)
	}
	return dir, nil
}

// cgroupProcs reads the pids currently attached to a cgroup.
func cgroupProcs(dir string) ([]int, error) {
	data, err := os.ReadFile(filepath.Join(dir, cgroupProcsFile))
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// CgroupProcs is the exported form for the daemon's monitoring path: the
// pids currently attached to a cgroup dir (cgroup.procs is authoritative
// for tree membership and cannot be derived from a /proc stat pass).
func CgroupProcs(dir string) ([]int, error) { return cgroupProcs(dir) }

// cgroupKill writes 1 to cgroup.kill (atomic subtree SIGKILL, kernel
// >= 5.14). Returns an error wrapping ENOENT as ErrNoCgroup when the
// kernel predates the file so the caller can fall back to freeze-drain.
func cgroupKill(dir string) error {
	if err := os.WriteFile(filepath.Join(dir, cgroupKillFile), []byte("1"), 0o644); err != nil {
		return fmt.Errorf("cgroup.kill: %w", err)
	}
	return nil
}

// setCgroupFrozen freezes (true) or thaws (false) the cgroup. Frozen
// tasks sit in the refrigerator: they cannot fork further, which is what
// makes the drain loop below airtight.
func setCgroupFrozen(dir string, frozen bool) error {
	v := "0"
	if frozen {
		v = "1"
	}
	if err := os.WriteFile(filepath.Join(dir, cgroupFreezeFile), []byte(v), 0o644); err != nil {
		return fmt.Errorf("cgroup.freeze: %w", err)
	}
	return nil
}

// forceCgroupKillPath is the force phase for kernel >= 5.14: cgroup.kill,
// then verify the cgroup drains to empty within grace.
func forceCgroupKillPath(dir string, grace time.Duration) error {
	if err := cgroupKill(dir); err != nil {
		// Kernel older than detection believed (or file hidden): fall back.
		return forceFreezeDrainPath(dir, grace)
	}
	return waitCgroupEmpty(dir, grace)
}

// forceFreezeDrainPath is the force phase for kernels 5.2..5.13:
// freeze -> SIGKILL everything visible -> thaw -> poll for empty.
//
// Why freeze first: a live tree can keep spawning children during the
// kill window (fake-child --fork-during-kill exists precisely for this).
// Freezing stops the spawner; any task that slipped through mid-clone is
// frozen too and appears in the procs read below.
//
// Ordering matters: SIGKILLed tasks that were frozen stay listed in
// cgroup.procs until they are thawed and actually die, so the emptiness
// check must happen AFTER thaw, not before. SIGKILL to a frozen task is
// delivered on thaw (the v2 freezer wakes dying tasks).
func forceFreezeDrainPath(dir string, grace time.Duration) error {
	if err := setCgroupFrozen(dir, true); err != nil {
		return fmt.Errorf("%w: %v", ErrNoCgroup, err)
	}

	sawAny := false
	procs, err := cgroupProcs(dir)
	if err != nil {
		if os.IsNotExist(err) {
			_ = setCgroupFrozen(dir, false)
			return nil // cgroup already gone
		}
		_ = setCgroupFrozen(dir, false)
		return fmt.Errorf("drain read: %w", err)
	}
	if len(procs) > 0 {
		sawAny = true
		for _, pid := range procs {
			// pids come from cgroup.procs microseconds ago; they are live
			// members of this cgroup, so plain kill is safe here. The
			// starttime guard (I6) is for pids stored across lifetimes,
			// which never happens on the cgroup path.
			_ = unix.Kill(pid, unix.SIGKILL)
		}
	}
	// Thaw unconditionally: a stuck frozen cgroup is worse than a leak.
	if err := setCgroupFrozen(dir, false); err != nil {
		return fmt.Errorf("thaw: %w", err)
	}
	if !sawAny {
		return nil
	}
	return waitCgroupEmpty(dir, grace)
}

// waitCgroupEmpty polls until cgroup.procs is empty or the grace expires
// (invariant I1 verification).
func waitCgroupEmpty(dir string, grace time.Duration) error {
	deadline := time.Now().Add(grace)
	for {
		procs, err := cgroupProcs(dir)
		if err != nil {
			// Cgroup removed (already cleaned) counts as empty.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if len(procs) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("cgroup %s still has %d procs after grace", dir, len(procs))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// removeCgroup removes an empty per-app cgroup. Retries briefly to dodge
// the kernel's asynchronous removal of a just-emptied cgroup.
func removeCgroup(dir string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Remove(dir)
		if err == nil || os.IsNotExist(err) {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// cgroupOomKills reads the cumulative oom_kill counter from
// <dir>/memory.events (kernel 4.19+ for the counter; the file itself is
// 5.x-era for some fields but oom_kill has been there since 4.19).
// 0 when the file is absent (cgroup gone, kernel too old): an
// unanswerable probe must never fabricate an OOM verdict.
func cgroupOomKills(dir string) uint64 {
	data, err := os.ReadFile(filepath.Join(dir, "memory.events"))
	if err != nil {
		return 0
	}
	return parseMemoryEvents(string(data))
}

// parseMemoryEvents extracts the oom_kill value from memory.events content
// ("key value" lines). Pure function; unit-tested.
func parseMemoryEvents(data string) uint64 {
	for _, line := range strings.Split(data, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || k != "oom_kill" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}
