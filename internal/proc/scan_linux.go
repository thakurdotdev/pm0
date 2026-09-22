//go:build linux

package proc

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// procVictim is a process identified by a /proc scan, carrying the
// starttime observed at scan time. Every kill of a scanned pid re-verifies
// the starttime immediately before kill(2) (invariant I6).
type procVictim struct {
	Pid       int
	Starttime uint64
}

// scanPgrpMembers lists live processes whose pgrp == pgid. Used in pgid
// mode to find tree members after (or instead of) a blind kill(-pgid):
// each returned pid was a member at scan time. Zombies are skipped: they
// cannot run, and kill(2) against them is a no-op that would make the
// emptiness check loop forever — the reaper owns corpse collection.
//
// Cost discipline: ONE stat read per candidate pid (the old version did
// three — pgrp, state, starttime — tripling delete-all wall time at 500
// apps). killVerified still re-verifies identity at kill time (I6).
func scanPgrpMembers(pgid int) []procVictim {
	var out []procVictim
	for _, pid := range procPids() {
		si, err := readStatInfo(pid)
		if err != nil || si.State == 'Z' {
			continue // vanished mid-scan / corpse
		}
		if si.Pgrp != pgid {
			continue
		}
		out = append(out, procVictim{Pid: pid, Starttime: si.Starttime})
	}
	return out
}

// scanReparentedOrphans lists live processes that (a) were reparented to
// this process (ppid == selfPid — we are the subreaper), (b) carry our
// app attribution marker in their environment, and (c) are NOT already
// members of excludePgrp. This is how pgid mode catches trees that
// escaped the process group via setsid/double-fork.
//
// Exclusion (c) is a correctness fix, not an optimization: a launched
// tree's leader is a direct child carrying the marker AND its own group
// leader (Setpgid), so without the exclusion it matched both scans and
// every consumer summing the union double-counted the leader's RSS and
// CPU ticks (monit, treeRSS -> max_memory_restart fired at limit/2 in
// pgid mode). Kill coverage is unchanged: an excluded pid is already a
// pgrp-member victim.
func scanReparentedOrphans(selfPid int, markerValue string, excludePgrp int) []procVictim {
	var out []procVictim
	for _, pid := range procPids() {
		si, err := readStatInfo(pid)
		if err != nil || si.State == 'Z' {
			continue // dying, or not ours to read
		}
		if si.PPid != selfPid {
			continue
		}
		if excludePgrp > 0 && si.Pgrp == excludePgrp {
			continue // already covered by the pgrp scan
		}
		env, rerr := os.ReadFile(procEnvironPath(pid))
		if rerr != nil {
			continue // dying, or not ours to read
		}
		if envHasMarker(env, EnvMarkerKey, markerValue) {
			out = append(out, procVictim{Pid: pid, Starttime: si.Starttime})
		}
	}
	return out
}

// envHasMarker reports whether a raw /proc/<pid>/environ image contains
// KEY=value with the given key and value.
func envHasMarker(env []byte, key, value string) bool {
	for _, entry := range bytes.Split(env, []byte{0}) {
		k, v, ok := strings.Cut(string(entry), "=")
		if ok && k == key && v == value {
			return true
		}
	}
	return false
}

// killVerified sends sig to pid, but only if /proc/<pid>/stat still reports
// the expected starttime. Returns ErrStalePid (and sends nothing) when the
// pid was recycled or died (invariant I6).
func killVerified(pid int, expectedStarttime uint64, sig syscall.Signal) error {
	st, err := StartTime(pid)
	if err != nil {
		return fmt.Errorf("%w: pid %d stat: %v", ErrStalePid, pid, err)
	}
	if st != expectedStarttime {
		return fmt.Errorf("%w: pid %d have %d want %d", ErrStalePid, pid, st, expectedStarttime)
	}
	return syscall.Kill(pid, sig)
}

// killVictimsVerified kills every victim whose starttime still matches.
// Returns the number of kill(2) calls actually issued.
func killVictimsVerified(victims []procVictim, sig syscall.Signal) int {
	killed := 0
	for _, v := range victims {
		if err := killVerified(v.Pid, v.Starttime, sig); err == nil {
			killed++
		}
	}
	return killed
}

func procPids() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make([]int, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		out = append(out, pid)
	}
	return out
}
