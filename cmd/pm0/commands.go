//go:build linux

// All pm0 subcommand implementations.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/daemon"
	"github.com/pm0/pm0/internal/proc"
	"github.com/pm0/pm0/internal/store"
	"github.com/pm0/pm0/pkg/client"
)

// newFlagSet is the per-command parser: unknown flags are hard errors
// (compat divergence 2).
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// runDaemon is `pm0 daemon`: foreground daemon (the CLI spawns it
// detached with `daemon` too).
func runDaemon(args []string) {
	fs := newFlagSet("daemon")
	redact := fs.Bool("redact-env", false, "redact secret-looking env values in jlist/describe/dump")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if err := store.EnsureHome(); err != nil {
		fatalf("%v", err)
	}
	l, err := proc.NewLauncher(proc.ModeAuto)
	if err != nil {
		fatalf("launcher: %v", err)
	}
	srv := daemon.New(l, version, commit, *redact)
	fmt.Printf("pm0 daemon %s starting on %s (kill mode: %s)\n", version, store.SocketPath(), l.Mode())
	if err := srv.Run(); err != nil {
		fatalf("%v", err)
	}
}

// cmdStart implements `pm0 start <script> [args --] [flags]`.
func cmdStart(args []string) {
	fs := newFlagSet("start")
	name := fs.String("name", "", "app name (default: script basename)")
	fs.StringVar(name, "n", "", "alias of --name (pm2 shorthand)")
	instances := fs.String("i", "", "instance count: N, or 'max' (= CPU count); node apps run in cluster mode (shared port), others fork as <name>-0..<N-1>")
	fs.StringVar(instances, "instances", "", "alias of -i")
	execMode := fs.String("exec-mode", "", "fork | cluster (cluster needs a node interpreter; -i implies cluster for node apps)")
	interpreter := fs.String("interpreter", "", "interpreter binary, \"none\" for direct exec, empty = auto")
	interpArgs := fs.String("interpreter-args", "", "comma-separated interpreter args")
	fs.StringVar(interpArgs, "node-args", "", "alias of --interpreter-args (row 5)")
	noAutorestart := fs.Bool("no-autorestart", false, "do not restart the app on exit (row 10: default restarts)")
	maxMem := fs.String("max-memory-restart", "0", "restart on RSS exceeding this (e.g. 1G)")
	killTimeout := fs.Int64("kill-timeout", 0, "grace period before force kill, ms (default 1600)")
	killSignal := fs.String("kill-signal", "", "graceful signal (default SIGINT)")
	maxRestarts := fs.Int("max-restarts", 0, "crash-loop budget within min_uptime (default 16)")
	minUptime := fs.Int64("min-uptime", 0, "run length that marks a start stable, ms (default 1000)")
	restartDelay := fs.Int64("restart-delay", -1, "fixed delay between auto restarts, ms (default 0 = immediate)")
	expBackoffDelay := fs.Int64("exp-backoff-restart-delay", 0, "exponential backoff base delay, ms (observed pm2: first sleep = this, then x1.5 growth capped at 15000)")
	stopExitCodes := fs.String("stop-exit-codes", "", "exit codes that park the app stopped without restart, comma-separated (e.g. 3,7)")
	watch := fs.Bool("watch", false, "restart the app on file changes under its cwd (rows 21/22; ignores node_modules/.git/*.swp/*.tmp)")
	cronRestart := fs.String("cron-restart", "", "cron expression to restart the app (e.g. '*/2 * * * *')")
	waitReady := fs.Bool("wait-ready", false, "hold status launching until the child sends the node-IPC 'ready' message (row 19)")
	listenTimeout := fs.Int64("listen-timeout", 0, "wait_ready deadline in ms before forcing online (default 3000, row 18)")
	healthCmd := fs.String("health-check-cmd", "", "probe command (sh -c) run while online; consecutive failures restart the app (pm0 extension)")
	healthInterval := fs.Int64("health-check-interval", 0, "probe interval in ms (default 30000)")
	healthTimeout := fs.Int64("health-check-timeout", 0, "per-probe timeout in ms (default 5000, clamped to interval)")
	healthRetries := fs.Int("health-check-retries", 0, "consecutive failures before a restart (default 3)")
	timeFlag := fs.Bool("time", false, "timestamps in log output")
	logDateFormat := fs.String("log-date-format", "", "log timestamp format")
	outFile := fs.String("out-file", "", "stdout log path (default ~/.pm0/logs/<name>-out.log)")
	errFile := fs.String("error-file", "", "stderr log path (default ~/.pm0/logs/<name>-error.log)")
	pidFile := fs.String("pid-file", "", "pid file path (default ~/.pm0/pids/<name>-<id>.pid)")
	mergeLogs := fs.Bool("merge-logs", false, "stderr into the out log")
	namespace := fs.String("namespace", "", "app namespace (default \"default\")")
	cwd := fs.String("cwd", "", "app working directory (default: caller cwd)")
	logRotateMax := fs.String("log-rotate-max", "", "rotation threshold per sink (size like 10M, or 'off'); default 10 MiB (divergence 12)")
	logRotateRetain := fs.Int("log-rotate-retain", 0, "rotated copies retained per sink (default 30)")
	only := fs.String("only", "", "ecosystem files: start only these comma-separated app names")
	env := map[string]string{}
	envSelect := ""
	fs.Func("env", "extra child env KEY=VALUE (repeatable); or NAME to select env_<NAME> from an ecosystem file", func(v string) error {
		if _, _, ok := strings.Cut(v, "="); ok {
			return parseEnvFlag(v, env)
		}
		if envSelect != "" && envSelect != v {
			return fmt.Errorf("--env name given twice (%q and %q)", envSelect, v)
		}
		envSelect = v
		return nil
	})

	flags, positional, appArgs := splitArgs(args, fs)
	if err := fs.Parse(flags); err != nil {
		os.Exit(2)
	}

	// Ecosystem-file start (M4): a config file positional switches to the
	// batch path; with NO positional, pm2 parity kicks in and
	// ./ecosystem.config.js is used when present.
	if len(positional) == 0 {
		if fileExists("ecosystem.config.js") {
			positional = []string{"ecosystem.config.js"}
		} else {
			fatalf("start: script required (pm0 start <script> [args --] | pm0 start ecosystem.config.js)")
		}
	}
	if isEcosystemFile(positional[0]) {
		if len(positional) > 1 {
			fatalf("start: an ecosystem file takes no app arguments (unexpected %v)", positional[1:])
		}
		cmdStartEcosystem(positional[0], *only, envSelect, env)
		return
	}
	if *only != "" {
		fatalf("start: --only applies to ecosystem files only")
	}
	if envSelect != "" {
		fatalf("start: --env <NAME> selects env_<NAME> inside an ecosystem file; use --env KEY=VALUE for extra variables")
	}

	script := positional[0]
	if len(positional) > 1 {
		// pm2 requires `--` before app args; extra bare positionals would
		// silently drop otherwise.
		fatalf("start: unexpected arguments %v (use `--` to separate app args: pm0 start <script> [flags] -- <args>)", positional[1:])
	}

	// M6 instance/mode resolution (pm2 parity): -i with a node app implies
	// exec_mode cluster; non-node scripts keep fork mode (instance family
	// <name>-0..<N-1>); --exec-mode overrides explicitly and fails loudly
	// at the daemon when it contradicts the interpreter.
	instCount := 0
	if *instances != "" {
		if strings.EqualFold(*instances, "max") {
			instCount = runtime.NumCPU()
		} else {
			n, err := strconv.Atoi(*instances)
			if err != nil || n < 1 {
				fatalf("start: -i expects a positive number or 'max', got %q", *instances)
			}
			instCount = n
		}
	}
	mode := strings.ToLower(strings.TrimSpace(*execMode))
	if mode != "" && mode != "fork" && mode != "cluster" {
		fatalf("start: --exec-mode must be fork or cluster, got %q", *execMode)
	}
	if mode == "" && instCount > 0 && looksLikeNode(script, *interpreter) {
		mode = "cluster"
	}

	spec := &v1.ProcessSpec{
		Name:                     *name,
		Script:                   script,
		Args:                     appArgs,
		Interpreter:              *interpreter,
		InterpreterArgs:          splitComma(*interpArgs),
		MaxMemoryRestartBytes:    mustSize(*maxMem),
		KillTimeoutMs:            int32(*killTimeout),
		KillSignal:               *killSignal,
		MaxRestarts:              int32(*maxRestarts),
		MinUptimeMs:              *minUptime,
		Instances:                int32(instCount),
		ExecMode:                 mode,
		Autorestart:              !*noAutorestart, // row 10 pinned default true
		ExpBackoffRestartDelayMs: *expBackoffDelay,
		StopExitCodes:            mustExitCodes(*stopExitCodes),
		Watch:                    *watch,
		CronRestart:              *cronRestart,
		WaitReady:                *waitReady,
		ListenTimeoutMs:          int32(*listenTimeout),
		HealthCheckCmd:           *healthCmd,
		HealthCheckIntervalMs:    *healthInterval,
		HealthCheckTimeoutMs:     *healthTimeout,
		HealthCheckRetries:       int32(*healthRetries),
		Time:                     *timeFlag,
		LogDateFormat:            *logDateFormat,
		OutFile:                  *outFile,
		ErrorFile:                *errFile,
		PidFile:                  *pidFile,
		MergeLogs:                *mergeLogs,
		Namespace:                *namespace,
		Env:                      env,
	}
	// Rotation knobs (divergence 12): ''/0 = default, 'off' = disabled.
	if *logRotateMax != "" {
		if strings.EqualFold(*logRotateMax, "off") || *logRotateMax == "-1" {
			spec.LogRotateMaxBytes = -1
		} else {
			spec.LogRotateMaxBytes = mustSize(*logRotateMax)
		}
	}
	if *logRotateRetain > 0 {
		spec.LogRotateRetain = int32(*logRotateRetain)
	}
	if *restartDelay >= 0 {
		spec.RestartDelayMs = *restartDelay
	}
	if spec.Cwd = *cwd; spec.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			fatalf("start: resolve cwd: %v", err)
		}
		spec.Cwd = wd
	}

	c := mustDial()
	resp, err := c.Start([]*v1.ProcessSpec{spec})
	if err != nil {
		// The daemon may still have registered (parked errored) — print
		// the error and the list, then fail like pm2 does.
		fmt.Fprintf(os.Stderr, "pm0: %v\n", err)
		if resp != nil {
			printStarted(resp)
		}
		os.Exit(1)
	}
	printStarted(resp)
}

