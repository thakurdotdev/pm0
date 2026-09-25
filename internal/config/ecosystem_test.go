package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func parseOK(t *testing.T, src string, dir string) []*EcosystemApp {
	t.Helper()
	apps, err := ParseEcosystem([]byte(src), dir)
	if err != nil {
		t.Fatalf("ParseEcosystem: %v", err)
	}
	return apps
}

func TestEcosystemAppsArray(t *testing.T) {
	apps := parseOK(t, `
                module.exports = {
                  apps: [
                    {
                      name: 'web',
                      script: './server.js',
                      args: ['--port', '3000'],
                      cwd: './svc',
                      out_file: 'logs/web-out.log',
                      error_file: 'logs/web-err.log',
                      pid_file: 'pids/web.pid',
                      interpreter_args: ['--harmony'],
                      instances: 2,
                      autorestart: false,
                      max_restarts: 3,
                      min_uptime: '5s',
                      restart_delay: 250,
                      exp_backoff_restart_delay: 100,
                      stop_exit_codes: [3, 7],
                      max_memory_restart: '1G',
                      kill_signal: 'SIGTERM',
                      kill_timeout: '2s',
                      merge_logs: true,
                      time: true,
                      log_date_format: 'YYYY-MM-DD',
                      namespace: 'prod',
                      env: {NODE_ENV: 'development', PORT: '3000'},
                      env_production: {NODE_ENV: 'production', PORT: '8080'},
                    },
                  ],
                };
        `, "/tmp/ecosys")

	if len(apps) != 1 {
		t.Fatalf("got %d apps, want 1", len(apps))
	}
	a := apps[0].App
	if a.Name != "web" || a.Script != "/tmp/ecosys/server.js" {
		t.Fatalf("name/script: %q %q", a.Name, a.Script)
	}
	if a.Cwd != "/tmp/ecosys/svc" || a.OutFile != "/tmp/ecosys/logs/web-out.log" ||
		a.ErrorFile != "/tmp/ecosys/logs/web-err.log" || a.PidFile != "/tmp/ecosys/pids/web.pid" {
		t.Fatalf("relative path resolution: cwd=%q out=%q err=%q pid=%q", a.Cwd, a.OutFile, a.ErrorFile, a.PidFile)
	}
	if len(a.Args) != 2 || a.Args[0] != "--port" {
		t.Fatalf("args: %v", a.Args)
	}
	if len(a.InterpreterArgs) != 1 || a.InterpreterArgs[0] != "--harmony" {
		t.Fatalf("interpreter_args: %v", a.InterpreterArgs)
	}
	if a.Instances != 2 || a.Autorestart || a.MaxRestarts != 3 {
		t.Fatalf("instances/autorestart/max_restarts: %d %v %d", a.Instances, a.Autorestart, a.MaxRestarts)
	}
	if a.MinUptime != 5*time.Second || a.RestartDelay != 250*time.Millisecond ||
		a.ExpBackoffRestartDelay != 100*time.Millisecond || a.KillTimeout != 2*time.Second {
		t.Fatalf("durations: min=%v delay=%v backoff=%v kill=%v", a.MinUptime, a.RestartDelay, a.ExpBackoffRestartDelay, a.KillTimeout)
	}
	if len(a.StopExitCodes) != 2 || a.StopExitCodes[0] != 3 || a.StopExitCodes[1] != 7 {
		t.Fatalf("stop_exit_codes: %v", a.StopExitCodes)
	}
	if a.MaxMemoryRestart != 1<<30 {
		t.Fatalf("max_memory_restart: %d", a.MaxMemoryRestart)
	}
	if a.KillSignal != "SIGTERM" || !a.MergeLogs || !a.Time || a.LogDateFormat != "YYYY-MM-DD" || a.Namespace != "prod" {
		t.Fatalf("misc echo: %v %v %v %q %q", a.KillSignal, a.MergeLogs, a.Time, a.LogDateFormat, a.Namespace)
	}
	// Rotation defaults survive ecosystem parsing when unset.
	if a.LogRotateMaxBytes != DefaultRotateMax || a.LogRotateRetain != DefaultRotateRetain {
		t.Fatalf("rotation defaults: %d %d", a.LogRotateMaxBytes, a.LogRotateRetain)
	}
	// env merge: base + production overlay.
	base := apps[0].ResolveEnv("")
	if base["NODE_ENV"] != "development" || base["PORT"] != "3000" {
		t.Fatalf("base env: %v", base)
	}
	prod := apps[0].ResolveEnv("production")
	if prod["NODE_ENV"] != "production" || prod["PORT"] != "8080" || len(prod) != 2 {
		t.Fatalf("production env: %v", prod)
	}
	// Unknown env name = base env unchanged (pm2 parity).
	if got := apps[0].ResolveEnv("staging"); got["NODE_ENV"] != "development" {
		t.Fatalf("staging env: %v", got)
	}
}

