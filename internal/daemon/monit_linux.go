//go:build linux

// Monit (jlist monit object, compat.md §2.1): RSS of the process TREE in
// bytes and CPU as percent of one core, both summed over the live tree.
//
//   - memory: per-pid resident set from /proc/<pid>/statm (field 2) —
//     NOT cgroup memory.current, whose page cache would inflate the number
//     and break parity with pm2 (§2.1 pin);
//   - cpu: (Δutime+stime ticks) / (Δwall) — needs two samples, so the
//     first observation of an app reports 0 and subsequent ones converge.
package daemon

import (
	"os"
	"strconv"
	"strings"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/ops"
	"github.com/pm0/pm0/internal/proc"
)

// userHZ: /proc tick values are counted in USER_HZ, a kernel ABI constant
// fixed at 100 on Linux for every architecture.
const userHZ = 100

type cpuSample struct {
	// pid is the tree leader the ticks were sampled from. Monotonic per
	// app only until a restart (or pid reuse) swaps the leader: a raw
	// ticks delta across leaders would report garbage (new tree's small
	// counter read as growth, or a reused pid's counter merged with the
	// old tree's). The delta is computed only when the leader is
	// unchanged; a leader change reports 0 for one sample and converges.
	pid   int
	ticks uint64
	at    time.Time
}

// cpuPercent derives CPU% from two samples of the SAME tree leader.
// Pure function; unit-tested in isolation.
func cpuPercent(last cpuSample, leader int, ticks uint64, now time.Time) float64 {
	if last.pid != 0 && leader != 0 && last.pid != leader {
		return 0 // leader changed (restart / pid reuse): reset window
	}
	if dt := now.Sub(last.at).Seconds(); dt > 0 && ticks >= last.ticks {
		return float64(ticks-last.ticks) / userHZ / dt * 100
	}
	return 0
}

// monitForPids computes the monit object for one app's tree using the
// shared snapshot (RSS + ticks come from one stat read per pid per pass).
// Pids missing from the pass — a child spawned after it was taken — fall
// back to direct reads so first-observation numbers stay truthful.
func (s *Server) monitForPids(pmid int, leader int, pids []int, snap *proc.Snapshot) *v1.Monit {
	var rssBytes, ticks uint64
	for _, pid := range pids {
		if info, ok := snap.Info(pid); ok {
			rssBytes += info.RSSBytes
			ticks += info.Ticks
			continue
		}
		rssBytes += proc.ResidentBytes(pid)
		ticks += procTicks(pid)
	}

	now := time.Now()
	s.mu.Lock()
	if s.cpuLast == nil {
		s.cpuLast = make(map[int]cpuSample)
	}
	cpu := cpuPercent(s.cpuLast[pmid], leader, ticks, now)
	s.cpuLast[pmid] = cpuSample{pid: leader, ticks: ticks, at: now}
	s.mu.Unlock()

	return &v1.Monit{Memory: int64(rssBytes), Cpu: cpu}
}

// residentBytes reads /proc/<pid>/statm field 2 (resident pages).
// Kernel threads and zombies report 0; vanished pids report 0.
// (Shared definition: proc.ResidentBytes — the M5 ops loop and monit must
// agree on what "tree memory" means, §2.1 and row 15.)

// procTicks sums utime+stime (/proc stat fields 14+15; post-comm indexes
// 11 and 12).
func procTicks(pid int) uint64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	i := strings.LastIndexByte(string(data), ')')
	if i < 0 {
		return 0
	}
	fields := strings.Fields(string(data)[i+2:])
	if len(fields) < 13 {
		return 0
	}
	utime, err1 := strconv.ParseUint(fields[11], 10, 64)
	stime, err2 := strconv.ParseUint(fields[12], 10, 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	return utime + stime
}

// treeRSS sums the live tree's RSS in bytes (M5 max_memory_restart
// sampling). Uses the shared snapshot path — the SAME attribution and
// per-pid memory definition the monit view reports (resident_linux.go:
// both consumers must agree on "tree memory", §2.1 and row 15).
// 0 when the app is unknown or not running — an unmeasurable tree must
// never trigger a memory restart.
func (s *Server) treeRSS(id int) int64 {
	v, ok := s.sup.Describe(strconv.Itoa(id))
	if !ok {
		return 0
	}
	snap := s.procSnapCache.get()
	pids := viewTreePids(v, snap, orphanMarkerIndex(snap))
	var sum int64
	for _, pid := range pids {
		if info, ok := snap.Info(pid); ok {
			sum += int64(info.RSSBytes)
			continue
		}
		sum += int64(proc.ResidentBytes(pid))
	}
	return sum
}

// appStatus reports the app's current status string for the ops layer.
func (s *Server) appStatus(id int) (string, bool) {
	if v, ok := s.sup.Describe(strconv.Itoa(id)); ok {
		return string(v.Runtime.Status), true
	}
	return "", false
}

// opsLoop drives the M5 enforcers: the reconciler starts/stops per-app
// runners from the registry state. The registry copy is version-gated
// (lazy ops tick — perf queue item 2): runners tick on their own
// goroutines, so a tick with an unchanged registry has nothing to align
// and costs one atomic load instead of a full List() copy — this is what
// drives idle CPU toward 0 at 500 apps. Exits with the process.
func (s *Server) opsLoop() {
	ticker := time.NewTicker(s.opsRecon.Tick)
	defer ticker.Stop()
	last := ^uint64(0) // first tick always syncs
	for range ticker.C {
		v := s.sup.Version()
		if v == last {
			continue
		}
		last = v
		views := s.sup.List()
		opsViews := make([]ops.View, 0, len(views))
		for _, v := range views {
			opsViews = append(opsViews, ops.View{ID: v.ID, Serial: v.Serial, Cfg: v.Config})
		}
		s.opsRecon.Sync(opsViews)
	}
}

// cleanupMonitCache drops samples for ids that vanished from the registry.
func (s *Server) cleanupMonitCache(live map[int]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.cpuLast {
		if !live[id] {
			delete(s.cpuLast, id)
		}
	}
}