func printStarted(resp *v1.StartProcessResponse) {
	for _, p := range resp.GetProcesses() {
		fmt.Printf("[pm0] %s (pm_id=%d) status=%s pid=%d\n",
			p.GetName(), p.GetPmId(), statusWord(p), p.GetPid())
	}
}

func statusWord(p *v1.ProcessInfo) string {
	switch p.GetStatus() {
	case v1.ProcessStatus_PROCESS_STATUS_ONLINE:
		return "online"
	case v1.ProcessStatus_PROCESS_STATUS_ERRORED:
		return "errored"
	case v1.ProcessStatus_PROCESS_STATUS_LAUNCHING:
		return "launching"
	case v1.ProcessStatus_PROCESS_STATUS_STOPPED:
		return "stopped"
	case v1.ProcessStatus_PROCESS_STATUS_STOPPING:
		return "stopping"
	case v1.ProcessStatus_PROCESS_STATUS_WAITING:
		return "waiting"
	}
	return "unknown"
}

// cmdSelector drives stop/restart/reload/delete (same grammar).
func cmdSelector(args []string, kind string) {
	fs := newFlagSet(kind)
	updateEnv := fs.Bool("update-env", false, "restart: re-read the current shell environment")
	flags, targets, _ := splitArgs(args, fs)
	if err := fs.Parse(flags); err != nil {
		os.Exit(2)
	}
	// --update-env is accepted only by restart (flagset grammar); stop /
	// reload / delete reject it at Parse as an unknown flag already.
	c := mustDial()
	sel := selector(targets)
	var err error
	switch kind {
	case "stop":
		_, err = c.Stop(sel)
	case "restart":
		upd := &v1.ProcessSpec{}
		if *updateEnv {
			upd.Env = environMap()
		}
		_, err = c.Restart(sel, upd)
	case "reload":
		_, err = c.Reload(sel)
	case "delete":
		_, err = c.Delete(sel)
	}
	if err != nil {
		fatalf("%s: %v", kind, err)
	}
	fmt.Printf("[pm0] %s done\n", kind)
}

