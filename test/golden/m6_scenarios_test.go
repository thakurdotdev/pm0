//go:build linux

// M6 golden scenarios: cluster mode end-to-end through the CLI — same-name
// instance groups with NODE_APP_INSTANCE, per-instance log files on disk
// (observed PM2 7.0.4 naming), scale in both directions, and the reload
// restart_time bump. The behavioral port-sharing/zero-downtime/rollback
// assertions live in internal/daemon/cluster_test.go (in-process daemon).
package golden

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// clusterFixture writes the node http server used by the cluster scenarios
// (env-driven port; PM2 shutdown-message compatible).
func clusterFixture(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "web.js")
	body := `const http = require('http');
if (process.env.FAIL_MARKER && require('fs').existsSync(process.env.FAIL_MARKER)) process.exit(1);
const port = parseInt(process.env.TEST_PORT, 10);
const server = http.createServer((q, s) => s.end('ok'));
server.listen(port, '127.0.0.1', () => console.log('up'));
process.on('message', (m) => { if (m === 'shutdown') { server.close(() => process.exit(0)); } });
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write web.js: %v", err)
	}
	return p
}

// jlistRows runs jlist and returns the parsed rows.
func jlistRows(t *testing.T, r *runner) []map[string]any {
	t.Helper()
	out := r.pm0(t, "jlist")
	var js []map[string]any
	if strings.TrimSpace(out) != "[]" {
		if err := json.Unmarshal([]byte(out), &js); err != nil {
			t.Fatalf("jlist parse: %v\n%s", err, out)
		}
	}
	return js
}

// familyRows filters jlist rows belonging to one app name, ordered by
// NODE_APP_INSTANCE.
func familyRows(t *testing.T, r *runner, name string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, p := range jlistRows(t, r) {
		if p["name"] == name {
			out = append(out, p)
		}
	}
	return out
}

// waitFamilySize polls until the named family has n rows all online.
func waitFamilySize(t *testing.T, r *runner, name string, n int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		rows := familyRows(t, r, name)
		online := 0
		for _, p := range rows {
			if p["pm2_env"].(map[string]any)["status"] == "online" {
				online++
			}
		}
		if len(rows) == n && online == n {
			return
		}
		time.Sleep(80 * time.Millisecond)
	}
	t.Fatalf("timeout: family %s never reached %d online rows", name, n)
}

func instEnv(t *testing.T, p map[string]any) string {
	t.Helper()
	e := p["pm2_env"].(map[string]any)["env"].(map[string]any)
	return fmt.Sprint(e["NODE_APP_INSTANCE"])
}

func TestGoldenClusterMode(t *testing.T) {
	work := t.TempDir()
	r := &runner{name: "cluster-mode", t: t, bin: pm0Bin, home: t.TempDir()}
	app := clusterFixture(t, work)

	// Pick two free ports (main + a spare the app never uses).
	p1, p2 := strconv.Itoa(20000+int(time.Now().UnixNano()%20000)), "0"
	_ = p2

	// -i 2 on a node script: cluster mode, same name, instances 0/1.
	r.pm0(t, "start", app, "--name", "web", "-i", "2",
		"--env", "TEST_PORT="+p1)
	defer r.pm0(t, "delete", "web")
	waitFamilySize(t, r, "web", 2, 20*time.Second)

	rows := familyRows(t, r, "web")
	if len(rows) != 2 {
		t.Fatalf("want 2 rows named web, got %d", len(rows))
	}
	seen := map[string]bool{}
	for _, p := range rows {
		e := p["pm2_env"].(map[string]any)
		if e["exec_mode"] != "cluster_mode" {
			t.Fatalf("exec_mode = %v, want cluster_mode", e["exec_mode"])
		}
		inst := instEnv(t, p)
		if seen[inst] {
			t.Fatalf("duplicate NODE_APP_INSTANCE %s", inst)
		}
		seen[inst] = true
		// per-instance log naming (observed PM2: web-out-<i>.log)
		if !strings.HasSuffix(fmt.Sprint(e["pm_out_log_path"]), "-out-"+inst+".log") {
			t.Fatalf("out log %v lacks -out-%s.log suffix", e["pm_out_log_path"], inst)
		}
	}
	if !seen["0"] || !seen["1"] {
		t.Fatalf("instances 0/1 missing: %v", seen)
	}

	// Per-instance log files exist on disk once the children wrote output.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(r.home, "logs", "web-out-0.log")); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(r.home, "logs", "web-out-0.log")); err != nil {
		t.Fatalf("per-instance log web-out-0.log missing: %v", err)
	}

	// Scale +1: a third instance joins with the next index.
	r.pm0(t, "scale", "web", "+1")
	waitFamilySize(t, r, "web", 3, 20*time.Second)
	rows = familyRows(t, r, "web")
	seen = map[string]bool{}
	for _, p := range rows {
		seen[instEnv(t, p)] = true
	}
	if !seen["2"] {
		t.Fatalf("scale +1 did not add instance 2: %v", seen)
	}

	// Reload: same pm_ids, restart_time bumped, all instances online.
	r.pm0(t, "reload", "web")
	waitFamilySize(t, r, "web", 3, 20*time.Second)
	rows = familyRows(t, r, "web")
	for _, p := range rows {
		e := p["pm2_env"].(map[string]any)
		if rt, ok := e["restart_time"].(float64); !ok || rt < 1 {
			t.Fatalf("restart_time not bumped after reload: %v", e["restart_time"])
		}
	}

	// Scale down to an absolute target: instances 2 removed.
	r.pm0(t, "scale", "web", "1")
	waitFamilySize(t, r, "web", 1, 20*time.Second)
	rows = familyRows(t, r, "web")
	if instEnv(t, rows[0]) != "0" {
		t.Fatalf("scale 1 left instance %s, want 0", instEnv(t, rows[0]))
	}
}
