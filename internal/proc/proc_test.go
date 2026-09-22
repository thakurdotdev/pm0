//go:build linux

package proc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakechildPath is built once in TestMain, BEFORE any test starts the
// reaper: the reaper owns all waitpid in this process, and an early
// reaper would steal `go build`'s children out from under os/exec.Wait.
var fakechildPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pm0-fakechild")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testmain: tmpdir:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	bin := filepath.Join(dir, "fakechild")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/pm0/pm0/test/fakechild")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "testmain: build fakechild: %v\n%s\n", err, out)
		os.Exit(1)
	}
	fakechildPath = bin
	os.Exit(m.Run())
}

var appNameCounter atomic.Uint64

// uniqueAppName yields a fresh app name per call. Names double as cgroup
// dir names and as the orphan-attribution marker value, so collisions
// across tests would let one test's sweep kill another's tree members.
func uniqueAppName(base string) string {
	return fmt.Sprintf("%s-%d", base, appNameCounter.Add(1))
}

// testKillTimeout keeps the force phase fast; I5's bound is asserted
// relative to this value, never hardcoded.
const testKillTimeout = 600 * time.Millisecond

func newTestLauncher(t *testing.T, mode Mode) *Launcher {
	t.Helper()
	l, err := NewLauncher(mode)
	if err != nil {
		t.Fatalf("NewLauncher(%s): %v", mode, err)
	}
	return l
}

// requireCgroupMode skips when the host has no delegated cgroup v2 subtree
// (cgroup v1 hosts, unprivileged containers). The privileged CI job runs
// these for real; pgid-mode siblings run everywhere.
func requireCgroupMode(t *testing.T, mode Mode) {
	t.Helper()
	d := Detect()
	if !d.CgroupUsable {
		t.Skipf("no delegated cgroup v2 subtree on this host (mode=%s warning=%q)", d.Mode, d.Warning)
	}
	_ = mode
}

// launch launches a fakechild with the given args and registers cleanup.
func launch(t *testing.T, l *Launcher, name string, args ...string) *Handle {
	t.Helper()
	h, err := l.Launch(Spec{
		Name:        name,
		BinPath:     fakechildPath,
		Args:        args,
		KillTimeout: testKillTimeout,
	})
	if err != nil {
		t.Fatalf("launch %v: %v", args, err)
	}
	t.Cleanup(func() {
		select {
		case <-h.Done():
		default:
			_ = h.Signal(syscall.SIGKILL)
			select {
			case <-h.Done():
			case <-time.After(5 * time.Second):
			}
		}
	})
	return h
}

// waitDone blocks for the leader's exit with a generous timeout.
func waitDone(t *testing.T, h *Handle, d time.Duration) ExitInfo {
	t.Helper()
	select {
	case <-h.Done():
		return h.ExitResult()
	case <-time.After(d):
		t.Fatalf("leader pid %d did not exit within %v", h.PID(), d)
		return ExitInfo{}
	}
}

// readIdentity polls a "pid starttime" file written by fakechild.
func readIdentity(t *testing.T, path string, d time.Duration) (int, uint64) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(data))
			if len(fields) == 2 {
				pid, e1 := strconv.Atoi(fields[0])
				st, e2 := strconv.ParseUint(fields[1], 10, 64)
				if e1 == nil && e2 == nil && pid > 0 {
					return pid, st
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("identity file %s never appeared", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitPidGone asserts the process fully disappeared from /proc — gone,
// not zombie (a zombie would mean the reaper failed to reap it).
func waitPidGone(t *testing.T, pid int, d time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		st, err := procState(pid)
		if err != nil {
			return // /proc entry gone: reaped
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s pid %d still present (state=%c) after %v", what, pid, st, d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// assertTreeEmpty is the invariant checker (plan: "after every
// stop/delete/kill in every test, assert the tree is empty — non-optional,
// built into the harness"). For cgroup modes it asserts cgroup.procs is
// empty AND the per-app cgroup dir was removed; for pgid mode it asserts
// no group members and no attributed reparented orphans remain (I1).
func assertTreeEmpty(t *testing.T, h *Handle) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		pids := h.TreePids()
		if len(pids) == 0 {
			switch h.Mode() {
			case ModeCgroupKill, ModeCgroupFreeze:
				if _, err := os.Stat(filepath.Join(h.CgroupPath(), "cgroup.procs")); !os.IsNotExist(err) {
					t.Fatalf("cgroup dir %s not removed after Stop", h.CgroupPath())
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("invariant I1 violated: app %q tree still has pids %v", h.spec.Name, pids)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sigTerm() syscall.Signal { return syscall.SIGTERM }
func sigKill() syscall.Signal { return syscall.SIGKILL }

// killPathsForTest returns every kill path runnable on this host. pgid
// always; cgroup paths only where delegation allows them to really run.
func killPathsForTest(t *testing.T) []Mode {
	d := Detect()
	modes := []Mode{ModePgid}
	if d.CgroupUsable {
		major, minor, err := kernelVersion(d.KernelRelease)
		if err == nil {
			if major > 5 || (major == 5 && minor >= 14) {
				modes = append(modes, ModeCgroupKill)
			}
			if major > 5 || (major == 5 && minor >= 2) {
				modes = append(modes, ModeCgroupFreeze)
			}
		}
	}
	return modes
}
