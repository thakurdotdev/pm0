//go:build linux

package supervisor

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/machine"
	"github.com/pm0/pm0/test/harness"
)

func TestMain(m *testing.M) {
	bin, cleanup, err := harness.BuildFakechild()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer cleanup()
	harness.SetFakechildPath(bin)
	os.Exit(m.Run())
}

func newSupervisor(t *testing.T) *Supervisor {
	t.Helper()
	s := New(harness.NewLauncher(t))
	// Every app this test registered must be killed at test end — a
	// forgotten sleeper is a leaked process (the actor is garbage but the
	// child outlives the test binary).
	t.Cleanup(func() {
		for _, v := range s.List() {
			_ = s.Delete(strconv.Itoa(v.ID))
		}
	})
	return s
}

// startSleep starts a forever-running app and returns its pm_id.
func startSleep(t *testing.T, s *Supervisor, name string) int {
	t.Helper()
	id, err := s.StartApp(harness.SleepConfig(name))
	if err != nil {
		t.Fatalf("StartApp(%s): %v", name, err)
	}
	view, ok := s.Describe(strconv.Itoa(id))
	if !ok || view.Runtime.Status != machine.StatusOnline {
		t.Fatalf("app %s not online after start: %+v", name, view)
	}
	return id
}

// compat §3.2 (M2 correction from observed pm2): ids come from a
// monotonic counter that never rewinds while the registry is non-empty;
// deleting ALL apps resets it to 0.
func TestPMIDMonotonicAllocation(t *testing.T) {
	s := newSupervisor(t)

	id0 := startSleep(t, s, harness.UniqueName("sup-a"))
	id1 := startSleep(t, s, harness.UniqueName("sup-b"))
	id2 := startSleep(t, s, harness.UniqueName("sup-c"))
	if id0 != 0 || id1 != 1 || id2 != 2 {
		t.Fatalf("sequential ids expected 0,1,2; got %d,%d,%d", id0, id1, id2)
	}

	// Delete the middle app; the next start does NOT reuse id 1 —
	// observed pm2 hands out 3 (verified against pm2 5.4.2 and 7.0.4).
	if err := s.Delete(strconv.Itoa(id1)); err != nil {
		t.Fatalf("Delete(1): %v", err)
	}
	id3 := startSleep(t, s, harness.UniqueName("sup-d"))
	if id3 != 3 {
		t.Fatalf("monotonic allocation: got %d, want 3", id3)
	}

	// Deleting the rest empties the registry and rewinds the counter.
	if err := s.Delete(strconv.Itoa(id0)); err != nil {
		t.Fatalf("Delete(0): %v", err)
	}
	if err := s.Delete(strconv.Itoa(id2)); err != nil {
		t.Fatalf("Delete(2): %v", err)
	}
	if err := s.Delete(strconv.Itoa(id3)); err != nil {
		t.Fatalf("Delete(3): %v", err)
	}
	if id := startSleep(t, s, harness.UniqueName("sup-e")); id != 0 {
		t.Fatalf("expected reset to 0 after emptying registry, got %d", id)
	}
}

// Identifiers: numeric strings match pm_id, names match the lowest id.
func TestResolveByIdAndName(t *testing.T) {
	s := newSupervisor(t)

	name := harness.UniqueName("sup-named")
	id := startSleep(t, s, name)
	dup := harness.UniqueName("sup-named")
	idDup := startSleep(t, s, dup) // duplicate name, different id

	if idDup != id+1 {
		t.Fatalf("dup ids: %d then %d", id, idDup)
	}

	// By name resolves to the LOWEST id with that name.
	got := s.mustSnapshot(t, s, name)
	if got.Pid == 0 {
		t.Fatal("name resolve returned a dead view")
	}
	if v, _ := s.Describe(name); v.Runtime.Name != name {
		t.Fatalf("resolved name %q", v.Runtime.Name)
	}

	// Numeric strings never fall through to name matching.
	if _, ok := s.Describe(strconv.Itoa(idDup)); !ok {
		t.Fatal("numeric id resolve failed")
	}

	// Deregistered identifier errors loudly.
	if err := s.Stop("9999"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stop(9999) = %v, want ErrNotFound", err)
	}
	if err := s.Stop(harness.UniqueName("sup-missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Stop(missing) = %v, want ErrNotFound", err)
	}
}

