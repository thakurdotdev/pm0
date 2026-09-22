//go:build linux

package proc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// StartTime returns /proc/<pid>/stat field 22 for pid. The starttime is the
// cornerstone of invariant I6: two processes can share a pid over time, but
// never the same (pid, starttime) pair.
//
// Parsing note: field 2 (comm) may contain spaces and parentheses, so we
// split after the LAST ')' — everything after it starts at field 3 (state).
// starttime is field 22, i.e. index 19 of the post-comm fields.
func StartTime(pid int) (uint64, error) {
	data, err := os.ReadFile(procStatPath(pid))
	if err != nil {
		return 0, err
	}
	return parseStarttime(data)
}

func parseStarttime(data []byte) (uint64, error) {
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 > len(data) {
		return 0, fmt.Errorf("malformed /proc stat: %q", truncate(data))
	}
	fields := bytes.Fields(data[i+2:])
	// fields[0] is field 3 (state); starttime is field 22 -> index 19.
	if len(fields) < 20 {
		return 0, fmt.Errorf("short /proc stat: %d post-comm fields", len(fields))
	}
	v, err := strconv.ParseUint(string(fields[19]), 10, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

// statPPidGrp returns (ppid, pgrp) — /proc stat fields 4 and 5 — used by
// the orphan sweep and pgid-member scan.
func statPPidGrp(pid int) (ppid, pgrp int, err error) {
	data, err := os.ReadFile(procStatPath(pid))
	if err != nil {
		return 0, 0, err
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 > len(data) {
		return 0, 0, errors.New("malformed /proc stat")
	}
	fields := bytes.Fields(data[i+2:])
	if len(fields) < 3 {
		return 0, 0, errors.New("short /proc stat")
	}
	// fields[0]=state(3), fields[1]=ppid(4), fields[2]=pgrp(5)
	ppid, err = strconv.Atoi(string(fields[1]))
	if err != nil {
		return 0, 0, err
	}
	pgrp, err = strconv.Atoi(string(fields[2]))
	if err != nil {
		return 0, 0, err
	}
	return ppid, pgrp, nil
}

// procState returns the task state letter from /proc/<pid>/stat field 3:
// 'R' running, 'S' sleeping, 'Z' zombie, 'D' disk sleep, ...
func procState(pid int) (byte, error) {
	data, err := os.ReadFile(procStatPath(pid))
	if err != nil {
		return 0, err
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 || i+2 > len(data) {
		return 0, errors.New("malformed /proc stat")
	}
	fields := bytes.Fields(data[i+2:])
	if len(fields) == 0 {
		return 0, errors.New("short /proc stat")
	}
	return fields[0][0], nil
}

func procStatPath(pid int) string {
	return fmt.Sprintf("/proc/%d/stat", pid)
}

// ProcStartEpochMs returns the process start time as epoch milliseconds:
// the kernel's boot epoch (btime from /proc/stat) plus field-22 starttime
// converted from clock ticks. This restores pm_uptime truthfully for
// adopted trees (compat.md §2.2: pm_uptime = epoch ms of last start).
func ProcStartEpochMs(pid int) (int64, error) {
	st, err := StartTime(pid)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, err
	}
	var btime int64
	for _, line := range bytes.Split(data, []byte("\n")) {
		if b := bytes.TrimSpace(line); bytes.HasPrefix(b, []byte("btime ")) {
			btime, err = strconv.ParseInt(string(bytes.TrimSpace(b[len("btime "):])), 10, 64)
			if err != nil {
				return 0, fmt.Errorf("bad btime: %w", err)
			}
			break
		}
	}
	if btime == 0 {
		return 0, errors.New("btime missing from /proc/stat")
	}
	// /proc stat tick values are counted in USER_HZ, a kernel ABI constant
	// fixed at 100 on Linux for every architecture and configuration —
	// sysconf(_SC_CLK_TCK) would return the same 100.
	const userHZ = 100
	return btime*1000 + int64(st)*1000/userHZ, nil
}

func truncate(b []byte) string {
	const n = 64
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
