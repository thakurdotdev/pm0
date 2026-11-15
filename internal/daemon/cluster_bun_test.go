//go:build linux

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/pkg/client"
)

// assertShared waits for n online instances and checks that probes are
// answered by more than one pid (one kernel reuseport pool).
func assertShared(t *testing.T, c *client.Client, name, port string, n int) {
	t.Helper()
	waitCluster(t, c, name, n, 15*time.Second)
	waitServing(t, port, 15*time.Second)
	pids := map[string]bool{}
	for i := 0; i < 80 && len(pids) < 2; i++ {
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
		t.Fatalf("port not shared: %d distinct pids (%v)", len(pids), pids)
	}
	// No instance may have crash-looped on EADDRINUSE on the way there.
	for _, p := range jlistOf(t, c) {
		if p["name"] == name {
			if r := p["pm2_env"].(map[string]any)["restart_time"].(float64); r != 0 {
				t.Fatalf("instance restarted %v times (bind collision?)", r)
			}
		}
	}
}

// Bun cluster mode: every common way a Bun app binds HTTP must join the
// shared reuseport pool through the --preload, in both launch shapes
// (`bun run file` with bun as the script, --interpreter bun, and
// `bun run start` through a package.json script).
func TestDaemonClusterBunPortSharing(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not installed")
	}
	apps := map[string]string{
		"serve.ts": `Bun.serve({ port: Number(process.env.TEST_PORT), hostname: "127.0.0.1",
  fetch() { return new Response("served by " + process.pid) } });`,
		"default.ts": `export default { port: Number(process.env.TEST_PORT), hostname: "127.0.0.1",
  fetch() { return new Response("served by " + process.pid) } };`,
		"nodehttp.js": `require("http").createServer((q, s) => s.end("served by " + process.pid))
  .listen(Number(process.env.TEST_PORT), "127.0.0.1");`,
		// Raw TCP: answer only once the request arrives (a response written
		// on connect is "unsolicited" to Go's HTTP client and flakes).
		"nodenet.js": `const body = "served by " + process.pid;
require("net").createServer((s) => s.once("data", () => s.end("HTTP/1.1 200 OK\r\nContent-Length: " +
  body.length + "\r\nConnection: close\r\n\r\n" + body)))
  .listen(Number(process.env.TEST_PORT), "127.0.0.1");`,
	}
	for file, body := range apps {
		// Launch shapes exercise argv/env plumbing, server styles exercise
		// the preload patches: cover each once rather than the full cross
		// product.
		shapes := []string{"bun-run"}
		if file == "serve.ts" {
			shapes = append(shapes, "interpreter", "pkg-script")
		}
		for _, shape := range shapes {
			t.Run(file+"/"+shape, func(t *testing.T) {
				_, c := startServer(t)
				dir := t.TempDir()
				path := filepath.Join(dir, file)
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				port := freePort(t)
				spec := &v1.ProcessSpec{
					Name:        "bunweb",
					ExecMode:    "cluster",
					Instances:   2,
					Autorestart: true,
					Env:         map[string]string{"TEST_PORT": port},
					Cwd:         dir,
				}
				switch shape {
				case "bun-run":
					spec.Script, spec.Args, spec.Interpreter = bun, []string{"run", path}, "none"
				case "pkg-script":
					// `bun run start`: the server is a CHILD bun of the
					// package.json script, reached only via BUN_OPTIONS.
					pkg := `{"scripts":{"start":"bun ` + file + `"}}`
					if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
						t.Fatal(err)
					}
					spec.Script, spec.Args, spec.Interpreter = bun, []string{"run", "start"}, "none"
				default:
					spec.Script, spec.Interpreter = path, "bun"
				}
				if _, err := c.Start([]*v1.ProcessSpec{spec}); err != nil {
					t.Fatalf("start: %v", err)
				}
				assertShared(t, c, "bunweb", port, 2)
			})
		}
	}
}

// Node cluster apps with their own CLI args: the --require preload must
// sit before the script, not among the app args (it used to land before
// the LAST argv element, so `app.js --flag value` never got the preload
// and every instance after the first hit EADDRINUSE).
func TestDaemonClusterNodeWithAppArgs(t *testing.T) {
	_, c := startServer(t)
	dir := t.TempDir()
	app := clusterApp(t, dir)
	port := freePort(t)
	if _, err := c.Start([]*v1.ProcessSpec{{
		Name:        "webargs",
		Script:      app,
		Args:        []string{"--some-flag", "value"},
		ExecMode:    "cluster",
		Instances:   2,
		Autorestart: true,
		Env:         map[string]string{"TEST_PORT": port},
		Cwd:         t.TempDir(),
	}}); err != nil {
		t.Fatalf("start: %v", err)
	}
	assertShared(t, c, "webargs", port, 2)
}
