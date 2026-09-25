//go:build linux

// Golden scenarios. Each test = one scripted lifecycle against the real
// binary; snapshots are compared against test/golden/fixtures/<name>.json.
// Regenerate fixtures with: PM0_GOLDEN_UPDATE=1 go test ./test/golden/
package golden

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestGoldenStartStop: start (online, exit null) → stop (stopped, exit
// recorded via the graceful kill path).
func TestGoldenStartStop(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "start-stop"}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "web", "--", "--exit-after", "600000")
	r.waitStatus(t, "web", "online", 10*time.Second)
	r.snap(t, "after-start")
	r.pm0(t, "stop", "web")
	r.waitStatus(t, "web", "stopped", 10*time.Second)
	r.snap(t, "after-stop")
	r.finish(t)
}

// TestGoldenCrashLoop: --max-restarts 2 with long min_uptime → errored
// with pinned restart counters.
func TestGoldenCrashLoop(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "crash-loop"}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "crasher",
		"--max-restarts", "2", "--min-uptime", "5000",
		"--", "--exit-after", "50", "--exit-code", "3")
	r.waitStatus(t, "crasher", "errored", 15*time.Second)
	r.snap(t, "after-crash-loop")
	r.finish(t)
}

// TestGoldenInstancesScale: -i 2 family, consecutive ids, scale up/down.
func TestGoldenInstancesScale(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "instances-scale"}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "farm", "-i", "2", "--", "--exit-after", "600000")
	r.waitStatus(t, "farm-0", "online", 10*time.Second)
	r.waitStatus(t, "farm-1", "online", 10*time.Second)
	r.snap(t, "two-instances")
	r.pm0(t, "scale", "farm", "+1")
	r.waitStatus(t, "farm-2", "online", 10*time.Second)
	r.snap(t, "scaled-up")
	r.pm0(t, "scale", "farm", "-1")
	time.Sleep(300 * time.Millisecond) // delete completes before snap
	r.snap(t, "scaled-down")
	r.finish(t)
}

// TestGoldenInterpreterEcho: shebang script → interpreter "none"; bare
// .js → interpreter "node" (row 3 policy).
func TestGoldenInterpreterEcho(t *testing.T) {
	fc := fakechild(t)
	_ = fc
	s := scenario{name: "interpreter-echo"}
	r := newRunner(t, s)

	dir := t.TempDir()
	shPath := filepath.Join(dir, "svc.sh")
	if err := os.WriteFile(shPath, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.pm0(t, "start", shPath, "--name", "shellsvc")
	r.waitStatus(t, "shellsvc", "online", 10*time.Second)

	jsPath := filepath.Join(dir, "nodeapp.js")
	if err := os.WriteFile(jsPath, []byte("setTimeout(()=>{},1e6)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.pm0(t, "start", jsPath, "--name", "nodesvc")
	r.waitStatus(t, "nodesvc", "online", 10*time.Second)

	r.snap(t, "both-interpreters")
	r.finish(t)
}

// TestGoldenAutorestartOff: --no-autorestart parks stopped with the exit
// code recorded (row 10).
func TestGoldenAutorestartOff(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "autorestart-off"}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "oneshot", "--no-autorestart",
		"--", "--exit-after", "100", "--exit-code", "5")
	r.waitStatus(t, "oneshot", "stopped", 10*time.Second)
	r.snap(t, "after-exit")
	r.finish(t)
}

// TestGoldenIDReuse: delete frees the pm_id; next start takes the lowest
// unused id (§3.2).
func TestGoldenIDReuse(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "id-reuse"}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "alpha", "--", "--exit-after", "600000")
	r.pm0(t, "start", fc, "--name", "beta", "--", "--exit-after", "600000")
	r.pm0(t, "start", fc, "--name", "gamma", "--", "--exit-after", "600000")
	r.waitStatus(t, "gamma", "online", 10*time.Second)
	r.snap(t, "three-apps")
	r.pm0(t, "delete", "beta")
	time.Sleep(200 * time.Millisecond)
	r.pm0(t, "start", fc, "--name", "delta", "--", "--exit-after", "600000")
	r.waitStatus(t, "delta", "online", 10*time.Second)
	r.snap(t, "after-reuse")
	r.finish(t)
}

// TestGoldenRedactEnv: --redact-env daemon redacts secret-looking keys in
// jlist while plain values survive (§4). This scenario keeps SELECTED env
// values in the snapshot (generic snapshots carry env keys only).
func TestGoldenRedactEnv(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "redact-env", redact: true}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "secretive",
		"--env", "AWS_SECRET_ACCESS_KEY=hunter2",
		"--env", "PLAIN_TEXT=visible",
		"--", "--exit-after", "600000")
	r.waitStatus(t, "secretive", "online", 10*time.Second)
	r.snapEnv(t, "redacted-env", "secretive", "AWS_SECRET_ACCESS_KEY", "PLAIN_TEXT")
	r.finish(t)
}

// TestGoldenRestartCounter: manual restart bumps restart_time (§2.2
// corrected semantics: every relaunch counts).
func TestGoldenRestartCounter(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "restart-counter"}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "manual", "--", "--exit-after", "600000")
	r.waitStatus(t, "manual", "online", 10*time.Second)
	r.pm0(t, "restart", "manual")
	r.waitStatus(t, "manual", "online", 10*time.Second)
	r.pm0(t, "restart", "manual")
	r.waitStatus(t, "manual", "online", 10*time.Second)
	r.snap(t, "after-two-restarts")
	r.finish(t)
}

// TestGoldenLogsBacklog: `pm0 logs -nostream` prints the FULL requested
// backlog. The stream-end signal races the buffered backlog lines on the
// client (the producer sends the error before closing the line channel),
// so the CLI must drain before exiting — regression: `logs -lines 5`
// randomly printed only 2. Inline assertions, no fixture.
func TestGoldenLogsBacklog(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "logs-backlog"}
	r := newRunner(t, s)
	r.pm0(t, "start", fc, "--name", "floods", "--", "--flood", "500", "--exit-after", "600000")
	r.waitStatus(t, "floods", "online", 10*time.Second)

	// The route pump captures asynchronously: poll until the CLI's
	// backlog shows the complete flood (then the assertion below is
	// about CLI completeness, not pump timing).
	deadline := time.Now().Add(15 * time.Second)
	for {
		out := r.pm0(t, "logs", "floods", "-lines", "500", "-nostream", "--out")
		missing := 0
		for i := 0; i < 500; i++ {
			if !strings.Contains(out, fmt.Sprintf("out-%d\n", i)) {
				missing++
			}
		}
		if missing == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("logs backlog incomplete: %d/500 flood lines missing\n%s", missing, out)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
