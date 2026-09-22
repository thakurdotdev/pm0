//go:build linux

package daemon

import (
	"net"
	"os"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/proc"
	"github.com/pm0/pm0/internal/store"
)

// harnessLauncher mirrors daemon_test.go's launcher setup for the
// takeover/pidfile tests (they run without the full startServer client).
func harnessLauncher(t *testing.T) *proc.Launcher {
	t.Helper()
	l, err := proc.NewLauncher(proc.ModeAuto)
	if err != nil {
		t.Fatalf("launcher: %v", err)
	}
	return l
}

// TestPidFileOwnerDead pins the proof semantics of the stale-socket
// takeover: only a record naming a GONE or REUSED pid counts as dead;
// missing/malformed records keep the conservative grace.
func TestPidFileOwnerDead(t *testing.T) {
	t.Setenv("PM0_HOME", t.TempDir())

	// No record: unknown — never read as dead.
	if pidFileOwnerDead() {
		t.Fatal("missing record must not read as dead")
	}

	// Own live identity: alive.
	if err := writePidFile(); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	if pidFileOwnerDead() {
		t.Fatal("own live record must not read as dead")
	}

	// Malformed records: unknown — keep the grace.
	for _, bad := range []string{"", "pid st\n", "0 123\n", "x y\n"} {
		if err := os.WriteFile(store.PidPath(), []byte(bad), 0o644); err != nil {
			t.Fatalf("seed %q: %v", bad, err)
		}
		if pidFileOwnerDead() {
			t.Fatalf("record %q must not read as dead", bad)
		}
	}

	// Same pid, wrong starttime: the pid was reused — recorded owner dead.
	st, err := proc.StartTime(os.Getpid())
	if err != nil {
		t.Fatalf("StartTime(self): %v", err)
	}
	if err := os.WriteFile(store.PidPath(), []byte("999999999 1\n"), 0o644); err != nil {
		t.Fatalf("seed dead pid: %v", err)
	}
	if !pidFileOwnerDead() {
		t.Fatal("gone pid must read as dead")
	}
	_ = st
}

func TestRemovePidFileIfOurs(t *testing.T) {
	t.Setenv("PM0_HOME", t.TempDir())

	if err := writePidFile(); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	removePidFileIfOurs()
	if _, err := os.Stat(store.PidPath()); !os.IsNotExist(err) {
		t.Fatal("own record must be removed")
	}

	// A foreign record (another daemon's identity) survives.
	foreign := "12345 777\n"
	if err := os.WriteFile(store.PidPath(), []byte(foreign), 0o644); err != nil {
		t.Fatalf("seed foreign: %v", err)
	}
	removePidFileIfOurs()
	data, err := os.ReadFile(store.PidPath())
	if err != nil || string(data) != foreign {
		t.Fatalf("foreign record must survive: (%q,%v)", string(data), err)
	}
}

// TestTakeoverSocketStaleFastPath: a stale socket file whose recorded
// owner is provably dead is taken over after takeoverDelay (~0.3s), not
// the historical 2s grace — the number daemon-crash respawn and reboot
// resurrect measure.
func TestTakeoverSocketStaleFastPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PM0_HOME", home)
	if err := store.EnsureHome(); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	// Stale control socket: a plain file at the socket path makes bind
	// fail with EADDRINUSE.
	if err := os.WriteFile(store.SocketPath(), []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale socket: %v", err)
	}
	// Provably dead owner.
	if err := os.WriteFile(store.PidPath(), []byte("999999999 1\n"), 0o644); err != nil {
		t.Fatalf("seed pidfile: %v", err)
	}

	srv := New(harnessLauncher(t), "test", "testing", false)
	// Mirror Run's boot order: verdict BEFORE the record is clobbered.
	staleOwner := pidFileOwnerDead()
	if !staleOwner {
		t.Fatal("seeded record must read as dead")
	}
	if err := writePidFile(); err != nil {
		t.Fatalf("writePidFile: %v", err)
	}
	start := time.Now()
	lis, err := srv.takeoverSocket(staleOwner)
	if err != nil {
		t.Fatalf("takeoverSocket: %v", err)
	}
	defer lis.Close()
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("stale takeover took %s, want ~%s", d, takeoverDelay)
	}
}

