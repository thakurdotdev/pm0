//go:build linux

// Conversions between the proto control-plane model and the internal
// config/machine/supervisor model. The proto fields carry FULLY RESOLVED
// values (the CLI applies config.Default()); the daemon still sanitizes
// zero scalars to the pinned defaults as defense for direct API users —
// booleans are copied as-is (proto3 cannot distinguish unset).
//
// jlist echo rules (compat.md §2.2): pm_exec_path is the script path,
// never the interpreter; treekill echoes true (row 23 divergence); env is
// the FULL child environment (§4), redacted when --redact-env is on.
package daemon

import (
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/config"
	"github.com/pm0/pm0/internal/machine"
	"github.com/pm0/pm0/internal/store"
	"github.com/pm0/pm0/internal/supervisor"
)

// specToConfig maps a proto spec onto the config model, filling pinned
// defaults for zero scalars and defaulting the name from the script the
// pm2 way (basename minus extension).
func specToConfig(sp *v1.ProcessSpec) config.App {
	cfg := config.Default()
	cfg.Name = sp.GetName()
	cfg.Script = sp.GetScript()
	cfg.Args = append([]string{}, sp.GetArgs()...)
	cfg.Interpreter = sp.GetInterpreter()
	cfg.InterpreterArgs = append([]string{}, sp.GetInterpreterArgs()...)
	cfg.Cwd = sp.GetCwd()
	if len(sp.GetEnv()) > 0 {
		cfg.Env = sp.GetEnv()
	}
	cfg.ExecMode = sp.GetExecMode()
	if cfg.ExecMode == "" {
		cfg.ExecMode = "fork"
	}
	cfg.Instances = int(sp.GetInstances())
	if cfg.Instances < 1 {
		cfg.Instances = 1
	}
	cfg.Namespace = sp.GetNamespace()
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	cfg.Autorestart = sp.GetAutorestart()
	cfg.MaxRestarts = int(sp.GetMaxRestarts())
	if cfg.MaxRestarts <= 0 {
		cfg.MaxRestarts = config.DefaultMaxRestarts
	}
	cfg.MinUptime = time.Duration(sp.GetMinUptimeMs()) * time.Millisecond
	if cfg.MinUptime <= 0 {
		cfg.MinUptime = config.DefaultMinUptime
	}
	cfg.RestartDelay = time.Duration(sp.GetRestartDelayMs()) * time.Millisecond
	cfg.ExpBackoffRestartDelay = time.Duration(sp.GetExpBackoffRestartDelayMs()) * time.Millisecond
	for _, c := range sp.GetStopExitCodes() {
		cfg.StopExitCodes = append(cfg.StopExitCodes, int(c))
	}
	cfg.MaxMemoryRestart = sp.GetMaxMemoryRestartBytes()
	cfg.KillSignal = sp.GetKillSignal()
	if cfg.KillSignal == "" {
		cfg.KillSignal = config.DefaultSignal
	}
	cfg.KillTimeout = time.Duration(sp.GetKillTimeoutMs()) * time.Millisecond
	if cfg.KillTimeout <= 0 {
		cfg.KillTimeout = config.DefaultKillTimeout
	}
	cfg.ListenTimeout = time.Duration(sp.GetListenTimeoutMs()) * time.Millisecond
	if cfg.ListenTimeout <= 0 {
		cfg.ListenTimeout = config.DefaultListenTimeout
	}
	cfg.WaitReady = sp.GetWaitReady()
	cfg.CronRestart = sp.GetCronRestart()
	cfg.Watch = sp.GetWatch()
	cfg.Treekill = true // row 23: always tree-kill in v1
	cfg.Time = sp.GetTime()
	cfg.LogDateFormat = sp.GetLogDateFormat()
	cfg.MergeLogs = sp.GetMergeLogs()

	// Explicit sinks win; otherwise the daemon fills concrete ~/.pm0
	// paths at registration (needs the pm_id → fillPaths under StartApps).
	cfg.OutFile = sp.GetOutFile()
	cfg.ErrorFile = sp.GetErrorFile()
	if cfg.MergeLogs && cfg.OutFile != "" {
		cfg.ErrorFile = cfg.OutFile
	}
	cfg.PidFile = sp.GetPidFile()

	// Rotation knobs (divergence 12, M4): the wire's three-state encoding
	// (0 = default, -1 = off, >0 = bytes) maps onto the in-memory model
	// (0 = off, >0 = bytes). config.Default() pre-filled the defaults.
	switch max := sp.GetLogRotateMaxBytes(); {
	case max == 0:
		// leave DefaultRotateMax
	case max < 0:
		cfg.LogRotateMaxBytes = 0 // rotation off
	default:
		cfg.LogRotateMaxBytes = max
	}
	if retain := sp.GetLogRotateRetain(); retain > 0 {
		cfg.LogRotateRetain = int(retain)
	}

	// Health check (divergence 7, M5 — pm0 extension): cmd empty = off;
	// zero knobs lift to the pinned defaults for direct API users, the
	// same lift the dump path applies on load.
	cfg.HealthCheckCmd = sp.GetHealthCheckCmd()
	if v := sp.GetHealthCheckIntervalMs(); v > 0 {
		cfg.HealthCheckInterval = time.Duration(v) * time.Millisecond
	}
	if v := sp.GetHealthCheckTimeoutMs(); v > 0 {
		cfg.HealthCheckTimeout = time.Duration(v) * time.Millisecond
	}
	if v := int(sp.GetHealthCheckRetries()); v > 0 {
		cfg.HealthCheckRetries = v
	}

	if cfg.Name == "" && cfg.Script != "" {
		cfg.Name = defaultName(cfg.Script)
	}
	return cfg
}

