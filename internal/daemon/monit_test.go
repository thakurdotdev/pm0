//go:build linux

package daemon

import (
	"testing"
	"time"
)

func TestCPUPercentSameLeader(t *testing.T) {
	now := time.Now()
	last := cpuSample{pid: 100, ticks: 200, at: now.Add(-500 * time.Millisecond)}
	// 50 ticks in 0.5s at USER_HZ=100 => 100% of one core.
	got := cpuPercent(last, 100, 250, now)
	if got < 99.9 || got > 100.1 {
		t.Fatalf("cpu = %f, want ~100", got)
	}
}

func TestCPUPercentLeaderChangeResets(t *testing.T) {
	now := time.Now()
	// Old leader 100 had 200 ticks; new leader 111 reports 999 ticks:
	// a raw delta would report a garbage spike. The guard reports 0 and
	// the caller stores the new sample (converges next round).
	last := cpuSample{pid: 100, ticks: 200, at: now.Add(-100 * time.Millisecond)}
	if got := cpuPercent(last, 111, 999, now); got != 0 {
		t.Fatalf("cpu across leader change = %f, want 0", got)
	}
}

func TestCPUPercentEdgeCases(t *testing.T) {
	now := time.Now()
	// Ticks went backwards (tree restarted under the same leader pid —
	// practically impossible, but the guard must hold).
	last := cpuSample{pid: 5, ticks: 500, at: now.Add(-time.Second)}
	if got := cpuPercent(last, 5, 100, now); got != 0 {
		t.Fatalf("backwards ticks cpu = %f, want 0", got)
	}
	// Zero elapsed time: no division by zero, no cpu.
	last = cpuSample{pid: 5, ticks: 500, at: now}
	if got := cpuPercent(last, 5, 600, now); got != 0 {
		t.Fatalf("zero-dt cpu = %f, want 0", got)
	}
	// First observation ever (zero-value last sample): effectively zero
	// (dt spans from the zero time), converges from the next sample on.
	if got := cpuPercent(cpuSample{}, 7, 600, now); got > 0.001 {
		t.Fatalf("first-sample cpu = %f, want ~0", got)
	}
	// Multi-core workloads exceed 100%: no clamp (pm2 reports raw %).
	last = cpuSample{pid: 9, ticks: 0, at: now.Add(-time.Second)}
	if got := cpuPercent(last, 9, 350, now); got < 349 || got > 351 {
		t.Fatalf("350%% cpu = %f, want ~350", got)
	}
}
