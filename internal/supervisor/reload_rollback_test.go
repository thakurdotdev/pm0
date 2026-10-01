//go:build linux

package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/machine"
	"github.com/pm0/pm0/test/harness"
)

func TestReloadSpawnFailureRestoresOldWorker(t *testing.T) {
	s := newSupervisor(t)
	path := filepath.Join(t.TempDir(), "sleep")
	if err := os.Symlink("/bin/sleep", path); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultFor(harness.UniqueName("rollback"), path)
	cfg.Args = []string{"60"}
	id, err := s.StartApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Describe(strconv.Itoa(id))
	old := s.apps[id]
	// Clean up even if a regression loses the old worker from the registry.
	t.Cleanup(func() {
		select {
		case <-old.Done():
		default:
			_ = old.Delete()
		}
	})
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if affected, err := s.ReloadIDs([]int{id}); err == nil || len(affected) != 0 {
		t.Fatalf("reload = %v, %v; expected spawn failure", affected, err)
	}
	after, ok := s.Describe(strconv.Itoa(id))
	if !ok || after.Runtime.Status != machine.StatusOnline || after.Runtime.Pid != before.Runtime.Pid || after.Serial != before.Serial {
		t.Fatalf("failed reload lost original incarnation: before=%+v after=%+v", before, after)
	}
	if _, parked := s.apps[parkKey(id)]; parked {
		t.Fatal("rollback left a parked slot")
	}
	if err := s.Delete(strconv.Itoa(id)); err != nil {
		t.Fatal(err)
	}
	harness.WaitGone(t, before.Runtime.Pid, 3*time.Second, "original worker after delete")
}

func TestDeleteDuringReloadDoesNotRestoreOldWorker(t *testing.T) {
	s := newSupervisor(t)
	cfg := harness.SleepConfig(harness.UniqueName("delete-roll"))
	cfg.MinUptime = time.Minute
	cfg.ListenTimeout = 2 * time.Minute
	id, err := s.StartApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Describe(strconv.Itoa(id))
	done := make(chan error, 1)
	go func() {
		_, err := s.ReloadIDs([]int{id})
		done <- err
	}()
	harness.WaitFor(t, 5*time.Second, "replacement registration", func() bool {
		view, ok := s.Describe(strconv.Itoa(id))
		return ok && view.Serial != before.Serial
	})
	if err := s.Delete(strconv.Itoa(id)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("reload succeeded after replacement deletion")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not finish after deletion")
	}
	if len(s.List()) != 0 {
		t.Fatal("reload resurrected a deleted app")
	}
	harness.WaitGone(t, before.Runtime.Pid, 3*time.Second, "parked worker after delete")
}
