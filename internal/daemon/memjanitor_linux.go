//go:build linux

// Memory janitor (perf queue item 5, docs/benchmarks2.md): after a
// sustained log flood the daemon's RSS stayed inflated (15.8 -> 59.4 MB
// measured) because the runtime only returns pages to the OS during GC
// cycles — and a supervisor idles between floods, so no cycle comes and
// the freed-but-mapped heap lingers indefinitely.
//
// Design: every 30s sample the process's own RSS (/proc/self/statm — no
// stop-the-world, unlike ReadMemStats). When RSS has grown past the last
// post-reclaim baseline by reclaimThreshold, force one GC + scavenge
// (debug.FreeOSMemory) and re-baseline. Cost: at most one extra GC per
// 30s, and only when memory actually grew — an idle supervisor never
// pays it; a flood-strained one pays a cycle it was running anyway.
package daemon

import (
	"os"
	"runtime/debug"
	"time"

	"github.com/pm0/pm0/internal/proc"
)

const (
	// janitorInterval is the RSS sampling cadence.
	janitorInterval = 30 * time.Second
	// reclaimThreshold is the growth over baseline that triggers a
	// reclaim — above allocation noise, below the flood-induced growth.
	reclaimThreshold = 16 << 20
)

// memoryJanitor runs for the daemon's lifetime.
func (s *Server) memoryJanitor() {
	baseline := selfRSS()
	if baseline == 0 {
		return // cannot sample: do nothing rather than reclaim blindly
	}
	for {
		time.Sleep(janitorInterval)
		rss := selfRSS()
		if rss == 0 {
			continue
		}
		if rss > baseline+reclaimThreshold {
			debug.FreeOSMemory() // GC + return free spans to the OS
			rss = selfRSS()
		}
		baseline = minInt64(baseline, rss) // growth from a lower baseline re-triggers
	}
}

// selfRSS reads this process's resident bytes; 0 when unreadable.
func selfRSS() int64 {
	return int64(proc.ResidentBytes(os.Getpid()))
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
