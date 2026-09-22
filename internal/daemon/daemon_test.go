//go:build linux

// End-to-end tests for the daemon control plane: a real Server bound to a
// real socket in a temp PM0_HOME, exercised through the client library
// with real fakechild processes.
//
// UpdateDaemon and KillDaemon are NOT exercised here — syscall.Exec would
// replace the test process image and os.Exit would kill it. Both flows are
// covered by the manual smoke procedure and the golden scripts (which run
// the real binary).
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/proc"
	"github.com/pm0/pm0/internal/store"
	"github.com/pm0/pm0/pkg/client"
	"github.com/pm0/pm0/test/harness"
)

var fakechild string

func TestMain(m *testing.M) {
	bin, cleanup, err := harness.BuildFakechild()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer cleanup()
	fakechild = bin
	harness.SetFakechildPath(bin)
	os.Exit(m.Run())
}

// startServer boots a daemon Server on a fresh home and returns a client.
func startServer(t *testing.T, redact ...bool) (*Server, *client.Client) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PM0_HOME", home)
	if err := store.EnsureHome(); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	l, err := proc.NewLauncher(proc.ModeAuto)
	if err != nil {
		t.Fatalf("launcher: %v", err)
	}
	srv := New(l, "test", "testing", len(redact) > 0 && redact[0])
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Run() }()

	c, err := client.Dial("")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, perr := c.Ping(); perr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not come up: %v", perr2(errCh))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Cleanup(func() {
		_ = c.Close()
		// Kill the in-process listener by stopping it through the server's
		// own shutdown of registered apps; the socket file dies with the
		// temp dir.
		for _, v := range srv.sup.List() {
			_ = srv.sup.Stop(fmt.Sprint(v.ID))
		}
	})
	return srv, c
}

func perr2(ch chan error) error {
	select {
	case err := <-ch:
		return err
	default:
		return fmt.Errorf("ping timeout")
	}
}

// startViaClient starts fakechild with args and returns its pm_id.
func startViaClient(t *testing.T, c *client.Client, name string, args ...string) int32 {
	t.Helper()
	resp, err := c.Start([]*v1.ProcessSpec{{
		Name:        name,
		Script:      fakechild,
		Args:        args,
		Autorestart: true, // CLI sends pinned defaults; daemon copies booleans
		Cwd:         t.TempDir(),
	}})
	if err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	if len(resp.GetProcesses()) != 1 {
		t.Fatalf("start %s: got %d processes", name, len(resp.GetProcesses()))
	}
	return resp.GetProcesses()[0].GetPmId()
}

func jlistOf(t *testing.T, c *client.Client) []map[string]any {
	t.Helper()
	resp, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var r client.JlistRenderer
	raw, err := r.RenderAll(resp.GetProcesses())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("jlist json: %v\n%s", err, raw)
	}
	return out
}

