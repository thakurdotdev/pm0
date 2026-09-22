//go:build linux

// Golden compat suite (docs/compat.md header): scripted scenarios run
// against the REAL pm0 binary + daemon, jlist output normalized and
// diffed against committed fixtures. The fixtures pin the §2 JSON
// contract; any behavioral drift shows up as a fixture diff.
//
// The pm2 side of the golden comparison lives in scripts/golden-pm2.sh
// (runs the same scenarios against real pm2 where a Node runtime + pm2
// are available and applies the same normalizer) — auto-skipped here.
package golden

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var pm0Bin string

func TestMain(m *testing.M) {
	// Self-heal after crashed runs: a test binary that dies on a fatal
	// error (e.g. runtime EAGAIN) skips t.Cleanup and leaks its daemon
	// tree. Its PM0_HOME temp dir is already gone, which is the
	// signature swept below. A live daemon's home always exists, so a
	// real user daemon (~/.pm0) is never touched.
	killOrphanDaemons()
	dir, err := os.MkdirTemp("", "pm0-golden")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "pm0")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/pm0/pm0/cmd/pm0").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build pm0: %v\n%s", err, out)
		os.Exit(1)
	}
	pm0Bin = bin
	os.Exit(m.Run())
}

// scenario is one scripted golden case: steps run CLI commands in order;
// every `snap` step records the normalized jlist under a label.
type scenario struct {
	name   string
	redact bool
}

type runner struct {
	name      string
	t         *testing.T
	bin       string
	home      string
	snaps     []snapshot
	redactCmd *exec.Cmd // explicitly spawned daemon (redact scenario)
}

type snapshot struct {
	Step  string           `json:"step"`
	Jlist []map[string]any `json:"jlist"`
}

