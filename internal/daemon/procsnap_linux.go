//go:build linux

// Shared /proc pass caching for the daemon monitoring paths (perf queue
// item 1, docs/benchmarks2.md): one directory read plus one stat read per
// pid feeds list/monit/describe for ALL apps, replacing the per-app scans
// that made `list` O(apps × processes) — 8.6s at 500 apps.
//
// Freshness contract: a snapshot older than minAge is refreshed on
// request (lazy, caller-synchronized). Back-to-back calls inside the
// window reuse the pass — this is what keeps the harness's all-online
// jlist poll flat at scale. CPU-percentage sampling degrades gracefully:
// two samples from the same pass report 0 until the next fresh pass,
// mirroring the "first observation is 0" convergence the monit section
// already documents.
package daemon

import (
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/pm0/pm0/internal/proc"
	"github.com/pm0/pm0/internal/supervisor"
)

// procSnapMinAge is the minimum age a shared pass must reach before a
// new one is taken. 1s matches the monit sampling cadence consumers
// expect (pm2 refreshes monit at 1s too); the M5 200ms value made the
// wait-online polling storm at 500 apps pay a full /proc pass per poll
// (~250ms apart), whose GC settling then leaked into the harness's idle
// window. All list/jlist consumers are sampled-data views: <=1s staleness
// on cpu/rss columns is within the sampling contract; process PIDs and
// statuses come from the registry, never from this cache, so freshness
// of STATE is unaffected.
const procSnapMinAge = time.Second

type procSnapshotCache struct {
	mu     sync.Mutex
	minAge time.Duration
	snap   *proc.Snapshot
}

func newProcSnapshotCache(minAge time.Duration) *procSnapshotCache {
	return &procSnapshotCache{minAge: minAge}
}

// get returns a pass no older than minAge, refreshing at most once per
// call. Readers share the immutable snapshot; refreshes serialize here.
func (c *procSnapshotCache) get() *proc.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snap != nil && time.Since(c.snap.At) < c.minAge {
		return c.snap
	}
	c.snap = proc.ReadSnapshot()
	return c.snap
}

// orphanMarkerIndex reads the attribution marker of every live pid
// reparented to this daemon (we are the subreaper). Usually zero pids
// qualify; the environ read is per candidate, not per process. Returns
// pid -> marker value (nil when no candidates).
func orphanMarkerIndex(snap *proc.Snapshot) map[int]string {
	cands := snap.ReparentedCandidates(os.Getpid())
	if len(cands) == 0 {
		return nil
	}
	out := make(map[int]string, len(cands))
	for _, pid := range cands {
		env, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		if err != nil {
			continue // dying, or not ours to read
		}
		if val, ok := proc.EnvMarkerValue(env); ok {
			out[pid] = val
		}
	}
	return out
}

// treeInfoForView extracts the immutable tree descriptor a view's
// snapshot carries (machine publishTreeLocked).
func treeInfoForView(v supervisor.AppView) proc.TreeInfo {
	return proc.TreeInfo{
		Mode:        proc.Mode(v.Runtime.TreeMode),
		CgroupPath:  v.Runtime.CgroupPath,
		LeaderPid:   v.Runtime.Pid,
		LeaderStart: v.Runtime.LeaderStart,
	}
}

// viewTreePids attributes one app's tree pids from the shared pass.
func viewTreePids(v supervisor.AppView, snap *proc.Snapshot, orphans map[int]string) []int {
	if v.Runtime.Pid <= 0 && v.Runtime.TreeMode == "" {
		return nil // not running
	}
	return snap.AttributeTree(treeInfoForView(v), v.Marker, orphans)
}
