//go:build linux

package proc

import (
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// ExitChan is the delivery channel type for reaped exits.
type ExitChan chan ExitInfo

// reaper owns every waitpid in the process. Per the plan: "single
// waitpid(-1) loop owns all exit events; os/exec.Wait is never called"
// (avoids ECHILD races between competing waiters).
//
// The spawn→register race (child exits between fork+exec and Register) is
// closed by a bounded cache: the reaper caches exits for pids nobody is
// waiting on; Register checks the cache under the same lock the reaper
// uses, so an exit can never fall through the gap.
type reaper struct {
	mu      sync.Mutex
	waiting map[int]ExitChan
	cache   map[int]ExitInfo // pids reaped with no registered waiter
	order   []int            // eviction order for cache (FIFO)
	started bool
	stop    chan struct{}
	kick    chan struct{} // Register nudges the idle loop (spawn latency)
	// idleTimer backs the ECHILD park (one allocation for the daemon's
	// lifetime — the old per-wake time.After allocated a fresh timer
	// 40x/s, measurable as GC churn even at zero apps).
	idleTimer *time.Timer
	// idlePoll is the CURRENT zero-children park duration. It backs off
	// 25ms -> 800ms while nothing reappable appears (a fully idle daemon
	// wakes ~1.2x/s instead of 40x/s) and resets to 25ms whenever a
	// Register kick arrives or a reap succeeds. The kick path keeps
	// spawn-to-reap latency at one select wake: a child that dies before
	// its Register is reaped on the kick, never on the poll.
	idlePoll time.Duration
}

const reaperCacheCap = 1024

var globalReaper = &reaper{
	waiting:  make(map[int]ExitChan),
	cache:    make(map[int]ExitInfo),
	stop:     make(chan struct{}),
	kick:     make(chan struct{}, 1),
	idlePoll: reaperIdleBase,
}

const (
	reaperIdleBase = 25 * time.Millisecond
	reaperIdleMax  = 800 * time.Millisecond
)

// StartReaper enables PR_SET_CHILD_SUBREAPER on this process and starts
// the singleton waitpid(-1) loop. Idempotent. Subreaper must be active
// before any child spawns so reparented grandchildren land here (invariant
// I2 groundwork: orphans are adoptable/reapable, never lost).
func StartReaper() error {
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		return err
	}
	globalReaper.mu.Lock()
	defer globalReaper.mu.Unlock()
	if globalReaper.started {
		return nil
	}
	globalReaper.started = true
	go globalReaper.loop()
	return nil
}

// Register subscribes to the exit of pid. Returns a channel that receives
// exactly one ExitInfo. Safe to call before or after the child exits
// (cache closes the race). Also kicks the idle loop so a child that dies
// right after spawn is reaped immediately, not on the next idle poll.
func Register(pid int) ExitChan {
	ch := make(ExitChan, 1)
	globalReaper.mu.Lock()
	if info, ok := globalReaper.cache[pid]; ok {
		ch <- info
		delete(globalReaper.cache, pid)
		globalReaper.removeOrder(pid)
		globalReaper.mu.Unlock()
		return ch
	}
	globalReaper.waiting[pid] = ch
	globalReaper.mu.Unlock()
	select {
	case globalReaper.kick <- struct{}{}:
	default:
	}
	return ch
}

// Deregister removes a subscription (e.g. caller gave up waiting). The
// exit, if it arrives later, lands in the cache and is dropped on eviction.
func Deregister(pid int) {
	globalReaper.mu.Lock()
	delete(globalReaper.waiting, pid)
	globalReaper.mu.Unlock()
}

// CachedExit non-blockingly reports a reaped-but-unclaimed exit for pid.
func CachedExit(pid int) (ExitInfo, bool) {
	globalReaper.mu.Lock()
	defer globalReaper.mu.Unlock()
	info, ok := globalReaper.cache[pid]
	return info, ok
}

func (r *reaper) loop() {
	for {
		select {
		case <-r.stop:
			return
		default:
		}
		var status unix.WaitStatus
		pid, err := unix.Wait4(-1, &status, unix.WNOHANG, nil)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			// ECHILD (or other): nothing to reap right now. Children may
			// appear later (we are a subreaper) — wake on the next Register
			// kick or on the idle poll, whichever first. The poll backs
			// off while fully idle; the kick never backs off (spawn
			// latency is one select wake regardless of the backoff).
			r.idleWait()
			continue
		}
		if pid == 0 {
			// Children exist, none has exited yet. Park in a BLOCKING wait4:
			// the kernel wakes it the instant any child (or reparented
			// orphan — we are the subreaper) dies, so exit-delivery latency
			// is one syscall with zero polling. The old 2ms WNOHANG spin
			// woke 500x/s whenever any child was alive — measurable idle
			// CPU even with one app, scaling with nothing.
			//
			// The spawn->register race stays closed without the spin: a
			// child that dies before Register is reaped here and cached
			// (deliver), and Register consults the cache under the reaper
			// lock. The kick matters only on the ECHILD path (no children
			// at all), where idleWait selects on it.
			bp, berr := unix.Wait4(-1, &status, 0, nil)
			if berr == unix.EINTR {
				continue
			}
			if berr != nil {
				// ECHILD: the last child vanished between the two calls.
				continue
			}
			if bp > 0 {
				r.deliver(bp, waitStatusToExit(bp, status))
			}
			continue
		}
		r.deliver(pid, waitStatusToExit(pid, status))
	}
}

// idleWait parks the loop while no children are reappable: either a fresh
// Register (kick) or the idle poll interval wakes it. The poll duration
// doubles from 25ms up to 800ms across consecutive silent parks (an idle
// daemon's dominant wake source used to be 40 timer allocations + wait4
// syscalls per second; backoff + a reusable timer cut that to ~1-2), and
// resets to base on any kick or successful reap so active supervision
// never observes the widened interval.
func (r *reaper) idleWait() {
	if r.idleTimer == nil {
		r.idleTimer = time.NewTimer(r.idlePoll)
	} else {
		r.idleTimer.Reset(r.idlePoll)
	}
	select {
	case <-r.kick:
		if !r.idleTimer.Stop() {
			select {
			case <-r.idleTimer.C:
			default:
			}
		}
		r.idlePoll = reaperIdleBase
	case <-r.idleTimer.C:
		if next := r.idlePoll * 2; next <= reaperIdleMax {
			r.idlePoll = next
		}
	}
}

func (r *reaper) deliver(pid int, info ExitInfo) {
	r.idlePoll = reaperIdleBase // activity: next idle park starts short
	r.mu.Lock()
	defer r.mu.Unlock()
	if ch, ok := r.waiting[pid]; ok {
		delete(r.waiting, pid)
		ch <- info // buffered(1); single consumer per pid by contract
		return
	}
	// Nobody waiting (yet): cache it for a later Register. This is what
	// makes spawn-then-register race-free.
	r.cache[pid] = info
	r.order = append(r.order, pid)
	for len(r.order) > reaperCacheCap {
		evict := r.order[0]
		r.order = r.order[1:]
		delete(r.cache, evict)
	}
}

func (r *reaper) removeOrder(pid int) {
	for i, p := range r.order {
		if p == pid {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}

func waitStatusToExit(pid int, ws unix.WaitStatus) ExitInfo {
	info := ExitInfo{Pid: pid}
	if ws.Signaled() {
		info.Signaled = true
		info.TermSig = int(ws.Signal())
	} else {
		info.ExitCode = ws.ExitStatus()
	}
	return info
}
