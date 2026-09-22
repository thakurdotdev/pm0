//go:build linux

// Package supervisor is the registry layer over the machine actors: it
// allocates pm_ids, resolves identifiers (numeric id or app name) the way
// the pm2 CLI does, and exposes list/describe views for the M2 daemon's
// jlist rendering.
//
// pm_id policy (docs/compat.md §3.2): ids are non-negative, unique among
// registered apps, and `start` allocates the LOWEST unused id — deleting an
// app frees its id for reuse, exactly like pm2.
//
// Concurrency: registry operations are serialized under one mutex; a Stop
// or Delete holds it while the kill path runs (bounded by kill_timeout +
// grace). This matches pm2's effectively serialized per-daemon command
// handling and keeps M1 simple; the M2 daemon can shard later if needed.
package supervisor

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/machine"
	"github.com/pm0/pm0/internal/proc"
)

// ErrNotFound is returned when an identifier matches no registered app.
var ErrNotFound = errors.New("supervisor: no such app")

// AppView pairs the immutable config with the runtime snapshot — the two
// halves of a jlist element (§2.1/§2.2). ID is the pm_id; Serial is the
// REGISTRATION serial (unique per registered app incarnation, runtime-only
// — pm_ids are reused after the registry empties, and the ops layer must
// not confuse a deleted app's runner with its successor at the same id).
// Marker is the app's attribution marker (sanitized name + pm id) — the
// key monitoring attribution matches child environments against.
type AppView struct {
	ID      int
	Serial  uint64
	Marker  string
	Config  config.App
	Runtime machine.Snapshot
}

// Supervisor owns the app registry.
type Supervisor struct {
	launcher *proc.Launcher

	mu     sync.Mutex
	apps   map[int]*machine.App
	nextID int // monotonic pm_id counter (observed pm2 God.next_id)

	// clusterPreload is the M6 reuseport preload path; launched node
	// cluster instances --require it. Set once by the daemon at boot.
	clusterPreload string

	// reloadMu serializes reloads (each holds registry ops piecemeal
	// while its roll is in flight; two concurrent rolls of the same
	// family would race the parked slots).
	reloadMu sync.Mutex

	// Registration serials (runtime-only): one per app incarnation,
	// handed to AppView so consumers keyed by pm_id can detect reuse.
	serials    map[int]uint64
	nextSerial uint64

	// version counts registry mutations (register/delete). The ops loop
	// reads it to skip per-tick registry copies when nothing changed —
	// the lazy-tick item from docs/benchmarks2.md. Atomic reads without
	// the registry lock.
	version uint64
}

// New creates the registry. The launcher (and thus the reaper) is shared.
func New(l *proc.Launcher) *Supervisor {
	return &Supervisor{
		launcher: l,
		apps:     make(map[int]*machine.App),
		serials:  make(map[int]uint64),
	}
}

// SetClusterPreload records the cluster preload path (daemon boot: the
// file is written into PM0_HOME before any cluster start).
func (s *Supervisor) SetClusterPreload(path string) { s.clusterPreload = path }

// clusterPreloadPath exposes the preload for machine.Options.
func (s *Supervisor) clusterPreloadPath() string { return s.clusterPreload }

// bumpVersionLocked counts a registry mutation. Caller holds s.mu.
func (s *Supervisor) bumpVersionLocked() { atomic.AddUint64(&s.version, 1) }

// Version reports the registry mutation counter (monotonic). Callers use
// it to detect change without copying the registry.
func (s *Supervisor) Version() uint64 { return atomic.LoadUint64(&s.version) }

// assignSerialLocked stamps a fresh serial for id. Caller holds s.mu.
func (s *Supervisor) assignSerialLocked(id int) uint64 {
	s.nextSerial++
	s.serials[id] = s.nextSerial
	return s.nextSerial
}

// StartApp validates, allocates the lowest unused pm_id, registers the
// app, and starts it. A spawn failure still REGISTERS the app (parked
// errored — pm2 keeps bad scripts listed) and returns the error alongside
// the id.
func (s *Supervisor) StartApp(cfg config.App) (int, error) {
	ids, errs := s.StartApps([]config.App{cfg}, nil)
	return ids[0], errs
}