// looksLikeNode is the CLI-side heuristic for `-i` mode selection: node
// scripts (explicit node interpreter, or an auto-detected js/mjs/cjs/ts
// extension) go to cluster mode; everything else stays fork. The daemon
// re-checks strictly at WithResolvedExec — this only picks the DEFAULT.
func looksLikeNode(script, interpreter string) bool {
	switch strings.ToLower(strings.TrimSpace(interpreter)) {
	case "node":
		return true
	case "":
		switch strings.ToLower(filepath.Ext(script)) {
		case ".js", ".mjs", ".cjs", ".ts", ".mts", ".cts":
			return true
		}
		return false
	default:
		return false
	}
}

// cmdScale implements `pm0 scale <name> <+N|-N>`. Raw positional
// parsing: "-1" must not be eaten as a flag (scale takes no flags; use
// `--` first if an app is literally named like a flag).
func cmdScale(args []string) {
	var positional []string
	for _, a := range args {
		if a == "--" {
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) != 2 {
		fatalf("scale: usage: pm0 scale <name> <+N|-N|target>")
	}
	name := positional[0]
	arg := positional[1]
	delta := 0
	if strings.HasPrefix(arg, "+") || strings.HasPrefix(arg, "-") {
		n, err := strconv.Atoi(arg)
		if err != nil {
			fatalf("scale: bad delta %q (want +N, -N or an absolute count)", arg)
		}
		delta = n
	} else {
		// Absolute target (pm2 parity): delta = target - current size
		// of the family (all instances sharing the name).
		target, err := strconv.Atoi(arg)
		if err != nil || target < 1 {
			fatalf("scale: bad target %q (want +N, -N or a positive count)", arg)
		}
		c0 := mustDial()
		list, err := c0.List()
		if err != nil {
			fatalf("scale: %v", err)
		}
		current := 0
		for _, p := range list.GetProcesses() {
			if p.GetName() == name || isForkInstanceName(p.GetName(), name) {
				current++
			}
		}
		if current == 0 {
			fatalf("scale: no such app: %s", name)
		}
		delta = target - current
		if delta == 0 {
			fmt.Printf("[pm0] %s already has %d instance(s)\n", name, current)
			return
		}
	}
	c := mustDial()
	if _, err := c.Scale(name, int32(delta)); err != nil {
		fatalf("scale: %v", err)
	}
	fmt.Printf("[pm0] scaled %s by %+d\n", name, delta)
}

// isForkInstanceName reports whether app is named <base>-<digits> (the
// fork-mode instance family naming, §3.2).
func isForkInstanceName(app, base string) bool {
	suffix, ok := strings.CutPrefix(app, base+"-")
	if !ok || suffix == "" {
		return false
	}
	for _, r := range suffix {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// cmdList implements `pm0 list`.
func cmdList(args []string) {
	fs := newFlagSet("list")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	c := mustDial()
	resp, err := c.List()
	if err != nil {
		fatalf("list: %v", err)
	}
	client.WriteListTable(os.Stdout, resp.GetProcesses())
}

// cmdJlist implements `pm0 jlist` — the load-bearing JSON contract.
func cmdJlist(args []string) {
	fs := newFlagSet("jlist")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	c := mustDial()
	resp, err := c.List()
	if err != nil {
		fatalf("jlist: %v", err)
	}
	var r client.JlistRenderer
	out, err := r.RenderAll(resp.GetProcesses())
	if err != nil {
		fatalf("jlist: %v", err)
	}
	fmt.Println(string(out))
}

// cmdDescribe implements `pm0 describe <name|id>`.
func cmdDescribe(args []string) {
	fs := newFlagSet("describe")
	flags, positional, _ := splitArgs(args, fs)
	if err := fs.Parse(flags); err != nil {
		os.Exit(2)
	}
	if len(positional) != 1 {
		fatalf("describe: usage: pm0 describe <name|pm_id>")
	}
	c := mustDial()
	resp, err := c.Describe(selector(positional))
	if err != nil {
		fatalf("describe: %v", err)
	}
	client.WriteDescribe(os.Stdout, resp.GetProcess())
}

// cmdLogs implements `pm0 logs [name]`.
func cmdLogs(args []string) {
	fs := newFlagSet("logs")
	lines := fs.Int("lines", 20, "backlog lines to print first")
	errOnly := fs.Bool("err", false, "stderr only")
	outOnly := fs.Bool("out", false, "stdout only")
	raw := fs.Bool("raw", false, "no name prefix")
	noStream := fs.Bool("nostream", false, "print backlog and exit")
	timestamp := fs.Bool("timestamp", false, "prefix timestamps")
	flags, positional, _ := splitArgs(args, fs)
	if err := fs.Parse(flags); err != nil {
		os.Exit(2)
	}

	c := mustDial()
	sel := selector(positional)
	includeStderr := !*outOnly // default: both streams
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	linesCh, errCh, err := c.StreamLogs(ctx, sel, int32(*lines), includeStderr, *raw, !*noStream)
	if err != nil {
		fatalf("logs: %v", err)
	}
	colorOutput := isColorTerm()
	maxTagLen := 0 // tracks widest "id|name" seen so far for alignment
	emit := func(line *v1.LogLine) {
		if *errOnly && line.GetStream() != v1.LogLine_STREAM_STDERR {
			return
		}
		prefix := ""
		if !*raw {
			name := line.GetName()
			tag := fmt.Sprintf("%d|%s", line.GetPmId(), name)
			if len(tag) > maxTagLen {
				maxTagLen = len(tag)
			}
			padded := tag + strings.Repeat(" ", maxTagLen-len(tag))
			isErr := line.GetStream() == v1.LogLine_STREAM_STDERR
			if colorOutput {
				if isErr {
					prefix = fmt.Sprintf("\033[31m%s\033[0m \033[90m│\033[0m ", padded)
				} else {
					prefix = fmt.Sprintf("\033[36m%s\033[0m \033[90m│\033[0m ", padded)
				}
			} else {
				if isErr {
					prefix = padded + " │ "
				} else {
					prefix = padded + " │ "
				}
			}
		}
		if *timestamp {
			ts := time.Now().Format("15:04:05")
			if colorOutput {
				prefix = fmt.Sprintf("\033[90m%s\033[0m %s", ts, prefix)
			} else {
				prefix = ts + " " + prefix
			}
		}
		fmt.Printf("%s%s\n", prefix, string(line.GetData()))
	}
	for {
		select {
		case line, ok := <-linesCh:
			if !ok {
				return
			}
			emit(line)
		case err := <-errCh:
			if err != nil && ctx.Err() == nil && !strings.Contains(err.Error(), "EOF") {
				fmt.Fprintf(os.Stderr, "pm0: logs stream ended: %v\n", err)
			}
			// The producer signals the stream end BEFORE closing linesCh,
			// so buffered backlog lines can still be in flight. Draining is
			// mandatory: exiting here silently truncated the backlog tail
			// (observed: `logs -lines 5` randomly printed 2). The drain
			// always terminates — the producer closes linesCh right after
			// the error send (deferred close in pkg/client).
			for line := range linesCh {
				emit(line)
			}
			return
		}
	}
}

// cmdFlush implements `pm0 flush [name|id|all]`.
func cmdFlush(args []string) {
	fs := newFlagSet("flush")
	flags, targets, _ := splitArgs(args, fs)
	if err := fs.Parse(flags); err != nil {
		os.Exit(2)
	}

	logsDir := filepath.Join(store.Home(), store.LogsDir)
	flushAll := len(targets) == 0
	targetSet := make(map[string]bool)
	targetIds := make(map[int32]bool)
	for _, t := range targets {
		if t == "all" {
			flushAll = true
			break
		}
		if id, err := strconv.Atoi(t); err == nil && id >= 0 {
			targetIds[int32(id)] = true
		} else {
			targetSet[t] = true
		}
	}

	filesToTruncate := make(map[string]bool)

	// If daemon is alive, query live processes for their exact out/err log paths
	if c, err := client.Dial(""); err == nil {
		defer c.Close()
		if resp, err := c.List(); err == nil {
			for _, p := range resp.GetProcesses() {
				match := flushAll || targetIds[p.GetPmId()] || targetSet[p.GetName()]
				if !match {
					for name := range targetSet {
						if isInstanceOfName(p.GetName(), name) {
							match = true
							break
						}
					}
				}
				if match {
					env := p.GetPm2Env()
					if env != nil {
						if out := env.GetPmOutLogPath(); out != "" {
							filesToTruncate[out] = true
						}
						if errP := env.GetPmErrLogPath(); errP != "" {
							filesToTruncate[errP] = true
						}
					}
				}
			}
		}
	}

	// Also check store's logs directory
	if entries, err := os.ReadDir(logsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(name, ".log") && !strings.Contains(name, ".log.") {
				continue
			}
			match := flushAll
			if !match {
				for t := range targetSet {
					if strings.HasPrefix(name, t+"-") || strings.HasPrefix(name, t+".") || name == t {
						match = true
						break
					}
				}
			}
			if match {
				filesToTruncate[filepath.Join(logsDir, name)] = true
			}
		}
	}

	flushedCount := 0
	for path := range filesToTruncate {
		if err := os.Truncate(path, 0); err == nil {
			flushedCount++
		}
	}

	if flushAll {
		fmt.Printf("[pm0] Logs flushed (%d file(s))\n", flushedCount)
	} else {
		for _, t := range targets {
			fmt.Printf("[pm0] [%s] Logs flushed\n", t)
		}
	}
}

func isInstanceOfName(app, base string) bool {
	if !strings.HasPrefix(app, base+"-") {
		return false
	}
	suf := strings.TrimPrefix(app, base+"-")
	_, err := strconv.Atoi(suf)
	return err == nil
}

// cmdSave implements `pm0 save`.
func cmdSave(args []string) {
	fs := newFlagSet("save")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	c := mustDial()
	resp, err := c.Save()
	if err != nil {
		fatalf("save: %v", err)
	}
	fmt.Printf("[pm0] saved %d app(s) to %s\n", resp.GetSavedCount(), resp.GetDumpPath())
}

// cmdResurrect implements `pm0 resurrect`.
func cmdResurrect(args []string) {
	fs := newFlagSet("resurrect")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	c := mustDial()
	resp, err := c.Resurrect()
	if err != nil {
		fatalf("resurrect: %v", err)
	}
	fmt.Printf("[pm0] resurrected %d app(s), %d errored\n", resp.GetStartedCount(), resp.GetErroredCount())
}

// cmdUpdate implements `pm0 update`: in-place re-exec, then wait for
// the daemon to come back and report the running version.
func cmdUpdate(args []string) {
	fs := newFlagSet("update")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	c := mustDial()
	oldPid := mustPing(c)
	fmt.Printf("[pm0] updating daemon (pid %d): saving and re-execing\n", oldPid)
	if err := c.Update(); err != nil {
		fmt.Fprintf(os.Stderr, "pm0: update stream: %v (polling)\n", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		if c2, err := client.Dial(""); err == nil {
			if p, perr := c2.Ping(); perr == nil {
				fmt.Printf("[pm0] daemon updated in place: new pid %d, version %s\n", p.GetPid(), p.GetVersion())
				_ = c2.Close()
				return
			}
			_ = c2.Close()
		}
	}
	fatalf("update: daemon did not come back within 15s")
}

// cmdKill implements `pm0 kill [--force]`.
func cmdKill(args []string) {
	fs := newFlagSet("kill")
	force := fs.Bool("force", false, "skip stopping apps (dangerous: leaves trees running)")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	c := mustDial()
	if _, err := c.Kill(*force); err != nil {
		fatalf("kill: %v", err)
	}
	fmt.Println("[pm0] daemon stopped")
}

// cmdPing implements `pm0 ping`.
func cmdPing(args []string) {
	fs := newFlagSet("ping")
	if err := fs.Parse(mustFlags(fs, args)); err != nil {
		os.Exit(2)
	}
	c := mustDial()
	resp, err := c.Ping()
	if err != nil {
		fatalf("ping: %v", err)
	}
	b, _ := json.Marshal(resp)
	fmt.Println(string(b))
}

// --- helpers -------------------------------------------------------------

func mustDial() *client.Client {
	c, err := dialDaemon()
	if err != nil {
		fatalf("%v", err)
	}
	return c
}

func mustPing(c *client.Client) int {
	p, err := c.Ping()
	if err != nil {
		fatalf("ping: %v", err)
	}
	return int(p.GetPid())
}

// mustFlags: commands without positionals still honor flags anywhere.
func mustFlags(fs *flag.FlagSet, args []string) []string {
	flags, _, _ := splitArgs(args, fs)
	return flags
}

// envList was folded into cmdStart's --env handling (M4: the flag now also
// selects env_<NAME> inside ecosystem files).

func environMap() map[string]string {
	m := make(map[string]string)
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

func splitComma(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func mustExitCodes(s string) []int32 {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []int32
	for _, part := range strings.Split(s, ",") {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			fatalf("bad --stop-exit-codes %q: %v", s, err)
		}
		out = append(out, int32(v))
	}
	return out
}

func mustSize(s string) int64 {
	v, err := parseSize(s)
	if err != nil {
		fatalf("%v", err)
	}
	return v
}

// isColorTerm reports whether stdout is a color-capable terminal.
func isColorTerm() bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}
