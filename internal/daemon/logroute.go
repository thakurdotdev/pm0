//go:build linux

// Log routing (M3): every registered app gets a logbus.Route that tails
// its on-disk sinks (rows 26/27) into a memory-bounded ring and fans new
// lines out to StreamLogs subscribers. Routes are transparent to the
// child (capture stays file-based), survive adoption, and are closed —
// not deleted from disk — when the app is deleted (pm2 keeps log files
// after `delete` too).
package daemon

import (
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/pm0/pm0/internal/logbus"
	"github.com/pm0/pm0/internal/supervisor"
)

// dropLogger rate-limits I8 drop/eviction reports to daemon.log: floods
// fire thousands of OnDrop calls per second, and the invariant only
// needs "loss is announced, counted, bounded". Design: drops accumulate
// in per-kind atomic counters (the kinds are a small closed set, so no
// map or lock belongs on the hot path — a flood evicting ~10^5 lines/s
// measured 11% daemon CPU in mutex+map churn); a 1s delayed flush prints
// the window's FULL totals exactly once — accurate counts, hard rate
// limit, and a sub-second burst is still announced (nothing silently
// swallowed).
type dropLogger struct {
	evicted    atomic.Uint64
	truncated  atomic.Uint64
	subscriber atomic.Uint64
	skipped    atomic.Uint64
	flushSet   atomic.Bool
	route      string
}

func (d *dropLogger) onDrop(kind string, st logbus.Stream, n int) {
	switch kind {
	case "evicted":
		d.evicted.Add(uint64(n))
	case "truncated":
		d.truncated.Add(uint64(n))
	case "skipped":
		d.skipped.Add(uint64(n))
	default:
		d.subscriber.Add(uint64(n))
	}
	if d.flushSet.CompareAndSwap(false, true) {
		time.AfterFunc(time.Second, d.flush)
	}
}

func (d *dropLogger) flush() {
	ev := d.evicted.Swap(0)
	tr := d.truncated.Swap(0)
	su := d.subscriber.Swap(0)
	sk := d.skipped.Swap(0)
	d.flushSet.Store(false)
	if ev+tr+su+sk == 0 {
		return // window closed with nothing to announce
	}
	summary := fmt.Sprintf("map[evicted:%d skipped:%d subscriber:%d truncated:%d]", ev, sk, su, tr)
	fmt.Printf("pm0 daemon: log route %s: bounded-buffer loss (I8): %s\n", d.route, summary)
}

// ensureRoute returns the app's route, creating it on first use (id
// start, adoption, resurrect, or a lazy StreamLogs attach for anything
// that slipped through the explicit hooks). A pm_id whose app was
// deleted gets a FRESH route here: closeRoute removed the old one.
func (s *Server) ensureRoute(v supervisor.AppView) *logbus.Route {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.routes[v.ID]; ok {
		return r
	}
	if v.Config.OutFile == "" && v.Config.ErrorFile == "" {
		return nil // no sinks configured: nothing to tail
	}
	dl := &dropLogger{route: fmt.Sprintf("%s(%d)", v.Config.Name, v.ID)}
	r := logbus.NewRoute(logbus.Options{
		OutPath: v.Config.OutFile,
		ErrPath: v.Config.ErrorFile,
		// Rotation ships ON (compat divergence 12: a flooded app
		// must not fill the disk out of the box). M4: the knobs are
		// per-app now — config.Default() pins 10 MiB / 30 retained;
		// ecosystem files, CLI flags and the dump can override (0 =
		// off). The zero value here (a config built without Default)
		// means off, matching the logbus contract.
		RotateMaxBytes: v.Config.LogRotateMaxBytes,
		RotateRetain:   v.Config.LogRotateRetain,
		OnDrop:         dl.onDrop,
	})
	s.routes[v.ID] = r
	return r
}

// closeRoute stops and forgets the app's route (delete/scale-down). The
// log files stay on disk.
func (s *Server) closeRoute(pmid int) {
	s.mu.Lock()
	r := s.routes[pmid]
	delete(s.routes, pmid)
	s.mu.Unlock()
	if r != nil {
		r.Close()
	}
}

// ensureRoutesFor covers batch start/resurrect/scale-up paths: every
// freshly registered id gets its route.
func (s *Server) ensureRoutesFor(ids []int) {
	for _, id := range ids {
		if id < 0 {
			continue // machine construction failed for this slot
		}
		if v, ok := s.sup.Describe(strconv.Itoa(id)); ok {
			s.ensureRoute(v)
		}
	}
}

// deleteAppView is sup.Delete plus route teardown — THE delete path every
// caller (delete RPC, scale-down) must use, so a reused pm_id never
// inherits a stale ring. Errors from the underlying delete win; route
// teardown runs regardless.
func (s *Server) deleteAppView(v supervisor.AppView) error {
	err := s.sup.Delete(strconv.Itoa(v.ID))
	s.closeRoute(v.ID)
	return err
}
