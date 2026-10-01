//go:build linux

package machine

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/test/harness"
)

func TestLeaderExitCleansDescendantsBeforePolicy(t *testing.T) {
	for _, policy := range []string{"no-autorestart", "stop-exit-code", "restart"} {
		t.Run(policy, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "orphan.pid")
			cfg := config.DefaultFor(harness.UniqueName("exit-tree"), fakechild)
			cfg.Args = []string{"--setsid-child", "--orphan-pidfile", file}
			cfg.Autorestart = policy != "no-autorestart"
			if policy == "stop-exit-code" {
				cfg.StopExitCodes = []int{0}
			}
			// Hold the next generation in waiting so the first descendant's
			// identity cannot be overwritten before we inspect it.
			cfg.RestartDelay = time.Minute
			a, err := New(cfg, harness.NewLauncher(t), Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { retireActor(a) })
			if err := a.Start(); err != nil {
				t.Fatal(err)
			}
			harness.WaitBarrier(t, file, 5*time.Second)
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var pid int
			if _, err := fmt.Sscanf(string(b), "%d", &pid); err != nil || pid <= 0 {
				t.Fatalf("invalid descendant identity %q", b)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			want := StatusStopped
			if policy == "restart" {
				want = StatusWaiting
			}
			waitStatus(t, a, 5*time.Second, want)
			harness.WaitGone(t, pid, time.Second, "descendant after leader exit")
			if snap := a.Snapshot(); snap.Pid != 0 || snap.ExitCode == nil || *snap.ExitCode != 0 {
				t.Fatalf("exit bookkeeping lost during cleanup: %+v", snap)
			}
		})
	}
}