// waitFor polls until cond holds (jlist has exactly one app in status).
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestDaemonStartStopDeleteLifecycle(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("dlc")
	id := startViaClient(t, c, name)

	waitFor(t, 5*time.Second, "app online", func() bool {
		v, ok := c.Describe(&v1.Selector{PmIds: []int32{id}})
		return ok == nil && v.GetProcess().GetStatus() == v1.ProcessStatus_PROCESS_STATUS_ONLINE
	})

	if _, err := c.Stop(&v1.Selector{PmIds: []int32{id}}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	waitFor(t, 5*time.Second, "app stopped", func() bool {
		v, err := c.Describe(&v1.Selector{PmIds: []int32{id}})
		return err == nil && v.GetProcess().GetStatus() == v1.ProcessStatus_PROCESS_STATUS_STOPPED
	})

	// Stop is idempotent (pm2 semantics).
	if _, err := c.Stop(&v1.Selector{PmIds: []int32{id}}); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}

	if _, err := c.Delete(&v1.Selector{PmIds: []int32{id}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp, err := c.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(resp.GetProcesses()) != 0 {
		t.Fatalf("delete left %d apps", len(resp.GetProcesses()))
	}
}

// TestDaemonJlistSchema pins the §2 JSON contract: key set, null exit_code
// while running, [] (not null) args, monit shape.
func TestDaemonJlistSchema(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("jschema")
	startViaClient(t, c, name, "--exit-after", "60000")

	waitFor(t, 5*time.Second, "app online", func() bool {
		for _, p := range jlistOf(t, c) {
			if p["name"] == name {
				return p["pm2_env"].(map[string]any)["status"] == "online"
			}
		}
		return false
	})

	js := jlistOf(t, c)
	var mine map[string]any
	for _, p := range js {
		if p["name"] == name {
			mine = p
		}
	}
	if mine == nil {
		t.Fatal("app missing from jlist")
	}

	// Top-level keys: exactly { pid, pm_id, name, monit, pm2_env } (§2.1).
	topKeys := make([]string, 0, 5)
	for k := range mine {
		topKeys = append(topKeys, k)
	}
	if len(topKeys) != 5 {
		t.Fatalf("top-level keys = %v, want exactly 5", topKeys)
	}

	env := mine["pm2_env"].(map[string]any)
	// §2.2 required keys.
	required := []string{
		"name", "namespace", "pm_id", "pm_exec_path", "args", "pm_cwd",
		"pm_out_log_path", "pm_err_log_path", "pm_pid_path", "interpreter",
		"interpreter_args", "exec_mode", "instances", "autorestart",
		"max_restarts", "min_uptime", "max_memory_restart", "kill_signal",
		"kill_timeout", "wait_ready", "listen_timeout", "cron_restart",
		"watch", "exp_backoff_restart_delay", "stop_exit_codes", "env", "status", "restart_time",
		"unstable_restarts", "created_at", "pm_uptime", "exit_code",
		"treekill", "time",
	}
	for _, k := range required {
		if _, ok := env[k]; !ok {
			t.Errorf("pm2_env missing key %q", k)
		}
	}
	if env["exit_code"] != nil {
		t.Errorf("exit_code = %v while running, want null", env["exit_code"])
	}
	if env["treekill"] != true {
		t.Errorf("treekill = %v, want true (row 23 divergence)", env["treekill"])
	}
	argsJSON, _ := json.Marshal(env["args"])
	if string(argsJSON) == "null" {
		t.Error("args must be [] not null")
	}
	if env["status"] != "online" {
		t.Errorf("status = %v, want online", env["status"])
	}
	monit := mine["monit"].(map[string]any)
	if mem, ok := monit["memory"].(float64); !ok || mem <= 0 {
		t.Errorf("monit.memory = %v, want > 0 for a live tree", monit["memory"])
	}
}

// TestDaemonCrashLoopErrored: unstable restarts hit max_restarts → status
// errored, no further restarts.
func TestDaemonCrashLoopErrored(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("crash")
	resp, err := c.Start([]*v1.ProcessSpec{{
		Name:        name,
		Script:      fakechild,
		Args:        []string{"--exit-after", "50", "--exit-code", "7"},
		Autorestart: true,
		MaxRestarts: 3,
		MinUptimeMs: 5000,
		Cwd:         t.TempDir(),
	}})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	id := resp.GetProcesses()[0].GetPmId()

	waitFor(t, 10*time.Second, "errored after crash loop", func() bool {
		v, err := c.Describe(&v1.Selector{PmIds: []int32{id}})
		return err == nil && v.GetProcess().GetStatus() == v1.ProcessStatus_PROCESS_STATUS_ERRORED
	})
	v, _ := c.Describe(&v1.Selector{PmIds: []int32{id}})
	env := v.GetProcess().GetPm2Env()
	// Observed pm2: parking errored RESETS unstable_restarts to 0 and nulls
	// created_at (God.handleExit) — the display never shows the counter
	// that tripped the budget.
	if env.GetUnstableRestarts() != 0 {
		t.Errorf("unstable_restarts = %d, want 0 (errored park resets it)", env.GetUnstableRestarts())
	}
	if env.GetCreatedAtPresent() {
		t.Errorf("created_at present after errored park, want null (observed pm2)")
	}
	if env.GetRestartTime() != 2 {
		t.Errorf("restart_time = %d, want 2 (initial + 2 relaunches; 3rd crash parks)", env.GetRestartTime())
	}
	if !env.GetExitCodePresent() || env.GetExitCode() != 7 {
		t.Errorf("exit_code = %d present=%v, want 7", env.GetExitCode(), env.GetExitCodePresent())
	}
}

// TestDaemonInstancesConsecutiveIds: instances=2 → app-0/app-1, ids n, n+1.
func TestDaemonInstancesConsecutiveIds(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("inst")
	startViaClient(t, c, name, "--exit-after", "60000")
	// Second app with 2 instances: ids must be 1 and 2 (consecutive).
	if _, err := c.Start([]*v1.ProcessSpec{{
		Name:        name + "-fam",
		Script:      fakechild,
		Args:        []string{"--exit-after", "60000"},
		Autorestart: true,
		Instances:   2,
		Cwd:         t.TempDir(),
	}}); err != nil {
		t.Fatalf("start family: %v", err)
	}

	waitFor(t, 5*time.Second, "family online", func() bool {
		ps := jlistOf(t, c)
		found := 0
		for _, p := range ps {
			n := p["name"].(string)
			if n == name+"-fam-0" || n == name+"-fam-1" {
				found++
			}
		}
		return found == 2
	})

	ids := map[string]int64{}
	for _, p := range jlistOf(t, c) {
		ids[p["name"].(string)] = int64(p["pm_id"].(float64))
	}
	if ids[name+"-fam-1"] != ids[name+"-fam-0"]+1 {
		t.Fatalf("family ids not consecutive: %v", ids)
	}

	// pm2 semantics: stopping the BASE name stops the whole family.
	if _, err := c.Stop(&v1.Selector{Names: []string{name + "-fam"}}); err != nil {
		t.Fatalf("family stop: %v", err)
	}
	waitFor(t, 5*time.Second, "family stopped", func() bool {
		for _, p := range jlistOf(t, c) {
			n := p["name"].(string)
			if n == name+"-fam-0" || n == name+"-fam-1" {
				if p["pm2_env"].(map[string]any)["status"] != "stopped" {
					return false
				}
			}
		}
		return true
	})
}

// TestDaemonScale grows and shrinks a family.
func TestDaemonScale(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("scale")
	startViaClient(t, c, name, "--exit-after", "60000")

	if _, err := c.Scale(name, 2); err != nil {
		t.Fatalf("scale +2: %v", err)
	}
	waitFor(t, 5*time.Second, "3 instances", func() bool {
		return countFamily(jlistOf(t, c), name) == 3
	})

	if _, err := c.Scale(name, -1); err != nil {
		t.Fatalf("scale -1: %v", err)
	}
	waitFor(t, 5*time.Second, "2 instances", func() bool {
		return countFamily(jlistOf(t, c), name) == 2
	})
}

func countFamily(js []map[string]any, base string) int {
	n := 0
	for _, p := range js {
		if name := p["name"].(string); name == base || isInstanceOf(name, base) {
			n++
		}
	}
	return n
}

// TestDaemonSaveResurrect: save → a FRESH server adopts the same pm_ids
// and the same trees survive (kill --force path) or start fresh.
func TestDaemonSaveResurrect(t *testing.T) {
	srv, c := startServer(t)
	name := harness.UniqueName("persist")
	id := startViaClient(t, c, name, "--exit-after", "600000")

	if _, err := c.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Dump exists and mentions the app.
	raw, err := os.ReadFile(store.DumpPath())
	if err != nil {
		t.Fatalf("dump missing: %v", err)
	}
	if !strings.Contains(string(raw), name) {
		t.Fatal("dump lacks the app name")
	}

	// Simulate a fresh daemon image: new Server, same home. Resurrect must
	// adopt the still-running tree at the SAME pm_id.
	_ = srv
	l, err := proc.NewLauncher(proc.ModeAuto)
	if err != nil {
		t.Fatalf("relauncher: %v", err)
	}
	srv2 := New(l, "test", "testing", false)
	started, errored := srv2.adoptOrStartFromDump()
	if started != 1 || errored != 0 {
		t.Fatalf("resurrect adopted=%d errored=%d, want 1/0", started, errored)
	}
	v := srv2.sup.List()
	if len(v) != 1 || int32(v[0].ID) != id {
		t.Fatalf("registry after adopt: %+v (want pm_id %d)", v, id)
	}
	if v[0].Runtime.Status != "online" {
		t.Fatalf("adopted app status %q, want online", v[0].Runtime.Status)
	}
	if v[0].Runtime.Pid == 0 {
		t.Fatal("adopted app has no pid")
	}
	// Adopted trees are real: stopping them empties them (I1 — Stop
	// verifies emptiness and errors on survivors).
	if err := srv2.sup.Stop(fmt.Sprint(v[0].ID)); err != nil {
		t.Fatalf("stop adopted: %v", err)
	}
}

// TestDaemonRedactEnv: secret-looking env values are [redacted] in jlist
// when the daemon runs with --redact-env (§4).
func TestDaemonRedactEnv(t *testing.T) {
	_, c := startServer(t, true)
	name := harness.UniqueName("redact")
	secret := "super-secret-value"
	if _, err := c.Start([]*v1.ProcessSpec{{
		Name:        name,
		Script:      fakechild,
		Args:        []string{"--exit-after", "60000"},
		Autorestart: true,
		Env:         map[string]string{"AWS_SECRET_ACCESS_KEY": secret, "PLAIN": visible},
		Cwd:         t.TempDir(),
	}}); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitFor(t, 5*time.Second, "app online", func() bool {
		for _, p := range jlistOf(t, c) {
			if p["name"] == name {
				return p["pm2_env"].(map[string]any)["status"] == "online"
			}
		}
		return false
	})

	for _, p := range jlistOf(t, c) {
		if p["name"] != name {
			continue
		}
		env := p["pm2_env"].(map[string]any)["env"].(map[string]any)
		if env["AWS_SECRET_ACCESS_KEY"] == secret {
			t.Fatal("secret leaked into jlist env")
		}
		if env["AWS_SECRET_ACCESS_KEY"] != "[redacted]" {
			t.Fatalf("redacted value = %v", env["AWS_SECRET_ACCESS_KEY"])
		}
		if env["PLAIN"] != visible {
			t.Fatalf("non-secret value mangled: %v", env["PLAIN"])
		}
	}
}

const visible = "plain-value"

// TestDaemonLogsStream: script output reaches the stream, backlog first.
func TestDaemonLogsStream(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("loggy")
	script := filepath.Join(t.TempDir(), "loggy.sh")
	body := "#!/bin/sh\necho out-line-1\necho err-line-1 >&2\nsleep 30\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start([]*v1.ProcessSpec{{
		Name:        name,
		Script:      script,
		Autorestart: true,
		Cwd:         t.TempDir(),
	}}); err != nil {
		t.Fatalf("start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	lines, errCh, err := c.StreamLogs(ctx, &v1.Selector{Names: []string{name}}, 10, true, true, true)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	got := map[string]int{}
	deadline := time.After(6 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				goto done
			}
			got[string(line.GetData())]++
			if _, ok2 := got["out-line-1"]; ok2 && got["err-line-1"] > 0 {
				goto done
			}
		case e := <-errCh:
			if e != nil && !strings.Contains(e.Error(), "EOF") && !strings.Contains(e.Error(), "context") {
				t.Fatalf("stream error: %v", e)
			}
			goto done
		case <-deadline:
			t.Fatalf("log lines incomplete: %v", got)
		}
	}