// StartApps registers and starts a batch, allocating a CONTIGUOUS block of
// lowest-unused pm_ids (compat.md §3.2: -i N instances take consecutive
// ids named <name>-0..<name>-N-1). fill runs for each app with the
// allocated id BEFORE the machine is built, under the registry lock — the
// daemon uses it to inject per-id log/pid paths without an allocate/use
// race. A spawn failure still registers that app (parked errored); the
// batch continues and the first error is returned.
func (s *Supervisor) StartApps(cfgs []config.App, fill func(cfg config.App, id int) config.App) ([]int, error) {
	if len(cfgs) == 0 {
		return nil, errors.New("supervisor: empty start batch")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Monotonic allocation (observed pm2 God.next_id++): ids are handed
	// out consecutively from a counter that never rewinds while the
	// registry is non-empty; deleting ALL apps resets it to 0 (observed
	// pm2 ActionMethods delete-all path). The M0 plan's "lowest unused"
	// guess was corrected against real pm2 in M2 — see compat.md §3.2.
	id := s.nextID
	s.nextID += len(cfgs)

	ids := make([]int, 0, len(cfgs))
	var firstErr error
	for k, cfg := range cfgs {
		pid := id + k
		if fill != nil {
			cfg = fill(cfg, pid)
		}
		app, err := machine.New(cfg, s.launcher, machine.Options{
			ID:                 pid,
			ClusterPreloadPath: s.clusterPreloadPath(),
		})
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			ids = append(ids, -1)
			continue
		}
		s.apps[pid] = app
		s.assignSerialLocked(pid)
		ids = append(ids, pid)
		if err := app.Start(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.bumpVersionLocked()
	return ids, firstErr
}

// StartAtID starts a fresh app at a specific pm_id (resurrect: dump-driven
// ids are stable across daemon restarts, compat.md §3.2). A spawn failure
// still registers the app (parked errored).
func (s *Supervisor) StartAtID(cfg config.App, id int) error {
	return s.StartReplacementAt(cfg, id, 0)
}

// StartReplacementAt is StartAtID with a seeded restart_time (cluster
// reload: the replacement counts as prev+1, PM2 softReload parity).
func (s *Supervisor) StartReplacementAt(cfg config.App, id, prevRestarts int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.apps[id]; taken {
		return fmt.Errorf("supervisor: pm_id %d already registered", id)
	}
	app, err := machine.New(cfg, s.launcher, machine.Options{
		ID:                 id,
		InitialRestarts:    prevRestarts,
		ClusterPreloadPath: s.clusterPreloadPath(),
	})
	if err != nil {
		return err
	}
	s.apps[id] = app
	s.assignSerialLocked(id)
	if id >= s.nextID {
		s.nextID = id + 1
	}
	s.bumpVersionLocked()
	return app.Start()
}

// RegisterAt registers an ALREADY-RUNNING app (adoption) at a specific
// pm_id — dump-driven ids must survive update/resurrect (§3.2). The
// machine actor starts in the adopted-online state; no spawn happens.
func (s *Supervisor) RegisterAt(cfg config.App, id int, h *proc.Handle, startedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.apps[id]; taken {
		return fmt.Errorf("supervisor: pm_id %d already registered", id)
	}
	app, err := machine.Adopt(cfg, s.launcher, machine.Options{ID: id}, h, startedAt)
	if err != nil {
		return err
	}
	s.apps[id] = app
	s.assignSerialLocked(id)
	if id >= s.nextID {
		s.nextID = id + 1
	}
	s.bumpVersionLocked()
	return nil
}

// Stop stops an app (identifier: numeric pm_id or name).
func (s *Supervisor) Stop(identifier string) error {
	return s.withApp(identifier, (*machine.App).Stop)
}

// Restart restarts an app (identifier: numeric pm_id or name).
func (s *Supervisor) Restart(identifier string) error {
	return s.withApp(identifier, (*machine.App).Restart)
}

// RestartWithEnv restarts an app after replacing its extra environment
// (restart --update-env: the CLI re-reads the current shell into env).
func (s *Supervisor) RestartWithEnv(identifier string, env map[string]string) error {
	return s.withApp(identifier, func(a *machine.App) error {
		if err := a.UpdateEnv(env); err != nil {
			return err
		}
		return a.Restart()
	})
}

// Delete stops an app, retires its actor, and frees its pm_id.
func (s *Supervisor) Delete(identifier string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.resolveLocked(identifier)
	if err != nil {
		return err
	}
	id := app.ID()
	if err := app.Delete(); err != nil {
		// Kill path failed: the actor still exits (Delete always tears
		// down). Remove the entry so the id is not leaked either way.
		delete(s.apps, id)
		delete(s.serials, id)
		s.resetIDIfEmptyLocked()
		s.bumpVersionLocked()
		return err
	}
	delete(s.apps, id)
	delete(s.serials, id)
	s.resetIDIfEmptyLocked()
	s.bumpVersionLocked()
	return nil
}

// resetIDIfEmptyLocked rewinds the id counter when the last app is gone —
// observed pm2 hands out 0 again once the registry is fully empty.
func (s *Supervisor) resetIDIfEmptyLocked() {
	if len(s.apps) == 0 {
		s.nextID = 0
	}
}

// Signal forwards sig to an app's tree (numeric pm_id or name).
func (s *Supervisor) Signal(identifier string, sig syscall.Signal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.resolveLocked(identifier)
	if err != nil {
		return err
	}
	return app.Signal(sig)
}

// List returns every registered app (config + runtime snapshot), ordered
// by pm_id. Negative keys are reload-parked incarnations — hidden.
func (s *Supervisor) List() []AppView {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int, 0, len(s.apps))
	for id := range s.apps {
		if id < 0 {
			continue // reload-parked old incarnation
		}
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]AppView, 0, len(ids))
	for _, id := range ids {
		app := s.apps[id]
		out = append(out, AppView{ID: id, Serial: s.serials[id], Marker: app.Attrib(), Config: app.Config(), Runtime: app.Snapshot()})
	}
	return out
}

