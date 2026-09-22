//go:build linux

// M4 golden scenarios: ecosystem-file starts (--env/--only/relative paths),
// loud ecosystem errors (divergence 2), the rotation knob end-to-end, and
// `startup --print`. These complement the committed-fixture scenarios in
// scenarios_test.go by asserting live state that snapshots cannot capture
// (paths, env values, rotated files on disk).
package golden

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ecoFixture writes an ecosystem file plus its scripts into dir and returns
// the ecosystem path. Two apps: web (long-running sh loop) and api (also
// long-running; used for the base-env check).
func ecoFixture(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatalf("mkdir svc: %v", err)
	}
	write := func(name, body string) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	write("svc/server.sh", "#!/bin/sh\nwhile :; do sleep 1; done\n")
	write("svc/worker.sh", "#!/bin/sh\nwhile :; do sleep 1; done\n")
	eco := `module.exports = {
  apps: [
    {
      name: 'web',
      script: 'svc/server.sh',
      cwd: '.',
      env: { SMOKE_ENV: 'base', BASE_ONLY: 'yes' },
      env_production: { SMOKE_ENV: 'production', PORT: '8080' },
    },
    {
      name: 'api',
      script: 'svc/worker.sh',
      interpreter: 'none',
    },
  ],
};
`
	write("ecosystem.config.js", eco)
	return filepath.Join(dir, "ecosystem.config.js")
}

// jlistOf parses `pm0 jlist` from a runner into a name → pm2_env map.
func jlistOf(t *testing.T, r *runner) map[string]map[string]any {
	t.Helper()
	out := r.pm0(t, "jlist")
	var apps []struct {
		Name   string         `json:"name"`
		Pm2Env map[string]any `json:"pm2_env"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &apps); err != nil {
		t.Fatalf("jlist parse: %v\n%s", err, out)
	}
	m := make(map[string]map[string]any, len(apps))
	for _, a := range apps {
		m[a.Name] = a.Pm2Env
	}
	return m
}

func TestGoldenEcosystemStart(t *testing.T) {
	work := t.TempDir()
	eco := ecoFixture(t, work)
	r := newRunner(t, scenario{name: "ecosystem-start"})

	r.pm0(t, "start", eco, "--env", "production", "--only", "web")
	r.waitStatus(t, "web", "online", 10*time.Second)

	env := jlistOf(t, r)
	web := env["web"]
	if web == nil {
		t.Fatalf("web missing from jlist")
	}
	// Relative paths resolve against the ecosystem file's directory.
	// (Without an explicit cwd key, row 9 applies: caller cwd — same as
	// script starts; the fixture pins cwd: '.' to exercise resolution.)
	if got := web["pm_cwd"]; got != work {
		t.Errorf("pm_cwd = %v, want %v", got, work)
	}
	if got := web["pm_exec_path"]; got != filepath.Join(work, "svc/server.sh") {
		t.Errorf("pm_exec_path = %v, want %v", got, filepath.Join(work, "svc/server.sh"))
	}
	// --env production merged env_production over env.
	if got := web["env"].(map[string]any); got["SMOKE_ENV"] != "production" || got["PORT"] != "8080" || got["BASE_ONLY"] != "yes" {
		t.Errorf("merged env wrong: %v", got)
	}
	// The unselected app must not have started.
	if _, ok := env["api"]; ok {
		t.Errorf("--only web must not start api")
	}

	// A second start without --env uses the base env.
	r.pm0(t, "start", eco, "--only", "api")
	r.waitStatus(t, "api", "online", 10*time.Second)
	env = jlistOf(t, r)
	if got := env["api"]["env"].(map[string]any); len(got) == 0 || got["SMOKE_ENV"] != nil {
		t.Errorf("base env must carry env only: %v", got)
	}
}

func TestGoldenEcosystemErrors(t *testing.T) {
	work := t.TempDir()
	eco := ecoFixture(t, work)
	r := newRunner(t, scenario{name: "ecosystem-errors"})

	// Unknown keys are a hard error listing the supported set (divergence 2).
	bad := filepath.Join(work, "bad.config.js")
	if err := os.WriteFile(bad, []byte(`module.exports={apps:[{name:'b',script:'svc/server.sh',maxMemry:1}]};`), 0o755); err != nil {
		t.Fatal(err)
	}
	out := r.pm0Fail(t, "start", bad)
	if !strings.Contains(out, `unknown key "maxMemry"`) {
		t.Errorf("unknown-key error missing:\n%s", out)
	}

	// A while(1) config times out instead of hanging the CLI.
	loop := filepath.Join(work, "loop.config.js")
	if err := os.WriteFile(loop, []byte(`while(true){}`), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	out = r.pm0Fail(t, "start", loop)
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("sandbox timeout took %s (must be bounded)", d)
	}
	if !strings.Contains(out, "evaluation exceeded") {
		t.Errorf("timeout error missing:\n%s", out)
	}

	// --only mismatch.
	out = r.pm0Fail(t, "start", eco, "--only", "nope")
	if !strings.Contains(out, "matched none") {
		t.Errorf("--only mismatch error missing:\n%s", out)
	}
}

// pm0Fail runs a command that MUST fail; returns the combined output.
func (r *runner) pm0Fail(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command(r.bin, args...)
	cmd.Env = append(os.Environ(), "PM0_HOME="+r.home)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("command unexpectedly succeeded: %v\n%s", args, out)
	}
	return string(out)
}

func TestGoldenLogRotateKnob(t *testing.T) {
	fc := fakechild(t)
	s := scenario{name: "log-rotate-knob"}
	r := newRunner(t, s)

	// 4 KB threshold + a 30000-line flood: rotation must fire well
	// before the 10 MiB default would, proving the knob reaches the
	// route (wiring, not mechanics — mechanics are logbus unit tests).
	r.pm0(t, "start", fc, "--name", "rotflood", "--log-rotate-max", "4KB", "--", "--flood", "30000", "--exit-after", "600000")
	r.waitStatus(t, "rotflood", "online", 10*time.Second)

	dir := filepath.Join(r.home, "logs")
	deadline := time.Now().Add(20 * time.Second)
	rotated := 0
	for {
		m, _ := filepath.Glob(filepath.Join(dir, "rotflood-out.log.2*"))
		rotated = len(m)
		if rotated >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no rotated copy appeared within 20s (knob not wired?)")
		}
		time.Sleep(200 * time.Millisecond)
	}
	// The live sink stays small under copytruncate rotation.
	live := filepath.Join(dir, "rotflood-out.log")
	if st, err := os.Stat(live); err == nil && st.Size() > 1<<20 {
		t.Errorf("live sink grew to %d bytes despite 4KB rotation", st.Size())
	}
	r.pm0(t, "delete", "rotflood")
}

func TestGoldenStartupPrint(t *testing.T) {
	r := newRunner(t, scenario{name: "startup-print"})
	out := r.pm0(t, "startup", "--print")
	if !strings.Contains(out, "ExecStart=") || !strings.Contains(out, " resurrect") {
		t.Errorf("unit missing resurrect ExecStart:\n%s", out)
	}
	if !strings.Contains(out, "PM0_HOME="+r.home) {
		t.Errorf("unit must pin the effective PM0_HOME (%s):\n%s", r.home, out)
	}
	if !strings.Contains(out, "systemctl enable pm0-") {
		t.Errorf("install commands missing:\n%s", out)
	}
}
