// Ecosystem-file support (M4): `pm0 start ecosystem.config.js` parses the
// PM2-shaped config through a SANDBOXED JavaScript evaluation and maps every
// declared key onto the config model.
//
// Sandbox contract (deliberate hardening, documented in compat.md §7):
//
//   - the VM exposes exactly one global, `module` (with `exports`) — no
//     require, no process, no console, no timers, no network, no filesystem;
//   - evaluation is time-boxed (evalTimeout): a `while(1){}` in a config
//     file fails the start instead of hanging the CLI;
//   - unknown keys are a hard error listing the supported set (compat
//     divergence 2 — typo protection; PM2 silently ignores several).
//
// Key mapping rules:
//
//   - aliases: exec_interpreter → interpreter, node_args → interpreter_args;
//   - env_<name> maps are collected per app; the CLI merges the selected
//     one (--env production) over the base env map (pm2 parity, §4);
//   - relative script/cwd/out_file/error_file/pid_file paths resolve
//     against the ecosystem file's directory (pm2 parity);
//   - durations accept a number of ms or a string ("1000", "5s", "1m",
//     "2h", "1d"); sizes accept bytes or a string ("1024", "10M", "1G",
//     "512KB", "1GiB"); instances accepts a number or "max" (= NumCPU).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// evalTimeout bounds ecosystem evaluation. Var so tests can shrink it.
var evalTimeout = 2 * time.Second

// EcosystemApp is one parsed `apps[]` entry: the config plus the collected
// env_<name> maps (base env lives in App.Env).
type EcosystemApp struct {
	App  App
	Envs map[string]map[string]string
}

// ResolveEnv merges the selected env_<name> map over the base env (pm2:
// `--env production` layers env_production on top of env). An unknown name
// yields the base env unchanged — pm2 behaves the same way.
func (e *EcosystemApp) ResolveEnv(envName string) map[string]string {
	out := make(map[string]string, len(e.App.Env)+len(e.Envs[envName]))
	for k, v := range e.App.Env {
		out[k] = v
	}
	for k, v := range e.Envs[envName] {
		out[k] = v
	}
	return out
}

// ParseEcosystemFile reads and parses an ecosystem file. Paths inside the
// file resolve against the file's directory.
func ParseEcosystemFile(path string) ([]*EcosystemApp, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("ecosystem: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("ecosystem: abs %s: %w", path, err)
	}
	return ParseEcosystem(data, filepath.Dir(abs))
}

// ParseEcosystem evaluates an ecosystem file body in the sandbox and maps
// module.exports onto app configs. dir is the base for relative paths.
// Accepted shapes: {apps: [...]} and a single app object ({script: ...}).
func ParseEcosystem(data []byte, dir string) ([]*EcosystemApp, error) {
	raw, err := evaluateExports(data)
	if err != nil {
		return nil, err
	}
	return appsFromExports(raw, dir)
}

// appsFromExports maps the evaluated exports object onto app configs.
func appsFromExports(raw map[string]any, dir string) ([]*EcosystemApp, error) {
	if appsV, has := raw["apps"]; has {
		list, ok := appsV.([]any)
		if !ok {
			return nil, fmt.Errorf(`ecosystem: "apps" must be an array of app objects`)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf(`ecosystem: "apps" is empty`)
		}
		out := make([]*EcosystemApp, 0, len(list))
		for i, a := range list {
			m, ok := a.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("ecosystem: apps[%d]: must be an object", i)
			}
			ea, err := parseEcosystemApp(i, m, dir)
			if err != nil {
				return nil, err
			}
			out = append(out, ea)
		}
		return out, nil
	}

	// Single-app form: the exports object IS the app.
	if _, has := raw["script"]; !has {
		return nil, fmt.Errorf("ecosystem: module.exports must contain an \"apps\" array or be a single app object with \"script\"")
	}
	ea, err := parseEcosystemApp(0, raw, dir)
	if err != nil {
		return nil, err
	}
	return []*EcosystemApp{ea}, nil
}