// pm0 executes a CLI command; output is checked for exit status only.
func (r *runner) pm0(t *testing.T, args ...string) string {
	t.Helper()
	r.t = t
	cmd := exec.Command(r.bin, args...)
	cmd.Env = append(os.Environ(), "PM0_HOME="+r.home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pm0 %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// snap records the current jlist, normalized.
func (r *runner) snap(t *testing.T, label string) {
	t.Helper()
	out := r.pm0(t, "jlist")
	var js []map[string]any
	if strings.TrimSpace(out) != "[]" {
		if err := json.Unmarshal([]byte(out), &js); err != nil {
			t.Fatalf("jlist parse: %v\n%s", err, out)
		}
	}
	normalize(t, js, r.home)
	for _, p := range js {
		if e, ok := p["pm2_env"].(map[string]any); ok {
			if env, isMap := e["env"].(map[string]any); isMap {
				// The daemon env dump is host-specific: pin its SHAPE, not
				// its contents. The daemon e2e tests assert exact values.
				_ = env
				e["env"] = "<daemon-env>"
			}
		}
	}
	sort.Slice(js, func(i, j int) bool {
		a := js[i]["pm_id"].(float64)
		b := js[j]["pm_id"].(float64)
		return a < b
	})
	r.snaps = append(r.snaps, snapshot{Step: label, Jlist: js})
}

// snapEnv records the jlist with env reduced to the SELECTED keys with
// their real (scrubbed) values — for scenarios that must show values,
// e.g. redaction. Generic snapshots carry env keys only.
func (r *runner) snapEnv(t *testing.T, label, appName string, envKeys ...string) {
	t.Helper()
	out := r.pm0(t, "jlist")
	var js []map[string]any
	if strings.TrimSpace(out) != "[]" {
		if err := json.Unmarshal([]byte(out), &js); err != nil {
			t.Fatalf("jlist parse: %v\n%s", err, out)
		}
	}
	normalize(t, js, r.home)
	var keep []map[string]any
	for _, p := range js {
		if p["name"] != appName {
			continue
		}
		e := p["pm2_env"].(map[string]any)
		if raw, ok := e["env"].(map[string]any); ok {
			filtered := map[string]any{}
			for _, k := range envKeys {
				filtered[k] = raw[k]
			}
			e["env"] = filtered
		}
		keep = append(keep, p)
	}
	r.snaps = append(r.snaps, snapshot{Step: label, Jlist: keep})
}

// waitStatus polls until an app (by base-name family) reaches status.
func (r *runner) waitStatus(t *testing.T, name, status string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		out := r.pm0(t, "jlist")
		var js []map[string]any
		_ = json.Unmarshal([]byte(out), &js)
		for _, p := range js {
			if p["name"].(string) == name {
				if p["pm2_env"].(map[string]any)["status"] == status {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s never reached %s", name, status)
}

func (r *runner) finish(t *testing.T) {
	t.Helper()
	raw, err := json.MarshalIndent(r.snaps, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw) + "\n"
	path := filepath.Join("fixtures", r.name+".json")
	if os.Getenv("PM0_GOLDEN_UPDATE") != "" {
		if err := os.MkdirAll("fixtures", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("fixture updated: %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("fixture missing (%s): regenerate with PM0_GOLDEN_UPDATE=1: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch for %s:\n--- want (fixtures/%s.json)\n+++ got\n%s",
			r.name, r.name, unified(want, []byte(got)))
	}
}

// normalize rewrites volatile fields so runs are comparable across hosts
// and against pm2 (which the shell-side normalizer mirrors):
//
//	pid → 0, monit → 0/0, created_at/pm_uptime → 0, env → sorted key list,
//	absolute paths → home-relative or basename.
func normalize(t *testing.T, js []map[string]any, home string) {
	t.Helper()
	for _, p := range js {
		p["pid"] = 0
		if m, ok := p["monit"].(map[string]any); ok {
			m["memory"] = 0
			m["cpu"] = 0
		}
		e, ok := p["pm2_env"].(map[string]any)
		if !ok {
			continue
		}
		e["created_at"] = 0
		e["pm_uptime"] = 0
		// env is kept as a map; snapshot helpers decide the stable
		// representation (the key set is host-dependent by nature).
		for _, k := range []string{"pm_exec_path", "pm_out_log_path", "pm_err_log_path", "pm_pid_path", "pm_cwd"} {
			if s, ok := e[k].(string); ok {
				e[k] = scrubPath(s, home)
			}
		}
	}
}

// scrubPath makes absolute paths host-independent: state paths become
// home-relative, script paths become basenames.
func scrubPath(s, home string) string {
	if strings.HasPrefix(s, home) {
		return "~" + strings.TrimPrefix(s, home)
	}
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// unified is a minimal line diff for readable failures.
func unified(want, got []byte) string {
	wl := strings.Split(string(want), "\n")
	gl := strings.Split(string(got), "\n")
	var b strings.Builder
	max := len(wl)
	if len(gl) > max {
		max = len(gl)
	}
	for i := 0; i < max; i++ {
		w, g := "", ""
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		switch {
		case w == g:
			fmt.Fprintf(&b, "  %s\n", w)
		default:
			fmt.Fprintf(&b, "- %s\n+ %s\n", w, g)
		}
	}
	return b.String()
}

// findDaemons returns the pids of live processes whose PM0_HOME equals
// home (empty home = any value). The daemon has no pid file of its own, so
// /proc environ inspection is the reliable way to attribute one to a
// state root; the re-exec wrapper preserves the environment, so the
// daemonized process carries PM0_HOME through.
func findDaemons(home string) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		comm, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil || strings.TrimSpace(string(comm)) != "pm0" {
			continue
		}
		env, err := os.ReadFile("/proc/" + e.Name() + "/environ")
		if err != nil {
			continue // vanished mid-scan or not ours
		}
		var got string
		for _, kv := range bytes.Split(env, []byte{0}) {
			if v, ok := bytes.CutPrefix(kv, []byte("PM0_HOME=")); ok {
				got = string(v)
				break
			}
		}
		if home == "" || got == home {
			pids = append(pids, pid)
		}
	}
	return pids
}

// hardKillTree SIGKILLs the daemon's process group and, defensively, the
// pid itself. The daemon setsid'd during startup, so pgid == pid and the
// group covers every app process it spawned; if the group does not exist
// (daemon never setsid'd) the group kill is a harmless ESRCH no-op.
func hardKillTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// killOrphanDaemons sweeps daemons whose PM0_HOME no longer exists —
// leftovers of crashed runs (see TestMain). Group kill reaches their app
// trees; a live run's daemons (existing homes) are never matched.
func killOrphanDaemons() {
	for _, pid := range findDaemons("") {
		env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
		if err != nil {
			continue
		}
		for _, kv := range bytes.Split(env, []byte{0}) {
			if v, ok := bytes.CutPrefix(kv, []byte("PM0_HOME=")); ok {
				if _, statErr := os.Stat(string(v)); statErr != nil {
					hardKillTree(pid)
				}
				break
			}
		}
	}
}

// fakechild builds once per package for scenario scripts.
var (
	fcOnce  sync.Once
	fcPath  string
	fcDir   string
	fcError error
)

func fakechild(t *testing.T) string {
	t.Helper()
	fcOnce.Do(func() {
		fcDir, fcError = os.MkdirTemp("", "golden-fakechild")
		if fcError != nil {
			return
		}
		fcPath = filepath.Join(fcDir, "fakechild")
		out, err := exec.Command("go", "build", "-o", fcPath, "github.com/pm0/pm0/test/fakechild").CombinedOutput()
		if err != nil {
			fcError = fmt.Errorf("build fakechild: %v\n%s", err, out)
		}
	})
	if fcError != nil {
		t.Fatalf("fakechild: %v", fcError)
	}
	return fcPath
}

func newRunner(t *testing.T, s scenario) *runner {
	t.Helper()
	home := t.TempDir()
	r := &runner{name: s.name, bin: pm0Bin, home: home}
	if s.redact {
		// The daemon must come up with --redact-env set: spawn it
		// explicitly (the auto-spawn would run without the flag).
		cmd := exec.Command(pm0Bin, "daemon", "--redact-env")
		cmd.Env = append(os.Environ(), "PM0_HOME="+home)
		if err := cmd.Start(); err != nil {
			t.Fatalf("spawn redact daemon: %v", err)
		}
		r.redactCmd = cmd
	}
	t.Cleanup(func() {
		// Single teardown path, ordered so nothing orphans anything:
		// (1) stop the daemon via RPC — stops apps first (I1). The
		// kill command MUST see the scenario's PM0_HOME: without
		// it, dial resolves the default home's socket and the kill
		// is a silent no-op that leaks the whole daemon tree
		// (observed: 14 leaked processes per suite run before this
		// fix).
		kill := exec.Command(r.bin, "kill")
		kill.Env = append(os.Environ(), "PM0_HOME="+r.home)
		if out, err := kill.CombinedOutput(); err != nil {
			t.Logf("pm0 kill: %v\n%s", err, out)
		}
		// (2) The kill RPC must leave no daemon behind: short exit
		// grace, then fail the test and sweep any survivor's process
		// group (daemon + its app trees).
		deadline := time.Now().Add(time.Second)
		for {
			survivors := findDaemons(r.home)
			if len(survivors) == 0 {
				break
			}
			if time.Now().After(deadline) {
				for _, pid := range survivors {
					t.Errorf("daemon pid %d survived `pm0 kill`; hard-killed group", pid)
					hardKillTree(pid)
				}
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		// (3) Reap the explicitly spawned redact daemon. After a
		// successful RPC kill this is corpse cleanup; if the RPC
		// path failed, the group sweep above already took its tree.
		if r.redactCmd != nil {
			_ = r.redactCmd.Process.Kill()
			_ = r.redactCmd.Wait()
		}
	})
	return r
}
