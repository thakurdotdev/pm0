// jlist.go — the PM2-compatible JSON renderer (docs/compat.md §2).
//
// `pm0 jlist` prints a JSON array where each element is
// { pid, pm_id, name, monit, pm2_env } with the exact key names, types and
// null-ness pm2 emits. Load-bearing details:
//
//   - exit_code is JSON null while running and for signal deaths (§2.2);
//   - args and interpreter_args are [] (never null);
//   - monit.memory is tree RSS bytes, monit.cpu is percent of one core;
//   - treekill echoes true (row 23 divergence), time echoes the flag.
package client

import (
	"bytes"
	"encoding/json"
	"fmt"

	v1 "github.com/pm0/pm0/api/v1"
)

type jsonMonit struct {
	Memory int64   `json:"memory"`
	CPU    float64 `json:"cpu"`
}

type jsonPm2Env struct {
	Name         string   `json:"name"`
	Namespace    string   `json:"namespace"`
	PmID         int32    `json:"pm_id"`
	PmExecPath   string   `json:"pm_exec_path"`
	Args         []string `json:"args"`
	PmCwd        string   `json:"pm_cwd"`
	PmOutLogPath string   `json:"pm_out_log_path"`
	PmErrLogPath string   `json:"pm_err_log_path"`
	PmPidPath    string   `json:"pm_pid_path"`
	// InterpreterArgs / NodeArgs: real pm2 emits both (row 5 alias); we
	// emit interpreter_args and node_args as the same list so scripts
	// reading either key see the value.
	InterpreterArgs []string `json:"interpreter_args"`
	NodeArgs        []string `json:"node_args"`
	// ExecInterpreter: real pm2 names this key exec_interpreter (observed
	// pm2 7.0.4); "interpreter" rides as an alias for the §2.2 doc name.
	ExecInterpreter string `json:"exec_interpreter"`
	Interpreter     string `json:"interpreter"`
	// ExecMode renders pm2's wire form: "fork_mode" / "cluster_mode"
	// (observed pm2); the config value stays "fork"/"cluster".
	ExecMode  string `json:"exec_mode"`
	Instances int32  `json:"instances"`

	Autorestart      bool   `json:"autorestart"`
	MaxRestarts      int32  `json:"max_restarts"`
	MinUptime        int64  `json:"min_uptime"`
	MaxMemoryRestart int64  `json:"max_memory_restart"`
	KillSignal       string `json:"kill_signal"`
	KillTimeout      int32  `json:"kill_timeout"`
	ListenTimeout    int32  `json:"listen_timeout"`
	WaitReady        bool   `json:"wait_ready"`
	CronRestart      string `json:"cron_restart"`

	RestartDelay  int64   `json:"restart_delay"`
	ExpBackoff    int64   `json:"exp_backoff_restart_delay"` // observed pm2 key (number)
	StopExitCodes []int32 `json:"stop_exit_codes"`
	Watch         bool    `json:"watch"`
	Treekill      bool    `json:"treekill"`
	Time          bool    `json:"time"`
	MergeLogs     bool    `json:"merge_logs"`
	LogDateFormat string  `json:"log_date_format"`

	Env map[string]string `json:"env"`

	// Runtime mirror (§2.2).
	Status           string          `json:"status"`
	RestartTime      int32           `json:"restart_time"`
	UnstableRestarts int32           `json:"unstable_restarts"`
	CreatedAt        json.RawMessage `json:"created_at"`
	PmUptime         int64           `json:"pm_uptime"`
	ExitCode         json.RawMessage `json:"exit_code"`
}

type jsonProc struct {
	Pid    int32      `json:"pid"`
	Name   string     `json:"name"`
	PmID   int32      `json:"pm_id"`
	Monit  jsonMonit  `json:"monit"`
	Pm2Env jsonPm2Env `json:"pm2_env"`
}

// JlistRenderer renders ProcessInfo entries into the exact jlist wire
// shape. opts.redactEnv must match the daemon's flag: the daemon already
// redacts pm2_env.env, so nothing happens here by default (kept as the
// single place to re-apply redaction if the daemon flag was off but the
// caller wants a scrubbed print).
type JlistRenderer struct {
	RedactEnv bool
}

