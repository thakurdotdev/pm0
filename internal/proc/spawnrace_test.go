//go:build linux

package proc

import (
	"testing"
	"time"
)

// TestStopRightAfterLaunchIsGraceful: in cgroup modes the re-exec wrapper
// attaches itself to the app cgroup a few ms after fork. A stop issued in
// that window used to signal an empty cgroup.procs — nobody got SIGINT, the
// kill path waited the full kill_timeout, then SIGKILLed the app. The
// leader must get the graceful signal and exit on its own, promptly.
func TestStopRightAfterLaunchIsGraceful(t *testing.T) {
	for _, mode := range killPathsForTest(t) {
		t.Run(string(mode), func(t *testing.T) {
			if mode != ModePgid {
				requireCgroupMode(t, mode)
			}
			l := newTestLauncher(t, mode)
			for i := 0; i < 5; i++ {
				h := launch(t, l, uniqueAppName("spawnrace"))
				start := time.Now()
				if err := h.Stop(); err != nil {
					t.Fatalf("stop: %v", err)
				}
				if elapsed := time.Since(start); elapsed >= testKillTimeout {
					t.Fatalf("stop right after launch took %v (>= kill_timeout %v): graceful signal was lost", elapsed, testKillTimeout)
				}
			}
		})
	}
}