// Describe resolves one app's view (numeric pm_id or name).
func (s *Supervisor) Describe(identifier string) (AppView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.resolveLocked(identifier)
	if err != nil {
		return AppView{}, false
	}
	id0 := app.ID()
	return AppView{ID: id0, Serial: s.serials[id0], Marker: app.Attrib(), Config: app.Config(), Runtime: app.Snapshot()}, true
}

// TreePids lists the live pids of app id's tree (monit, diagnostics).
// nil when the id is unknown or the app is not running.
func (s *Supervisor) TreePids(id int) []int {
	s.mu.Lock()
	app := s.apps[id]
	s.mu.Unlock()
	if app == nil {
		return nil
	}
	return app.TreePids()
}

// withApp resolves and runs op under the registry lock.
func (s *Supervisor) withApp(identifier string, op func(*machine.App) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	app, err := s.resolveLocked(identifier)
	if err != nil {
		return err
	}
	return op(app)
}

// resolveLocked maps an identifier to an app: a numeric string matches a
// pm_id; anything else matches an app name (lowest id wins — pm2 takes the
// first match). Caller holds s.mu.
func (s *Supervisor) resolveLocked(identifier string) (*machine.App, error) {
	if id, err := strconv.Atoi(identifier); err == nil {
		if id < 0 {
			return nil, fmt.Errorf("%w: pm_id %d", ErrNotFound, id)
		}
		app, ok := s.apps[id]
		if !ok {
			return nil, fmt.Errorf("%w: pm_id %d", ErrNotFound, id)
		}
		return app, nil
	}
	var best *machine.App
	for _, app := range s.apps {
		if app.Config().Name == identifier && (best == nil || app.ID() < best.ID()) {
			best = app
		}
	}
	if best == nil {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, identifier)
	}
	return best, nil
}

// cluster reload ---------------------------------------------------------

// ReloadIDs rolls the given apps one pm_id at a time (PM2 reload, God/
// Reload.js softReload): per app, park the old incarnation, start a
// replacement AT THE SAME pm_id, wait for it to stabilize, then retire the
// old with the 'shutdown' node-IPC handoff. With cluster mode the old and
// new instances serve concurrently through the shared reuseport pool, so
// the roll drops no connections. A replacement that dies or fails to
// stabilize within listen_timeout is removed and the old incarnation is
// RESTORED (PM2 unparkOldWorker) — a failed reload never leaves the app
// down. Errors are per-instance: the roll continues with the remaining
// ids and the first error is returned.
func (s *Supervisor) ReloadIDs(ids []int) ([]int, error) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	affected := make([]int, 0, len(ids))
	var firstErr error
	for _, id := range ids {
		if err := s.reloadOne(id); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("reload pm_id %d: %w", id, err)
			}
			continue
		}
		affected = append(affected, id)
	}
	return affected, firstErr
}