func TestEcosystemDefaultsAndAliases(t *testing.T) {
	apps := parseOK(t, `
                module.exports = {
                  apps: [{
                    script: '/abs/bin/tool',
                    args: 'serve --port 9000',        // string form: space-split
                    exec_interpreter: 'none',          // alias of interpreter
                    node_args: ['--max-old-space-size', '512'], // alias of interpreter_args
                    treekill: false,                   // accepted; always tree-kills (row 23)
                    log_rotate_max_bytes: '2MB',
                    log_rotate_retain: 5,
                  }],
                };
        `, "/x")
	a := apps[0].App
	if a.Script != "/abs/bin/tool" {
		t.Fatalf("abs script must stay: %q", a.Script)
	}
	if len(a.Args) != 3 || a.Args[2] != "9000" {
		t.Fatalf("string args: %v", a.Args)
	}
	if a.Interpreter != "none" {
		t.Fatalf("exec_interpreter alias: %q", a.Interpreter)
	}
	if len(a.InterpreterArgs) != 2 || a.InterpreterArgs[1] != "512" {
		t.Fatalf("node_args alias: %v", a.InterpreterArgs)
	}
	if a.LogRotateMaxBytes != 2<<20 || a.LogRotateRetain != 5 {
		t.Fatalf("rotation knobs: %d %d", a.LogRotateMaxBytes, a.LogRotateRetain)
	}
}

func TestEcosystemSingleAppAndDefaults(t *testing.T) {
	apps := parseOK(t, `module.exports = { script: './run.sh', name: 'job' };`, "/w")
	if len(apps) != 1 || apps[0].App.Name != "job" || apps[0].App.Script != "/w/run.sh" {
		t.Fatalf("single-app form: %+v", apps[0])
	}
	// Pinned defaults ride through.
	a := apps[0].App
	if !a.Autorestart || a.MaxRestarts != 16 || a.MinUptime != time.Second || a.KillSignal != "SIGINT" {
		t.Fatalf("defaults not applied: %+v", a)
	}
}

func TestEcosystemRotationOff(t *testing.T) {
	apps := parseOK(t, `module.exports = { apps: [{script: 'a.sh', log_rotate_max_bytes: 0}] };`, "/w")
	if apps[0].App.LogRotateMaxBytes != 0 {
		t.Fatalf("explicit 0 must mean rotation off, got %d", apps[0].App.LogRotateMaxBytes)
	}
}

