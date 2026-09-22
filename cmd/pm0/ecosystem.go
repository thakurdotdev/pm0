//go:build linux

// M4 config surface: `pm0 start <ecosystem file>` (--only, --env NAME),
// `pm0 env <name|id>` (pm2 parity) and `pm0 startup`/`unstartup`
// (systemd resurrection unit).
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/startup"
	"github.com/pm0/pm0/internal/store"
)

// isEcosystemFile decides whether a `start` positional is a config file
// (pm2 parity): .json anywhere, any path containing "ecosystem", or the
// `<name>.config.{js,cjs,mjs}` convention.
func isEcosystemFile(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if filepath.Ext(base) == ".json" {
		return true
	}
	if strings.Contains(base, "ecosystem") {
		return true
	}
	for _, sfx := range []string{".config.js", ".config.cjs", ".config.mjs"} {
		if strings.HasSuffix(base, sfx) {
			return true
		}
	}
	return false
}

// fileExists reports whether path exists (any type).
func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// cmdStartEcosystem implements `pm0 start <ecosystem file>`. All apps
// (after --only filtering) go to the daemon in ONE Start RPC so their
// pm_ids stay adjacent, mirroring a pm2 ecosystem start.
func cmdStartEcosystem(path, only, envSelect string, extraEnv map[string]string) {
	eas, err := config.ParseEcosystemFile(path)
	if err != nil {
		fatalf("%v", err)
	}

	var want map[string]bool
	if only != "" {
		want = make(map[string]bool)
		for _, n := range strings.Split(only, ",") {
			if n = strings.TrimSpace(n); n != "" {
				want[n] = true
			}
		}
	}

	callerCwd, err := os.Getwd()
	if err != nil {
		fatalf("start: resolve cwd: %v", err)
	}
	specs := make([]*v1.ProcessSpec, 0, len(eas))
	for _, ea := range eas {
		if want != nil && !want[ea.App.Name] {
			continue
		}
		specs = append(specs, specFromEcosystemApp(ea, envSelect, extraEnv, callerCwd))
	}
	if want != nil && len(specs) == 0 {
		fatalf("start: --only %q matched none of the ecosystem apps", only)
	}
	if len(specs) == 0 {
		fatalf("start: ecosystem file has no apps")
	}

	c := mustDial()

	listResp, _ := c.List()
	existingByName := make(map[string]*v1.ProcessInfo)
	if listResp != nil {
		for _, p := range listResp.GetProcesses() {
			existingByName[p.GetName()] = p
		}
	}

	var newSpecs []*v1.ProcessSpec
	var toRestart []*v1.ProcessSpec
	for _, sp := range specs {
		if _, exists := existingByName[sp.GetName()]; exists {
			toRestart = append(toRestart, sp)
		} else {
			newSpecs = append(newSpecs, sp)
		}
	}

	for _, sp := range toRestart {
		_, err := c.Restart(&v1.Selector{Names: []string{sp.GetName()}}, sp)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pm0: restart %s: %v\n", sp.GetName(), err)
		} else {
			p := existingByName[sp.GetName()]
			fmt.Printf("[pm0] %s (pm_id=%d) already exists -> restarted\n", sp.GetName(), p.GetPmId())
		}
	}

	if len(newSpecs) > 0 {
		resp, err := c.Start(newSpecs)
		if err != nil {
			// The daemon may still have registered (parked errored) —
			// same error shape as a script start.
			fmt.Fprintf(os.Stderr, "pm0: %v\n", err)
			if resp != nil {
				printStarted(resp)
			}
			os.Exit(1)
		}
		printStarted(resp)
	}
}

// specFromEcosystemApp maps a parsed ecosystem app onto the wire spec.
// Rotation knobs convert memory encoding (0 = off) → wire encoding
// (-1 = off, 0 = default); everything else is a direct copy of the
// resolved value.
func specFromEcosystemApp(ea *config.EcosystemApp, envSelect string, extraEnv map[string]string, callerCwd string) *v1.ProcessSpec {
	a := ea.App
	env := ea.ResolveEnv(envSelect)
	for k, v := range extraEnv {
		env[k] = v
	}
	if env == nil {
		env = map[string]string{}
	}

	sp := &v1.ProcessSpec{
		Name:                     a.Name,
		Script:                   a.Script,
		Args:                     a.Args,
		Interpreter:              a.Interpreter,
		InterpreterArgs:          a.InterpreterArgs,
		Cwd:                      a.Cwd,
		Env:                      env,
		ExecMode:                 a.ExecMode,
		Instances:                int32(a.Instances),
		Autorestart:              a.Autorestart,
		KillSignal:               a.KillSignal,
		KillTimeoutMs:            int32(a.KillTimeout.Milliseconds()),
		MaxRestarts:              int32(a.MaxRestarts),
		MinUptimeMs:              a.MinUptime.Milliseconds(),
		RestartDelayMs:           a.RestartDelay.Milliseconds(),
		ExpBackoffRestartDelayMs: a.ExpBackoffRestartDelay.Milliseconds(),
		StopExitCodes:            intsToI32(a.StopExitCodes),
		MaxMemoryRestartBytes:    a.MaxMemoryRestart,
		CronRestart:              a.CronRestart,
		Watch:                    a.Watch,
		Treekill:                 true, // row 23: always tree-kills in v1
		Time:                     a.Time,
		LogDateFormat:            a.LogDateFormat,
		OutFile:                  a.OutFile,
		ErrorFile:                a.ErrorFile,
		PidFile:                  a.PidFile,
		MergeLogs:                a.MergeLogs,
		Namespace:                a.Namespace,
		ListenTimeoutMs:          int32(a.ListenTimeout.Milliseconds()),
		WaitReady:                a.WaitReady,
		HealthCheckCmd:           a.HealthCheckCmd,
		HealthCheckIntervalMs:    a.HealthCheckInterval.Milliseconds(),
		HealthCheckTimeoutMs:     a.HealthCheckTimeout.Milliseconds(),
		HealthCheckRetries:       int32(a.HealthCheckRetries),
	}
	if a.LogRotateMaxBytes == 0 {
		sp.LogRotateMaxBytes = -1 // wire: rotation off
	} else {
		sp.LogRotateMaxBytes = a.LogRotateMaxBytes
	}
	if a.LogRotateRetain > 0 {
		sp.LogRotateRetain = int32(a.LogRotateRetain)
	}
	if sp.Cwd == "" {
		sp.Cwd = callerCwd // pm2: ecosystem apps run from the caller's shell unless cwd is set
	}
	return sp
}

