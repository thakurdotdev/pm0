//go:build linux

// Single-pass /proc snapshot (perf queue item 1, docs/benchmarks2.md).
// The monitoring paths used to pay O(apps × processes): every list/monit
// call re-scanned /proc once per app (pgrp members + orphan attribution)
// and then re-read /proc/<pid>/statm and stat per tree pid. One shared
// pass per refresh supplies every pid's state, ppid, pgrp, starttime,
// RSS and CPU ticks from a single stat read each, so the daemon
// attributes all trees in O(processes) total and `list` stays flat at
// any scale.
//
// RSS source note: stat field 24 (rss, pages) is the same mm counter
// statm field 2 reports — the §2.1 monit pin stays intact (per-pid
// resident pages, never cgroup memory.current).
package proc

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// statInfo carries the /proc/<pid>/stat fields the supervisor reads.
// Indexes are post-comm (comm may contain spaces/parens — split after
// the LAST ')').
type statInfo struct {
	State     byte   // field 3: R S D Z T ...
	PPid      int    // field 4
	Pgrp      int    // field 5
	Utime     uint64 // field 14 (clock ticks)
	Stime     uint64 // field 15
	Starttime uint64 // field 22 (the I6 identity anchor)
	RSS       uint64 // field 24 (pages)
}

// parseStat extracts the fields above from one raw stat image.
func parseStat(data []byte) (statInfo, error) {
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 > len(data) {
		return statInfo{}, fmt.Errorf("malformed /proc stat: %q", truncate(data))
	}
	f := bytes.Fields(data[i+2:])
	// f[0]=state(3) f[1]=ppid(4) f[2]=pgrp(5) f[11]=utime(14)
	// f[12]=stime(15) f[19]=starttime(22) f[21]=rss(24)
	if len(f) < 22 {
		return statInfo{}, fmt.Errorf("short /proc stat: %d post-comm fields", len(f))
	}
	var si statInfo
	si.State = f[0][0]
	var err error
	if si.PPid, err = strconv.Atoi(string(f[1])); err != nil {
		return statInfo{}, err
	}
	if si.Pgrp, err = strconv.Atoi(string(f[2])); err != nil {
		return statInfo{}, err
	}
	if si.Utime, err = strconv.ParseUint(string(f[11]), 10, 64); err != nil {
		return statInfo{}, err
	}
	if si.Stime, err = strconv.ParseUint(string(f[12]), 10, 64); err != nil {
		return statInfo{}, err
	}
	if si.Starttime, err = strconv.ParseUint(string(f[19]), 10, 64); err != nil {
		return statInfo{}, err
	}
	if si.RSS, err = strconv.ParseUint(string(f[21]), 10, 64); err != nil {
		return statInfo{}, err
	}
	return si, nil
}

// readStatInfo reads and parses /proc/<pid>/stat in one shot.
func readStatInfo(pid int) (statInfo, error) {
	data, err := os.ReadFile(procStatPath(pid))
	if err != nil {
		return statInfo{}, err
	}
	return parseStat(data)
}

// ProcInfo is one process's sample from a Snapshot.
type ProcInfo struct {
	State     byte
	PPid      int
	Pgrp      int
	Starttime uint64
	RSSBytes  uint64
	Ticks     uint64 // utime + stime
}

// Snapshot is one /proc pass: a pid-indexed view of every process that
// was alive (and parseable) at At. Immutable after construction —
// readers may share it freely; staleness is managed by the caller
// (the daemon refreshes against a minimum age).
type Snapshot struct {
	At    time.Time
	ByPid map[int]ProcInfo
}