func TestEcosystemErrors(t *testing.T) {
	cases := []struct {
		name, src, wantErr string
	}{
		{"unknown key", `module.exports={apps:[{script:'a',maxMemry:1}]}`, "unknown key \"maxMemry\""},
		{"missing script", `module.exports={apps:[{name:'x'}]}`, `"script" is required`},
		{"no exports", `1 + 1;`, "module.exports"},
		{"exports not object", `module.exports = 42;`, "must be an object"},
		{"apps not array", `module.exports={apps:{}}`, "must be an array"},
		{"empty apps", `module.exports={apps:[]}`, "empty"},
		{"bad app entry", `module.exports={apps:[42]}`, "apps[0]"},
		{"bad type args", `module.exports={apps:[{script:'a',args:42}]}`, "args"},
		{"bad bool", `module.exports={apps:[{script:'a',watch:'yes'}]}`, "boolean"},
		{"bad env value", `module.exports={apps:[{script:'a',env:{X:{}}}]}`, "env[\"X\"]"},
		{"bad duration", `module.exports={apps:[{script:'a',min_uptime:'soon'}]}`, "bad duration"},
		{"bad size", `module.exports={apps:[{script:'a',max_memory_restart:'lots'}]}`, "bad size"},
		{"syntax error", `module.exports={apps:[}`, "ecosystem:"},
		{"require forbidden", `module.exports={apps:[{script: require('path')}]} ;`, "ecosystem:"},
		{"process forbidden", `module.exports={apps:[{script: process.env.HOME}]};`, "ecosystem:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseEcosystem([]byte(tc.src), "/w")
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestEcosystemSandboxTimeout(t *testing.T) {
	old := evalTimeout
	evalTimeout = 100 * time.Millisecond
	defer func() { evalTimeout = old }()

	_, err := ParseEcosystem([]byte(`while(true){}`), "/w")
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("want timeout error, got %v", err)
	}
	// The VM must be time-boxed even with the interrupt raced by a busy
	// timer: a second parse still works (no leaked interrupt channel).
	if _, err := ParseEcosystem([]byte(`module.exports={apps:[{script:'a'}]}`), "/w"); err != nil {
		t.Fatalf("healthy parse after timeout: %v", err)
	}
}

func TestEcosystemJSONForm(t *testing.T) {
	// JSON is a valid JS expression: pm2 processes.json style.
	apps := parseOK(t, `{"apps":[{"name":"j","script":"j.sh","instances":"max"}]}`, "/w")
	if apps[0].App.Name != "j" || apps[0].App.Instances < 1 {
		t.Fatalf("json form: %+v", apps[0].App)
	}
}

func TestEcosystemModernJSSyntax(t *testing.T) {
	// ES6 features common in ecosystem files must evaluate (arrow fns,
	// template literals, const). The template literal needs a Go
	// interpreted string — backticks would collide with Go raw strings.
	src := "const port = 4242;\n" +
		"const mk = (n, s) => ({name: n, script: s, args: ['--port', `${port}`]});\n" +
		"module.exports = { apps: [mk('api', './api.js')] };"
	apps := parseOK(t, src, "/w")
	if apps[0].App.Name != "api" || len(apps[0].App.Args) != 2 || apps[0].App.Args[1] != "4242" {
		t.Fatalf("es6 form: %+v %+v", apps[0].App.Name, apps[0].App.Args)
	}
}

func TestEcosystemScriptResolutionCwd(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "control-plane")
	if err := os.MkdirAll(filepath.Join(subDir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(subDir, "dist", "index.js")
	if err := os.WriteFile(scriptPath, []byte("console.log('hi');\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	src := `module.exports = {
		apps: [{
			name: 'control',
			cwd: './control-plane',
			script: 'dist/index.js'
		}]
	};`

	apps := parseOK(t, src, dir)
	if len(apps) != 1 {
		t.Fatalf("got %d apps, want 1", len(apps))
	}
	if apps[0].App.Script != scriptPath {
		t.Fatalf("script = %q, want %q", apps[0].App.Script, scriptPath)
	}
}

func TestEcosystemScriptResolutionPathBinary(t *testing.T) {
	dir := t.TempDir()
	src := `module.exports = {
		apps: [{
			name: 'shell',
			cwd: './dashboard',
			script: 'sh',
			args: '-c echo hi'
		}]
	};`

	apps := parseOK(t, src, dir)
	if len(apps) != 1 {
		t.Fatalf("got %d apps, want 1", len(apps))
	}
	// "sh" must resolve via LookPath to the real binary (e.g. /bin/sh or /usr/bin/sh),
	// NOT /dir/sh or /dir/dashboard/sh.
	shBin, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	if apps[0].App.Script != shBin {
		t.Fatalf("script = %q, want %q", apps[0].App.Script, shBin)
	}
}