// intsToI32 widens config's []int stop-exit codes to the proto type.
func intsToI32(in []int) []int32 {
	out := make([]int32, 0, len(in))
	for _, v := range in {
		out = append(out, int32(v))
	}
	return out
}

// cmdEnv implements `pm0 env <name|id>` (pm2 parity): prints the app's
// pm2_env.env view — the full child environment, redacted when the daemon
// runs --redact-env.
func cmdEnv(args []string) {
	fs := newFlagSet("env")
	flags, positional, _ := splitArgs(args, fs)
	if err := fs.Parse(flags); err != nil {
		os.Exit(2)
	}
	if len(positional) != 1 {
		fatalf("env: usage: pm0 env <name|pm_id>")
	}
	c := mustDial()
	resp, err := c.Describe(selector(positional))
	if err != nil {
		fatalf("env: %v", err)
	}
	env := resp.GetProcess().GetPm2Env().GetEnv()
	if len(env) == 0 {
		fatalf("env: app has no environment recorded")
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("%s=%s\n", k, env[k])
	}
}

// cmdStartup implements `pm0 startup`: installs (as root) or prints (as
// a normal user) the systemd unit that resurrects the app set on boot.
// --print skips the systemd detection and only prints — for CI, debugging
// and hosts where the daemon's init differs from the operator's shell.
func cmdStartup(args []string) {
	fs := newFlagSet("startup")
	printOnly := fs.Bool("print", false, "print the unit and install commands instead of installing (skips systemd detection)")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	if !*printOnly && startup.Detect() != "systemd" {
		fatalf("startup: only systemd is supported in v1 (sentinel %s not found); add `pm0 resurrect` to your init scripts instead", startup.SystemdRunPath)
	}
	userName, _, err := startup.CurrentUser()
	if err != nil {
		fatalf("%v", err)
	}
	// Pin the EFFECTIVE home (store.Home honors PM0_HOME) — the unit
	// must resurrect the daemon this shell is actually talking to, the
	// same reason pm2's startup takes --hp.
	home := store.Home()
	bin, err := os.Executable()
	if err != nil {
		fatalf("startup: resolve binary: %v", err)
	}
	if abs, aerr := filepath.Abs(bin); aerr == nil {
		bin = abs
	}
	unitPath := filepath.Join("/etc/systemd/system", startup.UnitName(userName))

	if !*printOnly && os.Geteuid() == 0 {
		if err := os.WriteFile(unitPath, []byte(startup.Unit(userName, home, bin)), 0o644); err != nil {
			fatalf("startup: write %s: %v", unitPath, err)
		}
		for _, cmd := range [][]string{
			{"systemctl", "daemon-reload"},
			{"systemctl", "enable", startup.UnitName(userName)},
		} {
			if out, err := exec.Command(cmd[0], cmd[1:]...).CombinedOutput(); err != nil {
				fatalf("startup: %s: %v\n%s", strings.Join(cmd, " "), err, out)
			}
		}
		fmt.Printf("[pm0] installed %s (PM0_HOME=%s, user=%s)\n", unitPath, home, userName)
		fmt.Printf("[pm0] the daemon will resurrect the saved app set on boot (run `pm0 save` first)\n")
		return
	}

	fmt.Println("[pm0] run the following as root to enable resurrection on boot:")
	fmt.Print(startup.InstallScript(userName, home, bin, unitPath))
}

// cmdUnstartup removes the systemd unit (as root) or prints how.
func cmdUnstartup(args []string) {
	fs := newFlagSet("unstartup")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	userName, _, err := startup.CurrentUser()
	if err != nil {
		fatalf("%v", err)
	}
	unit := startup.UnitName(userName)
	unitPath := filepath.Join("/etc/systemd/system", unit)

	if os.Geteuid() == 0 {
		_ = exec.Command("systemctl", "disable", unit).Run()
		if err := os.Remove(unitPath); err != nil {
			fatalf("unstartup: remove %s: %v", unitPath, err)
		}
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			fatalf("unstartup: daemon-reload: %v\n%s", err, out)
		}
		fmt.Printf("[pm0] removed %s\n", unitPath)
		return
	}

	fmt.Println("[pm0] run the following as root to disable resurrection on boot:")
	fmt.Printf("systemctl disable %s\nrm -f %s\nsystemctl daemon-reload\n", unit, unitPath)
}