// parkKey is the registry slot of a reload-parked old incarnation
// (negative, hidden from List/resolve — PM2 parks at the "_old_<id>" key).
func parkKey(id int) int { return -id - 1 }

func (s *Supervisor) reloadOne(id int) error {
	s.mu.Lock()
	old := s.apps[id]
	if old == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	cfg := old.Config()
	prevRestarts := old.Snapshot().RestartTime
	s.apps[parkKey(id)] = old
	delete(s.apps, id)
	delete(s.serials, id)
	s.bumpVersionLocked()
	s.mu.Unlock()

	// Replacement at the same pm_id (PM2 reuses the id; the old worker
	// rides the _old_ slot until it is retired).
	if err := s.StartReplacementAt(cfg, id, prevRestarts+1); err != nil {
		s.unpark(id, old)
		return fmt.Errorf("start replacement: %w", err)
	}

	// Stability gate. PM2 waits for the cluster 'listening' event up to
	// listen_timeout; pm0 cannot observe a node bind, so the gate is
	// min_uptime stability — for reuseport instances the semantics match
	// (the replacement joins the shared pool as soon as it binds, and the
	// old instance keeps serving through the whole window either way).
	minUptime := cfg.MinUptime
	if minUptime <= 0 {
		minUptime = config.DefaultMinUptime
	}
	deadline := cfg.ListenTimeout
	if deadline <= 0 {
		deadline = config.DefaultListenTimeout
	}
	start := time.Now()
	for {
		time.Sleep(50 * time.Millisecond)
		s.mu.Lock()
		app := s.apps[id]
		s.mu.Unlock()
		if app == nil {
			// The replacement was deleted under us (user delete during
			// the roll) — restore the old out of courtesy; the user asked
			// for deletion, so if the id is taken again just drop the old.
			s.unpark(id, old)
			return fmt.Errorf("replacement deleted mid-roll")
		}
		snap := app.Snapshot()
		switch snap.Status {
		case machine.StatusOnline:
			if time.Since(time.UnixMilli(snap.PmUptimeMs)) >= minUptime {
				return s.retireParked(id, old)
			}
		case machine.StatusErrored, machine.StatusStopped:
			// The replacement died and its policy parked it: restore the
			// old (which never stopped serving).
			// The replacement died and its policy parked it: restore the
			// old (which never stopped serving).
			_ = s.Delete(strconv.Itoa(id))
			s.unpark(id, old)
			return fmt.Errorf("replacement exited (%s)", snap.Status)
		}
		if time.Since(start) > deadline {
			_ = s.Delete(strconv.Itoa(id))
			s.unpark(id, old)
			return fmt.Errorf("replacement not stable within listen_timeout")
		}
	}
}

// retireParked stops the parked old incarnation with the shutdown-IPC
// handoff and removes its registry slot.
func (s *Supervisor) retireParked(id int, old *machine.App) error {
	err := old.DeleteGraceful() // 'shutdown' message, then the kill path
	s.mu.Lock()
	delete(s.apps, parkKey(id))
	delete(s.serials, parkKey(id))
	s.bumpVersionLocked()
	s.mu.Unlock()
	return err
}

// unpark restores a parked old incarnation to its pm_id (PM2
// unparkOldWorker). The slot must be free (the failed replacement was
// deleted first); if another start took the id meanwhile, the old
// incarnation is simply discarded from the registry.
func (s *Supervisor) unpark(id int, old *machine.App) {
	s.mu.Lock()
	if _, taken := s.apps[id]; !taken {
		s.apps[id] = old
		s.assignSerialLocked(id)
	}
	delete(s.apps, parkKey(id))
	delete(s.serials, parkKey(id))
	s.bumpVersionLocked()
	s.mu.Unlock()
}