done:
	if got["out-line-1"] == 0 || got["err-line-1"] == 0 {
		t.Fatalf("missing lines: %v", got)
	}
}

// TestDaemonReloadRolls: reload restarts (pid changes for fork instances).
func TestDaemonReloadRolls(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("roll")
	startViaClient(t, c, name, "--exit-after", "600000")
	waitFor(t, 5*time.Second, "online", func() bool {
		for _, p := range jlistOf(t, c) {
			if p["name"] == name {
				return p["pm2_env"].(map[string]any)["status"] == "online"
			}
		}
		return false
	})
	before := pidOf(jlistOf(t, c), name)

	if _, err := c.Reload(&v1.Selector{Names: []string{name}}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	waitFor(t, 5*time.Second, "restarted with new pid", func() bool {
		p := pidOf(jlistOf(t, c), name)
		return p != 0 && p != before
	})
}

func pidOf(js []map[string]any, name string) int64 {
	for _, p := range js {
		if p["name"].(string) == name {
			return int64(p["pid"].(float64))
		}
	}
	return 0
}

// TestDaemonDescribeMatchesLowestId: describe on a family returns the
// lowest pm_id instance.
func TestDaemonDescribeMatchesLowestId(t *testing.T) {
	_, c := startServer(t)
	name := harness.UniqueName("fam2")
	if _, err := c.Start([]*v1.ProcessSpec{{
		Name:        name,
		Script:      fakechild,
		Args:        []string{"--exit-after", "60000"},
		Autorestart: true,
		Instances:   3,
		Cwd:         t.TempDir(),
	}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, 5*time.Second, "3 instances", func() bool {
		return countFamily(jlistOf(t, c), name) == 3
	})
	v, err := c.Describe(&v1.Selector{Names: []string{name}})
	if err != nil {
		t.Fatalf("describe family: %v", err)
	}
	if v.GetProcess().GetName() != name+"-0" {
		t.Fatalf("describe returned %q, want %q", v.GetProcess().GetName(), name+"-0")
	}
}