// evaluateExports produces the exports map: pure-JSON bodies (pm2's
// processes.json style) parse directly — JSON is inert data and needs no
// sandbox — everything else runs through the goja sandbox. The JSON-first
// probe also sidesteps JS's leading-`{`-is-a-block rule, which would
// otherwise reject `{"apps": ...}` as a SyntaxError.
func evaluateExports(data []byte) (map[string]any, error) {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "{") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(trimmed), &raw); err == nil {
			return raw, nil
		}
		// Not valid JSON: fall through to the JS sandbox, whose
		// SyntaxError is the actionable message for a broken file.
	}

	vm := goja.New()
	mod := vm.NewObject()
	if err := mod.Set("exports", vm.NewObject()); err != nil {
		return nil, fmt.Errorf("ecosystem: sandbox: %w", err)
	}
	if err := vm.Set("module", mod); err != nil {
		return nil, fmt.Errorf("ecosystem: sandbox: %w", err)
	}

	// Time-boxed evaluation: Interrupt makes the in-flight RunString
	// return an *InterruptedError (goja contract) — a `while(1){}` in a
	// config file fails the start instead of hanging the CLI.
	timer := time.AfterFunc(evalTimeout, func() {
		vm.Interrupt("evaluation timeout")
	})
	_, err := vm.RunString(string(data))
	timer.Stop()
	if err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			return nil, fmt.Errorf("ecosystem: evaluation exceeded %s (sandboxed config files must terminate)", evalTimeout)
		}
		return nil, fmt.Errorf("ecosystem: %w", err)
	}

	expV := mod.Get("exports")
	if expV == nil || goja.IsUndefined(expV) || goja.IsNull(expV) {
		return nil, fmt.Errorf("ecosystem: file must set module.exports")
	}
	raw, ok := expV.Export().(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ecosystem: module.exports must be an object")
	}
	return raw, nil
}

// ecosystemAliases maps accepted PM2 key aliases onto canonical names.
var ecosystemAliases = map[string]string{
	"exec_interpreter": "interpreter",
	"node_args":        "interpreter_args",
}

