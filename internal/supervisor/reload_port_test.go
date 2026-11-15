//go:build linux

package supervisor

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/machine"
	"github.com/pm0/pm0/test/harness"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// startListener starts a fork-mode app bound to addr and waits until it
// holds the port.
func startListener(t *testing.T, s *Supervisor, name, addr string, reuse bool) (int, AppView) {
	t.Helper()
	cfg := harness.SleepConfig(harness.UniqueName(name))
	cfg.Args = []string{"--listen", addr}
	if reuse {
		cfg.Args = append(cfg.Args, "--reuseport")
	}
	cfg.MinUptime = 300 * time.Millisecond
	cfg.ListenTimeout = 5 * time.Second
	id, err := s.StartApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	harness.WaitFor(t, 5*time.Second, "app listening", func() bool {
		c, err := net.Dial("tcp4", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
	view, _ := s.Describe(strconv.Itoa(id))
	return id, view
}

func assertReloaded(t *testing.T, s *Supervisor, id int, before AppView, addr string) {
	t.Helper()
	after, ok := s.Describe(strconv.Itoa(id))
	if !ok || after.Runtime.Status != machine.StatusOnline {
		t.Fatalf("after reload: ok=%v view=%+v", ok, after)
	}
	if after.Runtime.Pid == before.Runtime.Pid {
		t.Fatalf("reload kept the old pid %d", before.Runtime.Pid)
	}
	if after.Runtime.RestartTime != before.Runtime.RestartTime+1 {
		t.Fatalf("restart_time = %d, want %d", after.Runtime.RestartTime, before.Runtime.RestartTime+1)
	}
	if _, parked := s.apps[parkKey(id)]; parked {
		t.Fatal("reload left a parked slot")
	}
	harness.WaitGone(t, before.Runtime.Pid, 5*time.Second, "old incarnation")
	harness.WaitFor(t, 5*time.Second, "replacement listening", func() bool {
		c, err := net.Dial("tcp4", addr)
		if err == nil {
			c.Close()
		}
		return err == nil
	})
}

// A plain fork-mode server (no SO_REUSEPORT) cannot overlap with its
// replacement: reload must fall back to an in-place restart instead of
// crash-looping the replacement on EADDRINUSE and rolling back.
func TestReloadForkPortWithoutReuseport(t *testing.T) {
	s := newSupervisor(t)
	addr := freePort(t)
	id, before := startListener(t, s, "reload-plain", addr, false)
	if affected, err := s.ReloadIDs([]int{id}); err != nil || len(affected) != 1 {
		t.Fatalf("reload = %v, %v", affected, err)
	}
	assertReloaded(t, s, id, before, addr)
}

// With SO_REUSEPORT the overlapping roll still works.
func TestReloadForkPortWithReuseport(t *testing.T) {
	s := newSupervisor(t)
	addr := freePort(t)
	id, before := startListener(t, s, "reload-reuse", addr, true)
	if affected, err := s.ReloadIDs([]int{id}); err != nil || len(affected) != 1 {
		t.Fatalf("reload = %v, %v", affected, err)
	}
	assertReloaded(t, s, id, before, addr)
}