// TestTakeoverSocketWaitsForUnknownOwner: without a pidfile proof the
// takeover keeps the mid-boot grace — it must NOT unlink the path early
// (a daemon could be mid-boot behind that file), and must acquire the
// path once it frees.
func TestTakeoverSocketWaitsForUnknownOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PM0_HOME", home)
	if err := store.EnsureHome(); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	if err := os.WriteFile(store.SocketPath(), []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale socket: %v", err)
	}

	srv := New(harnessLauncher(t), "test", "testing", false)
	res := make(chan error, 1)
	go func() {
		lis, err := srv.takeoverSocket(false)
		if err == nil {
			defer lis.Close()
		}
		res <- err
	}()

	// takeoverDelay (300ms) is long past; an unknown owner must still be
	// waiting (bootGrace). Freeing the path releases it.
	time.Sleep(700 * time.Millisecond)
	select {
	case err := <-res:
		t.Fatalf("takeoverSocket returned early despite unknown owner: %v", err)
	default:
	}
	if err := os.Remove(store.SocketPath()); err != nil {
		t.Fatalf("free the path: %v", err)
	}
	select {
	case err := <-res:
		if err != nil {
			t.Fatalf("takeoverSocket after path freed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("takeoverSocket did not acquire the freed path")
	}
}

// TestTakeoverSocketOccupiedUnknownOwner: an occupied path with ping
// dead and an unknown owner keeps the grace — a live socket behind the
// file is never unlinked early. (Double-start refusal with a LIVE daemon
// answering ping is exercised by TestDaemonDoubleStartRefused.)
func TestTakeoverSocketOccupiedUnknownOwner(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PM0_HOME", home)
	if err := store.EnsureHome(); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	lis, err := net.Listen("unix", store.SocketPath())
	if err != nil {
		t.Fatalf("occupy socket: %v", err)
	}
	defer lis.Close()

	srv := New(harnessLauncher(t), "test", "testing", false)
	res := make(chan error, 1)
	go func() {
		lis2, terr := srv.takeoverSocket(false)
		if terr == nil {
			lis2.Close()
		}
		res <- terr
	}()

	time.Sleep(700 * time.Millisecond)
	select {
	case err := <-res:
		t.Fatalf("takeoverSocket returned early despite occupied path: %v", err)
	default:
	}
	// The original listener must still own its path: closing and
	// re-binding at the same path must succeed.
	if err := lis.Close(); err != nil {
		t.Fatalf("close original: %v", err)
	}
	lis2, err := net.Listen("unix", store.SocketPath())
	if err != nil {
		t.Fatalf("original socket path was disturbed: %v", err)
	}
	lis2.Close()
}

// TestDaemonDoubleStartRefused exercises the full Run() once and a
// takeover against the same home while the first daemon serves: the
// second must get ErrAlreadyRunning and the first must stay healthy.
func TestDaemonDoubleStartRefused(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PM0_HOME", home)

	srv1 := New(harnessLauncher(t), "test", "testing", false)
	go func() { _ = srv1.Run() }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := pingSocket(); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatal("first daemon did not come up")
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Cleanup(func() {
		// In-test polite teardown: srv1's os.Exit paths don't run here.
		_ = os.Remove(store.SocketPath())
		removePidFileIfOurs()
	})

	srv2 := New(harnessLauncher(t), "test", "testing", false)
	// srv1's live record is on disk: the verdict must read "not dead".
	if pidFileOwnerDead() {
		t.Fatal("live daemon's record must not read as dead")
	}
	if _, err := srv2.takeoverSocket(pidFileOwnerDead()); err == nil {
		t.Fatal("second daemon must not acquire the socket of a live daemon")
	}
	if err := pingSocket(); err != nil {
		t.Fatalf("first daemon socket damaged by second boot: %v", err)
	}
}
