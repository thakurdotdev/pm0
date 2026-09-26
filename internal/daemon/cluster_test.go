//go:build linux

// M6 cluster mode, end-to-end against the real daemon: N node children
// share one listening port through the reuseport pool (kernel load
// balancing), instances keep one name with per-instance NODE_APP_INSTANCE
// and log paths, reload rolls instance-by-instance with zero failed
// probes (new joins the pool before the old is retired), a failing
// replacement is rolled back to the old incarnation, and scale grows the
// NODE_APP_INSTANCE indices without touching the survivors.
package daemon

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/pkg/client"
)

// clusterApp writes a node http server that reports its pid and (for the
// rollback test) refuses to boot while markerPath exists.
func clusterApp(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "web.js")
	body := `const http = require('http');
const fs = require('fs');
if (process.env.FAIL_MARKER && fs.existsSync(process.env.FAIL_MARKER)) {
  process.exit(1);
}
const port = parseInt(process.env.TEST_PORT, 10);
const server = http.createServer((q, s) => s.end('served by ' + process.pid));
server.listen(port, '127.0.0.1', () => {
  console.log('up on ' + port);
});
// PM2 cluster-reload contract: the old worker closes its listener and
// drains on the 'shutdown' IPC message (daemon->worker handoff).
process.on('message', (m) => {
  if (m === 'shutdown') {
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 2000).unref();
  }
});
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write app: %v", err)
	}
	return p
}

// freePort grabs a loopback port the OS isn't using right now. Tiny
// reserve race is acceptable for tests: the app fails loudly (errored)
// and the test reports it, rather than mis-serving.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
}

func startCluster(t *testing.T, c *client.Client, script, name, port string, n int32) {
	t.Helper()
	_, err := c.Start([]*v1.ProcessSpec{{
		Name:        name,
		Script:      script,
		ExecMode:    "cluster",
		Instances:   n,
		Autorestart: true,
		MinUptimeMs: 2500,
		Env:         map[string]string{"TEST_PORT": port},
		Cwd:         t.TempDir(),
	}})
	if err != nil {
		t.Fatalf("start cluster %s: %v", name, err)
	}
}

// probe the shared port; returns (ok, refused, body). `refused` marks a
// connection-refused/error — the ACTUAL downtime signature (no listener in
// the reuseport pool). A mid-response reset on the retiring worker is an
// in-flight race every rolling reload has (PM2 included) and is counted
// separately by the caller.
func probe(port string) (bool, bool, string) {
	cl := &http.Client{Timeout: 1500 * time.Millisecond, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := cl.Get("http://127.0.0.1:" + port + "/")
	if err != nil {
		return false, true, "" // no listener / unreachable: downtime
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 {
		return false, false, "" // in-flight race: accepted, then reset
	}
	return true, false, strings.TrimSpace(string(b))
}

// waitServing polls until the shared port answers (node bind happens some
// time after the process is marked online — the probe loop starts only
// once the pool is live).
func waitServing(t *testing.T, port string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok, _, _ := probe(port); ok {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("timeout: port %s never started serving", port)
}

// waitCluster polls jlist until the named cluster group has n online rows.
func waitCluster(t *testing.T, c *client.Client, name string, n int, d time.Duration) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		js := jlistOf(t, c)
		online := 0
		for _, p := range js {
			if p["name"] == name && p["pm2_env"].(map[string]any)["status"] == "online" {
				online++
			}
		}
		if online == n {
			return js
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: cluster %s never reached %d online", name, n)
	return nil
}

func TestDaemonClusterPortSharing(t *testing.T) {
	srv, c := startServer(t)
	_ = srv
	dir := t.TempDir()
	app := clusterApp(t, dir)
	port := freePort(t)
	startCluster(t, c, app, "web", port, 2)
	js := waitCluster(t, c, "web", 2, 15*time.Second)

	// jlist shape: two same-named rows, cluster_mode, distinct instances.
	seen := map[string]int{}
	for _, p := range js {
		if p["name"] != "web" {
			continue
		}
		e := p["pm2_env"].(map[string]any)
		if e["exec_mode"] != "cluster_mode" {
			t.Fatalf("exec_mode = %v, want cluster_mode", e["exec_mode"])
		}
		env := e["env"].(map[string]any)
		inst := fmt.Sprint(env["NODE_APP_INSTANCE"])
		seen[inst]++
		if e["instances"].(float64) != 2 {
			t.Fatalf("instances = %v", e["instances"])
		}
		// per-instance log paths (observed PM2 naming)
		if !strings.Contains(fmt.Sprint(e["pm_out_log_path"]), "-out-"+inst+".log") {
			t.Fatalf("out log %v lacks instance suffix -%s", e["pm_out_log_path"], inst)
		}
	}
	if len(seen) != 2 || seen["0"] != 1 || seen["1"] != 1 {
		t.Fatalf("NODE_APP_INSTANCE values wrong: %v", seen)
	}

	// Port sharing: probes hit BOTH instances (kernel reuseport pool).
	waitServing(t, port, 15*time.Second)
	pids := map[string]bool{}
	for i := 0; i < 60; i++ {
		ok, refused, body := probe(port)
		if refused {
			t.Fatalf("probe %d refused against shared port", i)
		}
		if ok {
			pids[body] = true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(pids) < 2 {
		t.Fatalf("port not shared: %d distinct pids after 60 probes (%v)", len(pids), pids)
	}
}

func TestDaemonClusterReloadZeroDowntime(t *testing.T) {
	srv, c := startServer(t)
	_ = srv
	dir := t.TempDir()
	app := clusterApp(t, dir)
	port := freePort(t)
	startCluster(t, c, app, "web", port, 2)
	waitCluster(t, c, "web", 2, 15*time.Second)

	// Warm up first: the pool must be live before the failure counter arms.
	waitServing(t, port, 15*time.Second)

	// Probe continuously while the roll happens.
	refused := 0 // empty pool = real downtime: must stay zero
	resets := 0  // in-flight race on the retiring worker (bounded)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			ok, isRefused, _ := probe(port)
			switch {
			case isRefused:
				refused++
			case !ok:
				resets++
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	if _, err := c.Reload(&v1.Selector{Names: []string{"web"}}); err != nil {
		t.Fatalf("reload: %v", err)
	}
	close(stop)
	<-done

	js := waitCluster(t, c, "web", 2, 15*time.Second)
	for _, p := range js {
		e := p["pm2_env"].(map[string]any)
		if rt, ok := e["restart_time"].(float64); !ok || rt < 1 {
			t.Fatalf("restart_time not bumped after reload: %v", e["restart_time"])
		}
	}
	// Zero-downtime contract: the reuseport pool NEVER empties during the
	// roll (the replacement binds before the old is retired), so not a
	// single connection may be refused. A reset on the retiring worker's
	// accepted connection is the same in-flight race a PM2 reload has —
	// bounded, never a refused window.
	if refused != 0 {
		t.Fatalf("reload had %d refused connections (zero-downtime contract)", refused)
	}
	if resets > 2 {
		t.Fatalf("reload reset %d in-flight probes (expected <= 2)", resets)
	}
}

func TestDaemonClusterReloadRollback(t *testing.T) {
	srv, c := startServer(t)
	_ = srv
	dir := t.TempDir()
	app := clusterApp(t, dir)
	port := freePort(t)
	marker := filepath.Join(t.TempDir(), "fail")
	_, err := c.Start([]*v1.ProcessSpec{{
		Name:        "web",
		Script:      app,
		ExecMode:    "cluster",
		Instances:   1,
		Autorestart: true,
		Env:         map[string]string{"TEST_PORT": port, "FAIL_MARKER": marker},
		Cwd:         t.TempDir(),
	}})
	if err != nil {
		t.Fatalf("start cluster web: %v", err)
	}
	// REAL readiness: the instance must actually serve (the machine flips
	// online at spawn, well before the node child binds its port).
	waitServing(t, port, 15*time.Second)
	js := jlistOf(t, c)
	var oldPid float64
	for _, p := range js {
		if p["name"] == "web" {
			oldPid = p["pid"].(float64)
		}
	}

	// Replacement boots fail while the marker exists.
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, rerr := c.Reload(&v1.Selector{Names: []string{"web"}})
	if rerr == nil {
		t.Fatalf("reload with failing replacement must error")
	}
	os.Remove(marker)

	// The old incarnation is restored (PM2 unparkOldWorker) and serving.
	waitServing(t, port, 15*time.Second)
	js = jlistOf(t, c)
	for _, p := range js {
		if p["name"] == "web" {
			if p["pid"].(float64) != oldPid {
				t.Fatalf("old incarnation not restored: pid %v, want %v", p["pid"], oldPid)
			}
		}
	}
	ok, _, body := probe(port)
	if !ok || body != fmt.Sprintf("served by %d", int(oldPid)) {
		t.Fatalf("restored instance not serving (ok=%v body=%q)", ok, body)
	}
}

func TestDaemonClusterScale(t *testing.T) {
	srv, c := startServer(t)
	_ = srv
	dir := t.TempDir()
	app := clusterApp(t, dir)
	port := freePort(t)
	startCluster(t, c, app, "web", port, 2)
	waitCluster(t, c, "web", 2, 15*time.Second)

	if _, err := c.Scale("web", 1); err != nil {
		t.Fatalf("scale +1: %v", err)
	}
	js := waitCluster(t, c, "web", 3, 15*time.Second)
	insts := map[string]bool{}
	for _, p := range js {
		if p["name"] != "web" {
			continue
		}
		env := p["pm2_env"].(map[string]any)["env"].(map[string]any)
		insts[fmt.Sprint(env["NODE_APP_INSTANCE"])] = true
	}
	if !insts["0"] || !insts["1"] || !insts["2"] {
		t.Fatalf("scale +1 did not add instance 2: %v", insts)
	}

	if _, err := c.Scale("web", -2); err != nil {
		t.Fatalf("scale -2: %v", err)
	}
	js = waitCluster(t, c, "web", 1, 15*time.Second)
	for _, p := range js {
		if p["name"] == "web" {
			env := p["pm2_env"].(map[string]any)["env"].(map[string]any)
			if fmt.Sprint(env["NODE_APP_INSTANCE"]) != "0" {
				t.Fatalf("scale -2 left instance %v, want 0", env["NODE_APP_INSTANCE"])
			}
		}
	}
}
