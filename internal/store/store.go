// Package store owns pm0's on-disk state: the PM0_HOME layout and the
// dump.json app-set persistence (invariant I4: atomic writes — a crash mid
// save never corrupts the previous dump).
//
// Layout (compat.md divergence 1 — pm2 uses ~/.pm2):
//
//	~/.pm0/pm0.sock   control socket (0600)
//	~/.pm0/dump.json    saved app set (pm2 save / resurrect)
//	~/.pm0/logs/        per-app out/err logs (<name>-out.log, -error.log)
//	~/.pm0/pids/        per-app pid files (<name>-<id>.pid)
//	~/.pm0/daemon.log   daemon stdout/stderr when CLI-spawned
//
// PM0_HOME overrides the root (compat.md §4, the PM2_HOME analog).
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pm0/pm0/internal/config"
)

// DumpVersion is the dump.json schema version (bumped only with a
// migration story; v1 is the M2 pin).
const DumpVersion = 1

// DumpName / SocketName are the fixed file names inside home.
const (
	DumpName   = "dump.json"
	SocketName = "pm0.sock"
	LogsDir    = "logs"
	PidsDir    = "pids"
	DaemonLog  = "daemon.log"
	// PidName is the daemon's own pidfile ("<pid> <starttime>"). pm0
	// extension: the control-socket takeover reads it to prove the
	// previous daemon died, skipping the stale-socket grace.
	PidName = "pm0.pid"
)

// Home returns the state root: $PM0_HOME or ~/.pm0.
func Home() string {
	if h := os.Getenv("PM0_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".pm0"
	}
	return filepath.Join(home, ".pm0")
}

// Paths derived from home.
func SocketPath() string    { return filepath.Join(Home(), SocketName) }
func DumpPath() string      { return filepath.Join(Home(), DumpName) }
func DaemonLogPath() string { return filepath.Join(Home(), DaemonLog) }
func PidPath() string       { return filepath.Join(Home(), PidName) }

// EnsureHome creates the directory layout. Idempotent.
func EnsureHome() error {
	for _, d := range []string{Home(), filepath.Join(Home(), LogsDir), filepath.Join(Home(), PidsDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("store: mkdir %s: %w", d, err)
		}
	}
	return nil
}

// LogPaths fills the compat log/pid sinks (rows 26-28) for one app
// instance: <name>-out.log / <name>-error.log / <name>-<id>.pid.
func LogPaths(name string, pmid int) (out, errFile, pid string) {
	base := filepath.Join(Home(), LogsDir, name)
	return base + "-out.log",
		filepath.Join(Home(), LogsDir, name+"-error.log"),
		filepath.Join(Home(), PidsDir, fmt.Sprintf("%s-%d.pid", name, pmid))
}

// LogPathsCluster is the cluster-mode variant (observed PM2 7.0.4: each
// instance of a same-named cluster group owns its log files, named by the
// NODE_APP_INSTANCE index — web-out-0.log, web-out-1.log, ...). The pid
// file stays pm_id-keyed (one live tree per pm_id).
func LogPathsCluster(name string, instance, pmid int) (out, errFile, pid string) {
	base := filepath.Join(Home(), LogsDir, name)
	return fmt.Sprintf("%s-out-%d.log", base, instance),
		filepath.Join(Home(), LogsDir, fmt.Sprintf("%s-error-%d.log", name, instance)),
		filepath.Join(Home(), PidsDir, fmt.Sprintf("%s-%d.pid", name, pmid))
}

// Entry is one persisted app: its pm_id plus the resolved config.
type Entry struct {
	PMID int
	App  config.App
}

// Dump is the persisted app set.
type Dump struct {
	Version   int     `json:"version"`
	RedactEnv bool    `json:"redact_env"`
	Apps      []Entry `json:"-"`
}