// fillPaths completes per-id sink paths (rows 26-28) for apps started
// without explicit out/err/pid files. Cluster instances get per-instance
// log files keyed by NODE_APP_INSTANCE (observed PM2 7.0.4: web-out-0.log,
// web-out-1.log, ...); fork apps share the name-keyed pair.
func fillPaths(cfg config.App, id int) config.App {
	if cfg.ExecMode == "cluster" {
		out, errFile, pid := store.LogPathsCluster(cfg.Name, cfg.Instance, id)
		if cfg.OutFile == "" {
			cfg.OutFile = out
			if cfg.MergeLogs {
				cfg.ErrorFile = out
			}
		}
		if cfg.ErrorFile == "" {
			cfg.ErrorFile = errFile
		}
		if cfg.PidFile == "" {
			cfg.PidFile = pid
		}
		return cfg
	}
	out, errFile, pid := store.LogPaths(cfg.Name, id)
	if cfg.OutFile == "" {
		cfg.OutFile = out
		if cfg.MergeLogs {
			cfg.ErrorFile = out
		}
	}
	if cfg.ErrorFile == "" {
		cfg.ErrorFile = errFile
	}
	if cfg.PidFile == "" {
		cfg.PidFile = pid
	}
	return cfg
}

// execModePM2 renders exec_mode the PM2 jlist way ("fork_mode" /
// "cluster_mode" — observed pm2_env strings, load-bearing for scripts).
func execModePM2(mode string) string {
	if mode == "cluster" {
		return "cluster_mode"
	}
	return "fork_mode"
}

