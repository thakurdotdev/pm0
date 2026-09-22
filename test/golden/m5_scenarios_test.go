//go:build linux

// M5 golden scenarios: the ops restart triggers (health checks, watch) and
// the wait_ready gate, exercised end-to-end against the real binary +
// daemon. max_memory_restart (30s cadence) and cron (minute granularity)
// are too slow for the suite — their mechanics are pinned by internal/ops
// and internal/cron unit tests, and the memory path gets a live smoke.
package golden

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitRestartTime polls until the app's restart_time reaches min.
func (r *runner) waitRestartTime(t *testing.T, name string, min int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out := r.pm0(t, "jlist")
		var js []map[string]any
		_ = json.Unmarshal([]byte(out), &js)
		for _, p := range js {
			if p["name"].(string) == name {
				e := p["pm2_env"].(map[string]any)
				if n, ok := e["restart_time"].(float64); ok && int(n) >= min {
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout: %s restart_time never reached %d", name, min)
}

// statusOf returns the app's current status ("" when absent).
func (r *runner) statusOf(t *testing.T, name string) string {
	t.Helper()
	out := r.pm0(t, "jlist")
	var js []map[string]any
	_ = json.Unmarshal([]byte(out), &js)
	for _, p := range js {
		if p["name"].(string) == name {
			return p["pm2_env"].(map[string]any)["status"].(string)
		}
	}
	return ""
}

// Health checks (divergence 7) end-to-end: a probe that always fails with
// retries=2 restarts the app while it stays online (recovered), through
// the ecosystem-file config path.
func TestGoldenHealthCheckRestart(t *testing.T) {
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "serve.sh"),
		[]byte("#!/bin/sh\nwhile :; do sleep 1; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	eco := filepath.Join(work, "health.config.js")
	body := `module.exports = {
  apps: [{
    name: 'healthapp',
    script: 'serve.sh',
    interpreter: 'none',
    health_check_cmd: 'exit 1',
    health_check_interval: '300ms',
    health_check_timeout: '200ms',
    health_check_retries: 2,
  }],
};
`
	if err := os.WriteFile(eco, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	r := newRunner(t, scenario{name: "health-check"})
	r.pm0(t, "start", eco)
	r.waitStatus(t, "healthapp", "online", 10*time.Second)
	// 2 consecutive failures (300ms apart) -> restart; the app relaunches
	// and stays online afterwards.
	r.waitRestartTime(t, "healthapp", 1, 15*time.Second)
	r.waitStatus(t, "healthapp", "online", 5*time.Second)
	// The trigger keeps working while the probe keeps failing.
	r.waitRestartTime(t, "healthapp", 2, 15*time.Second)
	r.pm0(t, "delete", "healthapp")
}

// Watch (rows 21/22) end-to-end: a file change under the app cwd restarts
// the online app; the ignore list keeps node_modules churn silent.
func TestGoldenWatchRestart(t *testing.T) {
	fc := fakechild(t)
	dir := t.TempDir()
	r := newRunner(t, scenario{name: "watch-restart"})

	r.pm0(t, "start", fc, "--name", "watched", "--watch", "--cwd", dir)
	r.waitStatus(t, "watched", "online", 10*time.Second)

	// node_modules churn must NOT restart (ignored subtree, never watched).
	if err := os.MkdirAll(filepath.Join(dir, "node_modules", "dep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "dep", "x.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second) // debounce (500ms) + ops tick + restart margin
	r.pm0(t, "jlist")
	var js []map[string]any
	_ = json.Unmarshal([]byte(r.pm0(t, "jlist")), &js)
	for _, p := range js {
		if p["name"].(string) == "watched" {
			if n := p["pm2_env"].(map[string]any)["restart_time"].(float64); int(n) != 0 {
				t.Fatalf("node_modules churn restarted the app (restart_time=%d)", int(n))
			}
		}
	}

	// A real change restarts.
	if err := os.WriteFile(filepath.Join(dir, "app.conf"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.waitRestartTime(t, "watched", 1, 10*time.Second)
	r.pm0(t, "delete", "watched")
}

// wait_ready (row 19) end-to-end: the gate holds `launching` and the
// child's node-IPC ready frame flips it online well before listen_timeout;
// a silent child is forced online at the deadline (row 18).
func TestGoldenWaitReady(t *testing.T) {
	fc := fakechild(t)
	r := newRunner(t, scenario{name: "wait-ready"})

	// (a) child sends ready: online before the 3s default deadline proves
	// the IPC gate actually unlocked (otherwise only the deadline would).
	r.pm0(t, "start", fc, "--name", "readyok", "--wait-ready", "--", "--send-ready")
	r.waitStatus(t, "readyok", "online", 2*time.Second)

	// (b) child never sends: launching holds past the gate, forced online
	// at listen_timeout (600ms here).
	r.pm0(t, "start", fc, "--name", "readysilent", "--wait-ready",
		"--listen-timeout", "600", "--", "--exit-after", "600000")
	if st := r.statusOf(t, "readysilent"); st != "launching" {
		t.Fatalf("status right after start = %q, want launching", st)
	}
	r.waitStatus(t, "readysilent", "online", 5*time.Second)
	r.pm0(t, "delete", "readyok", "readysilent")
}

// An invalid cron_restart fails the start loudly (compat rule: drift is
// loud at invocation time; M5 changelog item 2).
func TestGoldenCronInvalidFailsStart(t *testing.T) {
	r := newRunner(t, scenario{name: "cron-invalid"})
	out := r.pm0Fail(t, "start", "/bin/true", "--name", "cronbad", "--cron-restart", "61 * * * *")
	if !strings.Contains(out, "cron_restart") {
		t.Errorf("error must mention cron_restart:\n%s", out)
	}
	// Nothing was registered by the failed start.
	if js := r.pm0(t, "jlist"); strings.TrimSpace(js) != "[]" {
		t.Errorf("failed start must not register the app: %s", js)
	}
}