// Render converts one ProcessInfo.
func (JlistRenderer) Render(p *v1.ProcessInfo) ([]byte, error) {
	if p.GetPm2Env() == nil {
		return nil, fmt.Errorf("jlist: process %d has no pm2_env", p.GetPmId())
	}
	e := p.GetPm2Env()
	m := p.GetMonit()

	env := jsonProc{
		Pid:   p.GetPid(),
		Name:  p.GetName(),
		PmID:  p.GetPmId(),
		Monit: jsonMonit{Memory: m.GetMemory(), CPU: m.GetCpu()},
		Pm2Env: jsonPm2Env{
			Name:            e.GetName(),
			Namespace:       e.GetNamespace(),
			PmID:            e.GetPmId(),
			PmExecPath:      e.GetPmExecPath(),
			Args:            orEmpty(e.GetArgs()),
			PmCwd:           e.GetPmCwd(),
			PmOutLogPath:    e.GetPmOutLogPath(),
			PmErrLogPath:    e.GetPmErrLogPath(),
			PmPidPath:       e.GetPmPidPath(),
			Interpreter:     e.GetInterpreter(),
			InterpreterArgs: orEmpty(e.GetInterpreterArgs()),
			NodeArgs:        orEmpty(e.GetInterpreterArgs()),
			ExecInterpreter: e.GetInterpreter(),
			ExecMode:        execModeWire(e.GetExecMode()),
			Instances:       e.GetInstances(),

			Autorestart:      e.GetAutorestart(),
			MaxRestarts:      e.GetMaxRestarts(),
			MinUptime:        e.GetMinUptimeMs(),
			MaxMemoryRestart: e.GetMaxMemoryRestartBytes(),
			KillSignal:       e.GetKillSignal(),
			KillTimeout:      e.GetKillTimeoutMs(),
			ListenTimeout:    e.GetListenTimeoutMs(),
			WaitReady:        e.GetWaitReady(),
			CronRestart:      e.GetCronRestart(),

			RestartDelay:  e.GetRestartDelayMs(),
			ExpBackoff:    e.GetExpBackoffRestartDelayMs(),
			StopExitCodes: e.GetStopExitCodes(),
			Watch:         e.GetWatch(),
			Treekill:      e.GetTreekill(),
			Time:          e.GetTime(),
			MergeLogs:     e.GetMergeLogs(),
			LogDateFormat: e.GetLogDateFormat(),

			Env: e.GetEnv(),

			Status:           statusString(p.GetStatus()),
			RestartTime:      e.GetRestartTime(),
			UnstableRestarts: e.GetUnstableRestarts(),
			CreatedAt:        createdAtJSON(e),
			PmUptime:         e.GetPmUptime(),
			ExitCode:         exitCodeJSON(e),
		},
	}
	return json.Marshal(env)
}

// RenderAll renders the full jlist array.
func (r JlistRenderer) RenderAll(ps []*v1.ProcessInfo) ([]byte, error) {
	if len(ps) == 0 {
		return []byte("[]"), nil
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, p := range ps {
		if i > 0 {
			buf.WriteByte(',')
		}
		b, err := r.Render(p)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
	}
	buf.WriteByte(']')
	return buf.Bytes(), nil
}

// statusString maps the enum back to the load-bearing jlist vocabulary
// (§3.1) — the enum names exist only for the typed transport.
func statusString(s v1.ProcessStatus) string {
	switch s {
	case v1.ProcessStatus_PROCESS_STATUS_LAUNCHING:
		return "launching"
	case v1.ProcessStatus_PROCESS_STATUS_ONLINE:
		return "online"
	case v1.ProcessStatus_PROCESS_STATUS_STOPPING:
		return "stopping"
	case v1.ProcessStatus_PROCESS_STATUS_STOPPED:
		return "stopped"
	case v1.ProcessStatus_PROCESS_STATUS_WAITING:
		return "waiting restart" // observed pm2 two-word string (5.x and 7.x)
	case v1.ProcessStatus_PROCESS_STATUS_ERRORED:
		return "errored"
	}
	return "stopped"
}

// createdAtJSON: null after a crash-loop park (observed pm2 sets
// created_at = null there); the daemon carries presence explicitly.
func createdAtJSON(e *v1.ProcessEnv) json.RawMessage {
	if !e.GetCreatedAtPresent() {
		return json.RawMessage("null")
	}
	return json.RawMessage(fmt.Sprintf("%d", e.GetCreatedAt()))
}

// exitCodeJSON: null while running or for signal deaths (§2.2); the daemon
// carries presence explicitly.
func exitCodeJSON(e *v1.ProcessEnv) json.RawMessage {
	if !e.GetExitCodePresent() {
		return json.RawMessage("null")
	}
	return json.RawMessage(fmt.Sprintf("%d", e.GetExitCode()))
}

// execModeWire maps config exec_mode onto pm2's jlist form.
func execModeWire(m string) string {
	switch m {
	case "fork":
		return "fork_mode"
	case "cluster":
		return "cluster_mode"
	}
	return m
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