// defaultName mirrors pm2: script basename without extension.
func defaultName(script string) string {
	base := script
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// killSignalValue converts a config signal name ("SIGINT", "INT") to its
// syscall number; unknown names fall back to the pinned default.
func killSignalValue(name string) syscall.Signal {
	if sig, ok := machine.ParseKillSignal(name); ok {
		return sig
	}
	return syscall.SIGINT
}

// statusToProto maps the pinned vocabulary onto the enum.
func statusToProto(s machine.Status) v1.ProcessStatus {
	switch s {
	case machine.StatusLaunching:
		return v1.ProcessStatus_PROCESS_STATUS_LAUNCHING
	case machine.StatusOnline:
		return v1.ProcessStatus_PROCESS_STATUS_ONLINE
	case machine.StatusStopping:
		return v1.ProcessStatus_PROCESS_STATUS_STOPPING
	case machine.StatusStopped:
		return v1.ProcessStatus_PROCESS_STATUS_STOPPED
	case machine.StatusWaiting:
		return v1.ProcessStatus_PROCESS_STATUS_WAITING
	case machine.StatusErrored:
		return v1.ProcessStatus_PROCESS_STATUS_ERRORED
	}
	return v1.ProcessStatus_PROCESS_STATUS_UNSPECIFIED
}

// viewToProcessInfo renders one registry view into the typed jlist backing.
func viewToProcessInfo(v supervisor.AppView, m *v1.Monit, redact bool) *v1.ProcessInfo {
	cfg := v.Config
	snap := v.Runtime
	exitCode, exitCodePresent := exitCodeProto(snap.ExitCode)

	env := fullChildEnv(cfg.Env)
	if redact {
		env = redactEnvMap(env)
	}
	if cfg.ExecMode == "cluster" {
		// NODE_APP_INSTANCE rides the pm2_env (observed PM2 7.0.4 jlist);
		// machine.launch injects it into the real child env at spawn.
		env["NODE_APP_INSTANCE"] = strconv.Itoa(cfg.Instance)
	}

	pmEnv := &v1.ProcessEnv{
		Name:                  cfg.Name,
		Namespace:             cfg.Namespace,
		PmId:                  int32(v.ID),
		PmExecPath:            cfg.Script,
		Args:                  cfg.Args,
		PmCwd:                 cfg.Cwd,
		PmOutLogPath:          cfg.OutFile,
		PmErrLogPath:          cfg.ErrorFile,
		PmPidPath:             cfg.PidFile,
		Interpreter:           cfg.Interpreter,
		InterpreterArgs:       cfg.InterpreterArgs,
		ExecMode:              execModePM2(cfg.ExecMode),
		Instances:             int32(cfg.Instances),
		KillSignal:            cfg.KillSignal,
		KillTimeoutMs:         int32(cfg.KillTimeout.Milliseconds()),
		Autorestart:           cfg.Autorestart,
		MaxRestarts:           int32(cfg.MaxRestarts),
		MinUptimeMs:           cfg.MinUptime.Milliseconds(),
		MaxMemoryRestartBytes: cfg.MaxMemoryRestart,
		RestartTime:           int32(snap.RestartTime),
		UnstableRestarts:      int32(snap.UnstableRestarts),
		CreatedAt:             snap.CreatedAtMs,
		PmUptime:              snap.PmUptimeMs,
		ExitCode:              exitCode,
		ExitCodePresent:       exitCodePresent,
		WaitReady:             cfg.WaitReady,
		ListenTimeoutMs:       int32(cfg.ListenTimeout.Milliseconds()),
		CronRestart:           cfg.CronRestart,
		Watch:                 cfg.Watch,
		Env:                   env,
		Treekill:              true, // row 23 divergence
		Time:                  cfg.Time,
		MergeLogs:             cfg.MergeLogs,
		LogDateFormat:         cfg.LogDateFormat,
	}

	return &v1.ProcessInfo{
		PmId:       int32(v.ID),
		Name:       snap.Name,
		Pid:        int32(snap.Pid),
		Status:     statusToProto(snap.Status),
		Monit:      m,
		Pm2Env:     pmEnv,
		ExitReason: snap.LastExitReason,
	}
}

// exitCodeProto: nil exit (running or signal death) must reach the JSON
// layer as null, so presence rides alongside the value.
func exitCodeProto(code *int) (int32, bool) {
	if code == nil {
		return 0, false
	}
	return int32(*code), true
}

// fullChildEnv dumps what the child would run with: the daemon environment
// merged with the app extras (machine.buildEnv semantics), as a map.
func fullChildEnv(extra map[string]string) map[string]string {
	out := make(map[string]string, len(extra)+len(os.Environ()))
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