// appRecord is the JSON shape of one persisted app. Durations ride as
// milliseconds (pm2 numbers are ms too — readable diffs beat struct tags
// on config.App, which would leak nanosecond internals into the file).
type appRecord struct {
	PMID              int      `json:"pm_id"`
	Name              string   `json:"name"`
	Script            string   `json:"pm_exec_path"`
	Args              []string `json:"args"`
	Interpreter       string   `json:"interpreter"`
	InterpreterArgs   []string `json:"interpreter_args"`
	ExecPath          string   `json:"exec_path"`
	ExecArgs          []string `json:"exec_args"`
	ExecMode          string   `json:"exec_mode"`
	Instances         int      `json:"instances"`
	Instance          int      `json:"instance,omitempty"`
	Namespace         string   `json:"namespace"`
	Cwd               string   `json:"pm_cwd"`
	Autorestart       bool     `json:"autorestart"`
	MaxRestarts       int      `json:"max_restarts"`
	MinUptimeMs       int64    `json:"min_uptime"`
	RestartDelayMs    int64    `json:"restart_delay"`
	ExpBackoffDelayMs int64    `json:"exp_backoff_restart_delay"`
	StopExitCodes     []int    `json:"stop_exit_codes"`
	ExpBackoffCapMs   int64    `json:"exp_backoff_cap"`
	MaxMemoryRestart  int64    `json:"max_memory_restart"`
	KillSignal        string   `json:"kill_signal"`
	KillTimeoutMs     int64    `json:"kill_timeout"`
	ListenTimeoutMs   int64    `json:"listen_timeout"`
	WaitReady         bool     `json:"wait_ready"`
	CronRestart       string   `json:"cron_restart"`
	Watch             bool     `json:"watch"`
	// Health check (divergence 7, M5 — pm0 extension). Cmd "" = off;
	// 0 durations/retries lift to the pinned defaults on load (legacy
	// dumps and hand-edited files stay valid).
	HealthCheckCmd     string `json:"health_check_cmd,omitempty"`
	HealthIntervalMs   int64  `json:"health_check_interval,omitempty"`
	HealthTimeoutMs    int64  `json:"health_check_timeout,omitempty"`
	HealthCheckRetries int    `json:"health_check_retries,omitempty"`
	Treekill           bool   `json:"treekill"`
	Time               bool   `json:"time"`
	LogDateFormat      string `json:"log_date_format"`
	OutFile            string `json:"out_file"`
	ErrorFile          string `json:"error_file"`
	PidFile            string `json:"pid_file"`
	MergeLogs          bool   `json:"merge_logs"`
	// Rotation knobs (divergence 12, M4). Wire encoding: 0 = default
	// (also the legacy M3-dump value — the field may be absent), -1 =
	// rotation off, >0 = bytes. Retain: 0 = default, >0 = value.
	LogRotateMaxBytes int64             `json:"log_rotate_max_bytes,omitempty"`
	LogRotateRetain   int               `json:"log_rotate_retain,omitempty"`
	Env               map[string]string `json:"env"`
}

type dumpFile struct {
	Version   int         `json:"version"`
	RedactEnv bool        `json:"redact_env"`
	Apps      []appRecord `json:"apps"`
}

// Save writes the dump atomically (temp file in the same dir + rename,
// invariant I4) and fsyncs before renaming so a power cut cannot leave a
// truncated dump behind.
func Save(d Dump) error {
	if err := EnsureHome(); err != nil {
		return err
	}
	if d.Apps == nil {
		d.Apps = []Entry{}
	}
	d.Version = DumpVersion
	file := dumpFile{Version: d.Version, RedactEnv: d.RedactEnv}
	for _, e := range d.Apps {
		file.Apps = append(file.Apps, appToRecord(e))
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal dump: %w", err)
	}
	data = append(data, '\n')

	path := DumpPath()
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("store: open %s: %w", tmp, err)
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("store: rename dump: %w", err)
	}
	return nil
}

// Load reads dump.json. A missing dump is not an error: the returned Dump
// is empty and ok=false.
func Load() (Dump, bool, error) {
	data, err := os.ReadFile(DumpPath())
	if err != nil {
		if os.IsNotExist(err) {
			return Dump{}, false, nil
		}
		return Dump{}, false, fmt.Errorf("store: read dump: %w", err)
	}
	var file dumpFile
	if err := json.Unmarshal(data, &file); err != nil {
		// A corrupt dump must not brick resurrect: surface loudly, but the
		// daemon decides policy (never abort the batch, compat §Save).
		return Dump{}, false, fmt.Errorf("store: parse %s: %w", DumpPath(), err)
	}
	if file.Version > DumpVersion {
		return Dump{}, false, fmt.Errorf("store: dump version %d newer than supported %d", file.Version, DumpVersion)
	}
	d := Dump{Version: file.Version, RedactEnv: file.RedactEnv, Apps: []Entry{}}
	for _, r := range file.Apps {
		d.Apps = append(d.Apps, recordToApp(r))
	}
	return d, true, nil
}

