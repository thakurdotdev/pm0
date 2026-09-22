//go:build linux

// Tree memory sampling shared by the daemon monit view (jlist monit.memory,
// compat.md §2.1) and the M5 ops loop (max_memory_restart enforcement,
// row 15). Both must see the SAME number: per-pid anon RSS from
// /proc/<pid>/statm, never cgroup memory.current (page cache inflates it
// and breaks parity with pm2).
package proc

import (
	"os"
	"strconv"
	"strings"
)

// ResidentBytes reads /proc/<pid>/statm field 2 (resident pages) and
// returns the RSS in bytes. Kernel threads and zombies report 0; vanished
// pids report 0. Exported so the daemon (monit) and the ops loop
// (max_memory_restart) share one definition of process-tree memory.
func ResidentBytes(pid int) uint64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}