// Delete stops the tree (pid reaped) and frees the registry entry.
func TestDeleteStopsTreeAndFreesEntry(t *testing.T) {
	s := newSupervisor(t)

	id := startSleep(t, s, harness.UniqueName("sup-del"))
	view, ok := s.Describe(strconv.Itoa(id))
	if !ok || view.Runtime.Pid <= 0 {
		t.Fatalf("no pid before delete: %+v ok=%v", view, ok)
	}
	pid := view.Runtime.Pid

	if err := s.Delete(strconv.Itoa(id)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	harness.WaitGone(t, pid, 3*time.Second, "deleted app")
	if _, ok := s.Describe(strconv.Itoa(id)); ok {
		t.Fatal("deleted app still resolvable")
	}
	if len(s.List()) != 0 {
		t.Fatalf("List not empty after delete: %d entries", len(s.List()))
	}
}

// List/Describe shapes: config echo + runtime snapshot.
func TestListDescribeShapes(t *testing.T) {
	s := newSupervisor(t)

	cfg := harness.SleepConfig(harness.UniqueName("sup-shape"))
	cfg.Env = map[string]string{"PM0_SUP_TEST": "1"}
	id, err := s.StartApp(cfg)
	if err != nil {
		t.Fatalf("StartApp: %v", err)
	}

	views := s.List()
	if len(views) != 1 {
		t.Fatalf("List len = %d", len(views))
	}
	v := views[0]
	if v.Config.Name != cfg.Name || v.Config.Script != cfg.Script {
		t.Fatalf("config echo wrong: %+v", v.Config)
	}
	if v.Runtime.Status != machine.StatusOnline || v.Runtime.Pid <= 0 {
		t.Fatalf("runtime snapshot wrong: %+v", v.Runtime)
	}
	if v.Runtime.RestartTime != 0 || v.Runtime.UnstableRestarts != 0 {
		t.Fatalf("fresh app counters wrong: %+v", v.Runtime)
	}
	if v.Runtime.CreatedAtMs == 0 || v.Runtime.PmUptimeMs == 0 {
		t.Fatal("timestamps not set")
	}
	if !v.Runtime.Status.Valid() {
		t.Fatal("status outside pinned vocabulary")
	}

	// Stop via name identifier (no dup names here).
	if err := s.Stop(cfg.Name); err != nil {
		t.Fatalf("Stop(name): %v", err)
	}
	if v2, ok := s.Describe(strconv.Itoa(id)); !ok || v2.Runtime.Status != machine.StatusStopped {
		t.Fatalf("post-stop status: %+v ok=%v", v2, ok)
	}
}

// Restart by name/identifier across the supervisor boundary.
func TestSupervisorRestart(t *testing.T) {
	s := newSupervisor(t)

	name := harness.UniqueName("sup-restart")
	id := startSleep(t, s, name)

	if err := s.Restart(strconv.Itoa(id)); err != nil {
		t.Fatalf("Restart(id): %v", err)
	}
	v, _ := s.Describe(strconv.Itoa(id))
	if v.Runtime.Status != machine.StatusOnline || v.Runtime.RestartTime != 1 {
		t.Fatalf("post-restart: %+v", v.Runtime)
	}

	if err := s.Restart(name); err != nil {
		t.Fatalf("Restart(name): %v", err)
	}
	if v, _ := s.Describe(name); v.Runtime.RestartTime != 2 {
		t.Fatalf("restart_time = %d, want 2", v.Runtime.RestartTime)
	}
}

// Concurrent registry ops stay race-clean and consistent.
func TestSupervisorConcurrentOps(t *testing.T) {
	s := newSupervisor(t)

	ids := make([]int, 4)
	for i := range ids {
		ids[i] = startSleep(t, s, harness.UniqueName("sup-conc"))
	}

	var wg sync.WaitGroup
	ops := 0
	for _, id := range ids {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			idStr := strconv.Itoa(id)
			for i := 0; i < 5; i++ {
				switch i % 3 {
				case 0:
					_ = s.Restart(idStr)
				case 1:
					_ = s.Stop(idStr)
				case 2:
					_ = s.Restart(idStr)
				}
			}
		}(id)
		ops++
	}
	wg.Wait()

	for _, id := range ids {
		if err := s.Delete(strconv.Itoa(id)); err != nil {
			t.Fatalf("Delete(%d): %v", id, err)
		}
	}
	if len(s.List()) != 0 {
		t.Fatal("registry not empty after deletes")
	}
	_ = ops
}

// A spawn failure registers the app parked errored and surfaces the error.
func TestSupervisorSpawnFailureRegisters(t *testing.T) {
	s := newSupervisor(t)

	cfg := config.DefaultFor(harness.UniqueName("sup-bad"), "/nonexistent/sup-bin")
	id, err := s.StartApp(cfg)
	if err == nil {
		t.Fatal("StartApp must surface the spawn error")
	}
	v, ok := s.Describe(strconv.Itoa(id))
	if !ok {
		t.Fatal("failed app must stay registered (pm2 lists it)")
	}
	if v.Runtime.Status != machine.StatusErrored {
		t.Fatalf("status = %s, want errored", v.Runtime.Status)
	}
	// id 0 consumed; next app gets 1.
	if id2 := startSleep(t, s, harness.UniqueName("sup-next")); id2 != id+1 {
		t.Fatalf("next id = %d, want %d", id2, id+1)
	}
}

func (s *Supervisor) mustSnapshot(t *testing.T, _ *Supervisor, identifier string) machine.Snapshot {
	t.Helper()
	v, ok := s.Describe(identifier)
	if !ok {
		t.Fatalf("describe %q failed", identifier)
	}
	return v.Runtime
}