// ReadSnapshot walks /proc once, reading each pid's stat exactly once.
// Pids that vanish mid-scan (or die while being read) are simply absent.
func ReadSnapshot() *Snapshot {
	snap := &Snapshot{At: time.Now(), ByPid: make(map[int]ProcInfo, 128)}
	page := uint64(os.Getpagesize())
	for _, pid := range procPids() {
		si, err := readStatInfo(pid)
		if err != nil {
			continue // vanished mid-scan, kernel thread without stats, etc.
		}
		snap.ByPid[pid] = ProcInfo{
			State:     si.State,
			PPid:      si.PPid,
			Pgrp:      si.Pgrp,
			Starttime: si.Starttime,
			RSSBytes:  si.RSS * page,
			Ticks:     si.Utime + si.Stime,
		}
	}
	return snap
}

// Info returns the sample for pid (ok=false when absent from the pass).
func (s *Snapshot) Info(pid int) (ProcInfo, bool) {
	info, ok := s.ByPid[pid]
	return info, ok
}

// PgrpMembers lists live (non-zombie) pids whose process group is pgid,
// sorted for deterministic output. This mirrors scanPgrpMembers' verdict
// for the monitoring path (kill paths keep their own fresh scans).
func (s *Snapshot) PgrpMembers(pgid int) []int {
	out := make([]int, 0, 4)
	for pid, info := range s.ByPid {
		if info.Pgrp == pgid && info.State != 'Z' {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}

// ReparentedCandidates lists live pids reparented to selfPid, sorted.
// The caller decides which carry an attribution marker (environ reads
// stay per-candidate: usually zero pids qualify).
func (s *Snapshot) ReparentedCandidates(selfPid int) []int {
	out := make([]int, 0, 2)
	for pid, info := range s.ByPid {
		if info.PPid == selfPid && info.State != 'Z' {
			out = append(out, pid)
		}
	}
	sort.Ints(out)
	return out
}

// EnvMarkerValue extracts the attribution marker value from a raw
// /proc/<pid>/environ image (the value of EnvMarkerKey). ok=false when
// the key is absent.
func EnvMarkerValue(env []byte) (string, bool) {
	for _, entry := range bytes.Split(env, []byte{0}) {
		k, v, ok := strings.Cut(string(entry), "=")
		if ok && k == EnvMarkerKey {
			return v, true
		}
	}
	return "", false
}

// AttributeTree lists the pids of one app's tree from this pass — the
// monitoring-path twin of Handle.TreePids (kill paths keep their own
// fresh scans; the numbers here only feed monit/jlist/ops):
//
//   - cgroup modes: cgroup.procs is authoritative and cannot be derived
//     from a /proc stat pass — read it fresh (one file read per app);
//   - pgid mode: pgrp members from the pass, plus reparented pids whose
//     marker value matches (the caller supplies the pid->marker index for
//     this pass's reparented candidates). A candidate already inside the
//     leader's group is skipped — the pgrp scan covers it, and without
//     the exclusion the leader (a direct child carrying the marker and
//     its own group leader) would be double-counted.
//
// The leader's starttime (ti.LeaderStart) is intentionally NOT used to
// gate pgid attribution: scanPgrpMembers — the kill-path twin — also
// attributes by group regardless of leader liveness, and consumers sum
// RSS/ticks over the result. Returning an empty set for a dead leader
// would make monit report zeros while the tree's last members wind down.
func (s *Snapshot) AttributeTree(ti TreeInfo, marker string, orphanMarkers map[int]string) []int {
	switch ti.Mode {
	case ModeCgroupKill, ModeCgroupFreeze:
		procs, err := CgroupProcs(ti.CgroupPath)
		if err != nil {
			return nil // cgroup gone (tree gone) or unreadable: nothing to sum
		}
		return procs
	case ModePgid:
		pids := s.PgrpMembers(ti.LeaderPid)
		for pid, m := range orphanMarkers {
			if m != marker {
				continue
			}
			if info, ok := s.ByPid[pid]; ok && info.Pgrp == ti.LeaderPid {
				continue // pgrp scan covers it (leader double-count guard)
			}
			pids = append(pids, pid)
		}
		sort.Ints(pids)
		return pids
	default:
		return nil
	}
}