func appToRecord(e Entry) appRecord {
	a := e.App
	return appRecord{
		PMID:               e.PMID,
		Name:               a.Name,
		Script:             a.Script,
		Args:               a.Args,
		Interpreter:        a.Interpreter,
		InterpreterArgs:    a.InterpreterArgs,
		ExecPath:           a.ExecPath,
		ExecArgs:           a.ExecArgs,
		ExecMode:           a.ExecMode,
		Instances:          a.Instances,
		Instance:           a.Instance,
		Namespace:          a.Namespace,
		Cwd:                a.Cwd,
		Autorestart:        a.Autorestart,
		MaxRestarts:        a.MaxRestarts,
		MinUptimeMs:        a.MinUptime.Milliseconds(),
		RestartDelayMs:     a.RestartDelay.Milliseconds(),
		ExpBackoffDelayMs:  a.ExpBackoffRestartDelay.Milliseconds(),
		StopExitCodes:      a.StopExitCodes,
		ExpBackoffCapMs:    a.ExpBackoffCap.Milliseconds(),
		MaxMemoryRestart:   a.MaxMemoryRestart,
		KillSignal:         a.KillSignal,
		KillTimeoutMs:      a.KillTimeout.Milliseconds(),
		ListenTimeoutMs:    a.ListenTimeout.Milliseconds(),
		WaitReady:          a.WaitReady,
		CronRestart:        a.CronRestart,
		Watch:              a.Watch,
		HealthCheckCmd:     a.HealthCheckCmd,
		HealthIntervalMs:   a.HealthCheckInterval.Milliseconds(),
		HealthTimeoutMs:    a.HealthCheckTimeout.Milliseconds(),
		HealthCheckRetries: a.HealthCheckRetries,
		Treekill:           a.Treekill,
		Time:               a.Time,
		LogDateFormat:      a.LogDateFormat,
		OutFile:            a.OutFile,
		ErrorFile:          a.ErrorFile,
		PidFile:            a.PidFile,
		MergeLogs:          a.MergeLogs,
		// Memory (0 = off) → wire (0 = default, -1 = off).
		LogRotateMaxBytes: wireRotateMax(a.LogRotateMaxBytes),
		LogRotateRetain:   a.LogRotateRetain,
		Env:               a.Env,
	}
}

func recordToApp(r appRecord) Entry {
	ms := func(v int64) time.Duration { return time.Duration(v) * time.Millisecond }
	a := config.App{
		Name:                   r.Name,
		Script:                 r.Script,
		Args:                   r.Args,
		Interpreter:            r.Interpreter,
		InterpreterArgs:        r.InterpreterArgs,
		ExecPath:               r.ExecPath,
		ExecArgs:               r.ExecArgs,
		ExecMode:               r.ExecMode,
		Instances:              r.Instances,
		Instance:               r.Instance,
		Namespace:              r.Namespace,
		Cwd:                    r.Cwd,
		Autorestart:            r.Autorestart,
		MaxRestarts:            r.MaxRestarts,
		MinUptime:              ms(r.MinUptimeMs),
		RestartDelay:           ms(r.RestartDelayMs),
		ExpBackoffRestartDelay: time.Duration(r.ExpBackoffDelayMs) * time.Millisecond,
		StopExitCodes:          r.StopExitCodes,
		ExpBackoffCap:          ms(r.ExpBackoffCapMs),
		MaxMemoryRestart:       r.MaxMemoryRestart,
		KillSignal:             r.KillSignal,
		KillTimeout:            ms(r.KillTimeoutMs),
		ListenTimeout:          ms(r.ListenTimeoutMs),
		WaitReady:              r.WaitReady,
		CronRestart:            r.CronRestart,
		Watch:                  r.Watch,
		HealthCheckCmd:         r.HealthCheckCmd,
		HealthCheckInterval:    liftDur(r.HealthIntervalMs, config.DefaultHealthInterval),
		HealthCheckTimeout:     liftDur(r.HealthTimeoutMs, config.DefaultHealthTimeout),
		HealthCheckRetries:     liftInt(r.HealthCheckRetries, config.DefaultHealthRetries),
		Treekill:               r.Treekill,
		Time:                   r.Time,
		LogDateFormat:          r.LogDateFormat,
		OutFile:                r.OutFile,
		ErrorFile:              r.ErrorFile,
		PidFile:                r.PidFile,
		MergeLogs:              r.MergeLogs,
		// Wire (0 = default/legacy, -1 = off) → memory (0 = off).
		LogRotateMaxBytes: memRotateMax(r.LogRotateMaxBytes),
		LogRotateRetain:   memRotateRetain(r.LogRotateRetain),
		Env:               r.Env,
	}
	return Entry{PMID: r.PMID, App: a}
}

// wireRotateMax / memRotateMax convert between the in-memory rotation
// encoding (0 = off, >0 = bytes; config.Default pins 10 MiB) and the dump
// encoding (0 = default or legacy dump, -1 = off, >0 = bytes). memRotateRetain
// lifts legacy zero retains to the pinned default.
func wireRotateMax(mem int64) int64 {
	if mem == 0 {
		return -1 // explicit off must survive the round-trip
	}
	return mem
}

func memRotateMax(wire int64) int64 {
	switch {
	case wire == 0:
		return config.DefaultRotateMax // legacy M3 dump: rotation on
	case wire < 0:
		return 0 // rotation off
	default:
		return wire
	}
}

func memRotateRetain(wire int) int {
	if wire <= 0 {
		return config.DefaultRotateRetain
	}
	return wire
}

// liftDur / liftInt raise zero (absent or legacy-dump) values to the
// pinned defaults. Explicitly saved values round-trip unchanged.
func liftDur(ms int64, def time.Duration) time.Duration {
	if ms <= 0 {
		return def
	}
	return time.Duration(ms) * time.Millisecond
}

func liftInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
