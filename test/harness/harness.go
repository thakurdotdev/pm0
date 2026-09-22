//go:build linux

// Package harness is test-only shared scaffolding for the supervision
// packages (machine, supervisor): fakechild building, launcher setup, and
// polling helpers. Production code must not import it.
package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/proc"
)

// BuildFakechild compiles test/fakechild to a temp binary. Call it from
// TestMain BEFORE any launcher exists: the reaper owns all waitpid in the
// test process and must not race `go build`'s children (same rule as the
// proc package tests).
func BuildFakechild() (path string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "pm0-fakechild")
	if err != nil {
		return "", nil, err
	}
	bin := filepath.Join(dir, "fakechild")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/pm0/pm0/test/fakechild")
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("build fakechild: %v\n%s", err, out)
	}
	return bin, func() { _ = os.RemoveAll(dir) }, nil
}

var nameCounter atomic.Uint64

// UniqueName yields a fresh app name per call — names double as cgroup
// dirs and orphan-attribution marker values, so collisions across tests
// would let one test's sweep kill another's tree members.
func UniqueName(base string) string {
	return fmt.Sprintf("%s-%d", base, nameCounter.Add(1))
}

// NewLauncher returns a launcher in auto mode, failing the test on error.
func NewLauncher(t *testing.T) *proc.Launcher {
	t.Helper()
	l, err := proc.NewLauncher(proc.ModeAuto)
	if err != nil {
		t.Fatalf("NewLauncher: %v", err)
	}
	return l
}

// WithSignalBarrier injects `--report-pid <path>` into cfg.Args and returns
// the barrier path. fakechild writes "pid starttime" there AFTER installing
// its signal handlers, so the file's presence proves a signal to the child
// will hit the installed discipline, not the kernel default disposition.
// Tests that Stop/Signal a freshly spawned child MUST wait on the barrier
// first — signaling during the child's exec/init window kills it with the
// default disposition (a test artifact, not supervisor behavior; same rule
// as the proc package tests). Relaunches rewrite the file, so a barrier
// wait before signaling a relaunched child can read the previous
// generation's identity — only the death-manner assertions of the FIRST
// child are barrier-exact.
func WithSignalBarrier(cfg config.App) (config.App, string) {
	dir, err := os.MkdirTemp("", "pm0-barrier")
	if err != nil {
		panic("harness: barrier tmpdir: " + err.Error())
	}
	path := filepath.Join(dir, "ready")
	cfg.Args = append(append([]string{}, cfg.Args...), "--report-pid", path)
	return cfg, path
}

// WaitBarrier blocks until the identity file appears.
func WaitBarrier(t *testing.T, path string, d time.Duration) {
	t.Helper()
	WaitFor(t, d, "signal barrier "+path, func() bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

// CrashConfig returns PM2 defaults tuned for fast tests: the child exits
// with code after afterMs milliseconds; min_uptime is long enough that
// such exits count as unstable.
func CrashConfig(name string, afterMs, exitCode int, minUptime time.Duration, maxRestarts int) config.App {
	cfg := config.DefaultFor(name, fakechildOrPanic())
	cfg.Args = []string{"--exit-after", fmt.Sprint(afterMs), "--exit-code", fmt.Sprint(exitCode)}
	cfg.MinUptime = minUptime
	cfg.MaxRestarts = maxRestarts
	return cfg
}

// SleepConfig returns a config for a child that runs forever (default
// fakechild behavior: sleeps, exits 0 on SIGINT/SIGTERM).
func SleepConfig(name string) config.App {
	return config.DefaultFor(name, fakechildOrPanic())
}

// fakechild path cache: tests set it via SetFakechildPath in TestMain.
var fakechildPath string

// SetFakechildPath records the built fakechild binary for config helpers.
func SetFakechildPath(p string) { fakechildPath = p }

func fakechildOrPanic() string {
	if fakechildPath == "" {
		panic("harness.SetFakechildPath not called in TestMain")
	}
	return fakechildPath
}

// WaitFor polls until cond passes or the deadline expires.
func WaitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s after %v", what, d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// WaitGone polls until pid disappears from /proc (reaped, not zombie).
func WaitGone(t *testing.T, pid int, d time.Duration, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(fmt.Sprintf("/proc/%d/stat", pid)); err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s pid %d still present after %v", what, pid, d)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