// parseEcosystemApp maps one app object onto config.App. Only spec-level
// fields are filled (the daemon resolves the interpreter at registration).
func parseEcosystemApp(idx int, m map[string]any, dir string) (*EcosystemApp, error) {
	fail := func(format string, a ...any) error {
		return fmt.Errorf("ecosystem: apps[%d]: %s", idx, fmt.Sprintf(format, a...))
	}

	app := Default()
	envs := make(map[string]map[string]string)
	var rawScript string

	for k, v := range m {
		if v == nil {
			continue // explicit null/undefined = absent
		}
		canon := ecosystemAliases[k]
		if canon == "" {
			canon = k
		}
		var err error
		switch canon {
		case "name":
			app.Name, err = jsString(v)
		case "script":
			rawScript, err = jsString(v)
		case "args":
			app.Args, err = jsStringSlice(v)
		case "interpreter":
			app.Interpreter, err = jsString(v)
		case "interpreter_args":
			app.InterpreterArgs, err = jsStringSlice(v)
		case "exec_mode":
			app.ExecMode, err = jsString(v)
		case "instances":
			var n int
			if n, err = jsInstances(v); err == nil {
				app.Instances = n
			}
		case "namespace":
			app.Namespace, err = jsString(v)
		case "cwd":
			var s string
			if s, err = jsString(v); err == nil && s != "" && !filepath.IsAbs(s) {
				s = filepath.Join(dir, s)
			}
			app.Cwd = s
		case "autorestart":
			app.Autorestart, err = jsBool(v)
		case "max_restarts":
			var n int
			if n, err = jsInt(v); err == nil {
				app.MaxRestarts = n
			}
		case "min_uptime":
			app.MinUptime, err = jsDuration(v)
		case "restart_delay":
			app.RestartDelay, err = jsDuration(v)
		case "exp_backoff_restart_delay":
			app.ExpBackoffRestartDelay, err = jsDuration(v)
		case "stop_exit_codes":
			var codes []int
			if codes, err = jsIntSlice(v); err == nil {
				app.StopExitCodes = codes
			}
		case "max_memory_restart":
			var n int64
			if n, err = jsSize(v); err == nil {
				app.MaxMemoryRestart = n
			}
		case "kill_signal":
			app.KillSignal, err = jsString(v)
		case "kill_timeout":
			app.KillTimeout, err = jsDuration(v)
		case "listen_timeout":
			app.ListenTimeout, err = jsDuration(v)
		case "wait_ready":
			app.WaitReady, err = jsBool(v)
		case "cron_restart":
			app.CronRestart, err = jsString(v)
		case "watch":
			app.Watch, err = jsBool(v)
		case "treekill":
			// Row 23: accepted for compat, v1 always tree-kills.
			_, err = jsBool(v)
		case "time":
			app.Time, err = jsBool(v)
		case "log_date_format":
			app.LogDateFormat, err = jsString(v)
		case "out_file":
			var s string
			if s, err = jsString(v); err == nil && s != "" && !filepath.IsAbs(s) {
				s = filepath.Join(dir, s)
			}
			app.OutFile = s
		case "error_file":
			var s string
			if s, err = jsString(v); err == nil && s != "" && !filepath.IsAbs(s) {
				s = filepath.Join(dir, s)
			}
			app.ErrorFile = s
		case "pid_file":
			var s string
			if s, err = jsString(v); err == nil && s != "" && !filepath.IsAbs(s) {
				s = filepath.Join(dir, s)
			}
			app.PidFile = s
		case "merge_logs":
			app.MergeLogs, err = jsBool(v)
		case "log_rotate_max_bytes":
			var n int64
			if n, err = jsSize(v); err == nil {
				app.LogRotateMaxBytes = n // 0 = rotation off
			}
		case "log_rotate_retain":
			var n int
			if n, err = jsInt(v); err == nil {
				app.LogRotateRetain = n
			}
		case "health_check_cmd":
			app.HealthCheckCmd, err = jsString(v)
		case "health_check_interval":
			app.HealthCheckInterval, err = jsDuration(v)
		case "health_check_timeout":
			app.HealthCheckTimeout, err = jsDuration(v)
		case "health_check_retries":
			app.HealthCheckRetries, err = jsInt(v)
		case "env":
			var env map[string]string
			if env, err = jsEnvMap(v); err == nil {
				app.Env = env
			}
		default:
			if strings.HasPrefix(k, "env_") {
				var env map[string]string
				if env, err = jsEnvMap(v); err == nil {
					envs[strings.TrimPrefix(k, "env_")] = env
				}
			} else {
				return nil, fail("unknown key %q (pm0: unknown ecosystem keys are errors — supported: %s)",
					k, strings.Join(supportedEcosystemKeys, ", "))
			}
		}
		if err != nil {
			return nil, fail("key %q: %v", k, err)
		}
	}

	if rawScript == "" {
		return nil, fail(`"script" is required`)
	}

	if filepath.IsAbs(rawScript) {
		app.Script = rawScript
	} else {
		// Resolution order (PM2 parity):
		// 1. If script exists relative to app.Cwd, use that.
		// 2. If script exists relative to the ecosystem dir, use that.
		// 3. If script has no path separators and is found in $PATH, use the looked-up binary.
		// 4. Fall back to dir/script for standard missing-file reporting.
		var fromCwd string
		if app.Cwd != "" {
			fromCwd = filepath.Join(app.Cwd, rawScript)
		}
		fromDir := filepath.Join(dir, rawScript)

		if fromCwd != "" && fileExistsOnDisk(fromCwd) {
			app.Script = fromCwd
		} else if fileExistsOnDisk(fromDir) {
			app.Script = fromDir
		} else if !strings.Contains(rawScript, "/") && !strings.Contains(rawScript, "\\") {
			if bin, err := exec.LookPath(rawScript); err == nil {
				app.Script = bin
			} else {
				app.Script = fromDir
			}
		} else {
			app.Script = fromDir
		}
	}
	return &EcosystemApp{App: app, Envs: envs}, nil
}

func fileExistsOnDisk(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// supportedEcosystemKeys feeds the unknown-key error message (typo
// protection, divergence 2). env_<name> keys are always accepted.
var supportedEcosystemKeys = []string{
	"name", "script", "args", "interpreter", "exec_interpreter",
	"interpreter_args", "node_args", "exec_mode", "instances", "namespace",
	"cwd", "autorestart", "max_restarts", "min_uptime", "restart_delay",
	"exp_backoff_restart_delay", "stop_exit_codes", "max_memory_restart",
	"kill_signal", "kill_timeout", "listen_timeout", "wait_ready",
	"cron_restart", "watch", "treekill", "time", "log_date_format",
	"out_file", "error_file", "pid_file", "merge_logs", "env", "env_*",
	"log_rotate_max_bytes", "log_rotate_retain",
	// pm0 extensions (divergence 7): health checks
	"health_check_cmd", "health_check_interval", "health_check_timeout",
	"health_check_retries",
}

// --- scalar coercions (typed errors beat reflect decode for UX) ----------

func jsString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case bool, float64, int, int64:
		return fmt.Sprintf("%v", t), nil
	default:
		return "", fmt.Errorf("want a string, got %T", v)
	}
}

func jsBool(v any) (bool, error) {
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("want a boolean, got %T", v)
	}
	return b, nil
}

func jsFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int8:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func jsInt(v any) (int, error) {
	f, ok := jsFloat(v)
	if !ok || f != float64(int64(f)) {
		return 0, fmt.Errorf("want an integer, got %v", v)
	}
	return int(f), nil
}

// jsInstances accepts a count or "max" (= NumCPU, pm2 parity).
func jsInstances(v any) (int, error) {
	if s, ok := v.(string); ok && strings.EqualFold(strings.TrimSpace(s), "max") {
		return runtime.NumCPU(), nil
	}
	n, err := jsInt(v)
	if err != nil {
		return 0, fmt.Errorf("want a number or \"max\", got %v", v)
	}
	return n, nil
}

// jsDuration: number = ms; string = bare ms or "<n><unit>" with unit in
// s/m/h/d (pm2 accepts plain numbers; the unit suffixes are the common
// ecosystem-file style).
func jsDuration(v any) (time.Duration, error) {
	if f, ok := jsFloat(v); ok {
		if f < 0 {
			return 0, fmt.Errorf("duration must be >= 0, got %v", v)
		}
		return time.Duration(f) * time.Millisecond, nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("want ms (number) or a duration string, got %T", v)
	}
	s = strings.TrimSpace(strings.ToLower(s))
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Duration(n) * time.Millisecond, nil
	}
	// Longest suffix first ("ms" before "s"); deterministic order — a map
	// here once made "300ms" match "s" first and fail the parse.
	units := []struct {
		suffix string
		ms     int64
	}{
		{"ms", 1}, {"s", 1000}, {"m", 60_000}, {"h", 3_600_000}, {"d", 86_400_000},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, u.suffix), 10, 64)
			if err != nil || n < 0 {
				break
			}
			return time.Duration(n*u.ms) * time.Millisecond, nil
		}
	}
	return 0, fmt.Errorf("bad duration %q (want ms or e.g. \"5s\", \"500ms\", \"1m\", \"2h\", \"1d\")", s)
}

// jsSize: number = bytes; string = bare bytes or "<n><unit>" with unit in
// k/kb/kib, m/mb/mib, g/gb/gib, t/tb/tib (case-insensitive).
func jsSize(v any) (int64, error) {
	if f, ok := jsFloat(v); ok {
		if f < 0 {
			return 0, fmt.Errorf("size must be >= 0, got %v", v)
		}
		return int64(f), nil
	}
	s, ok := v.(string)
	if !ok {
		return 0, fmt.Errorf("want bytes (number) or a size string, got %T", v)
	}
	s = strings.ToLower(strings.TrimSpace(s))
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n, nil
	}
	units := []struct {
		suffix string
		mult   int64
	}{
		{"kib", 1 << 10}, {"kb", 1 << 10}, {"k", 1 << 10},
		{"mib", 1 << 20}, {"mb", 1 << 20}, {"m", 1 << 20},
		{"gib", 1 << 30}, {"gb", 1 << 30}, {"g", 1 << 30},
		{"tib", 1 << 40}, {"tb", 1 << 40}, {"t", 1 << 40},
	}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			n, err := strconv.ParseInt(strings.TrimSuffix(s, u.suffix), 10, 64)
			if err != nil || n < 0 {
				break
			}
			return n * u.mult, nil
		}
	}
	return 0, fmt.Errorf("bad size %q (want bytes or e.g. \"512KB\", \"10M\", \"1G\")", s)
}

// jsStringSlice: array of scalars, or a string (space-split, pm2's args
// string form).
func jsStringSlice(v any) ([]string, error) {
	if s, ok := v.(string); ok {
		return strings.Fields(s), nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("want an array of strings, got %T", v)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, err := jsString(e)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func jsIntSlice(v any) ([]int, error) {
	list, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("want an array of integers, got %T", v)
	}
	out := make([]int, 0, len(list))
	for _, e := range list {
		n, err := jsInt(e)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// jsEnvMap coerces an env object: string values pass through, scalars are
// stringified (exec environments are strings), containers are an error.
func jsEnvMap(v any) (map[string]string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("want an object of KEY=VALUE pairs, got %T", v)
	}
	out := make(map[string]string, len(m))
	for k, e := range m {
		switch t := e.(type) {
		case string:
			out[k] = t
		case bool, float64, int, int64:
			out[k] = fmt.Sprintf("%v", t)
		default:
			return nil, fmt.Errorf("env[%q]: want a string value, got %T", k, e)
		}
	}
	return out, nil
}
