//go:build linux

// Opt-in local HTTP/REST control-plane API (PM0_HTTP_ADDR).
//
// This is a thin JSON/HTTP facade over the SAME control-plane methods the
// gRPC Daemon service exposes (control.go / logtail_linux.go): every handler
// calls the existing Server methods directly, so process-control semantics
// (selector resolution, name families, rolling reload, kill paths, log
// buffering) are identical to the CLI and the gRPC SDK. It is NOT a second
// implementation — it is a transport adapter.
//
// It is OFF by default and bound locally on purpose: enable it with
// PM0_HTTP_ADDR (a TCP "host:port" or "unix:<path>" for a local socket), or
// with a PM0_HTTP_ADDR line in $PM0_HOME/config (what the installer writes).
// The intended topology is a co-located backend service (dashboard API)
// talking to this on the same host and re-exposing it with its own auth —
// never a public bind. See docs/http-api.md for the full contract.
//
// Routes (all under /api/v1):
//
//	GET    /health                         daemon metadata (Ping)
//	GET    /processes                      list all apps (PM2 jlist JSON)
//	POST   /processes                      start (spec | {"specs":[...]} | [spec,...])
//	GET    /processes/{target}             describe one app (PM2 jlist JSON)
//	DELETE /processes/{target}             stop + forget
//	POST   /processes/{target}/restart     restart (body {"env":{...}} => --update-env)
//	POST   /processes/{target}/reload      rolling, zero-downtime
//	POST   /processes/{target}/stop        stop (state kept)
//	POST   /processes/{target}/scale       body {"delta": N}
//	GET    /processes/{target}/logs        Server-Sent Events log stream
//	POST   /scale                          body {"name":"x","delta":N}
//	POST   /daemon/save                    persist the app set (dump.json)
//	POST   /daemon/resurrect               adopt/start from dump.json
//
// {target} is a pm_id ("3"), an app name ("web", matches the whole -0..-N
// family), or "all". Errors carry the gRPC status mapped onto HTTP codes and
// a JSON body {"error":"..."}.
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/store"
	"github.com/pm0/pm0/pkg/client"
)

// httpAPIEnv configures the local HTTP API bind address. Value: "host:port"
// for TCP, "unix:<path>" for a local socket, or off/none/disabled to disable.
const httpAPIEnv = "PM0_HTTP_ADDR"

// httpAPIAddr resolves the effective HTTP API bind address. The API is OFF
// unless configured: the PM0_HTTP_ADDR environment variable wins, then the
// PM0_HTTP_ADDR line in $PM0_HOME/config. An unset/empty value (or
// off/none/disabled/false/0) means disabled.
func httpAPIAddr() (string, bool) {
	v := strings.TrimSpace(os.Getenv(httpAPIEnv))
	if v == "" {
		v = strings.TrimSpace(store.ConfigValue(httpAPIEnv))
	}
	switch strings.ToLower(v) {
	case "", "off", "none", "disabled", "disable", "false", "0":
		return "", false
	}
	return v, true
}

// maxBodyBytes caps request bodies (specs, env maps) — control-plane bodies
// are tiny; anything larger is a mistake worth rejecting early.
const maxBodyBytes = 1 << 20

// serveHTTP binds addr and serves the REST API until the listener fails.
func (s *Server) serveHTTP(addr string) error {
	lis, err := httpListener(addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.httpHandler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: log streaming is long-lived. IdleTimeout only
		// closes truly idle keep-alive connections.
		IdleTimeout: 120 * time.Second,
	}
	fmt.Printf("pm0 daemon: http api listening on %s\n", addr)
	return srv.Serve(lis)
}

// httpListener opens the listener for addr: "unix:<path>" gets a 0600 local
// socket (never network-reachable), anything else is a TCP bind.
func httpListener(addr string) (net.Listener, error) {
	if strings.HasPrefix(addr, "unix:") {
		path := strings.TrimPrefix(addr, "unix:")
		_ = os.Remove(path) // stale socket from a previous run
		lis, err := net.Listen("unix", path)
		if err != nil {
			return nil, fmt.Errorf("http api: listen unix %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			_ = lis.Close()
			return nil, fmt.Errorf("http api: chmod %s: %w", path, err)
		}
		return lis, nil
	}
	return net.Listen("tcp", addr)
}

// httpHandler wires the routes. Go 1.22+ ServeMux method+wildcard patterns
// keep the routing declarative and the handlers free of manual parsing.
func (s *Server) httpHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", s.httpHealth)
	mux.HandleFunc("GET /api/v1/ping", s.httpHealth)

	mux.HandleFunc("GET /api/v1/processes", s.httpList)
	mux.HandleFunc("POST /api/v1/processes", s.httpStart)
	mux.HandleFunc("GET /api/v1/processes/{target}", s.httpDescribe)
	mux.HandleFunc("DELETE /api/v1/processes/{target}", s.httpDelete)
	mux.HandleFunc("POST /api/v1/processes/{target}/restart", s.httpRestart)
	mux.HandleFunc("POST /api/v1/processes/{target}/reload", s.httpReload)
	mux.HandleFunc("POST /api/v1/processes/{target}/stop", s.httpStop)
	mux.HandleFunc("POST /api/v1/processes/{target}/scale", s.httpScaleByPath)
	mux.HandleFunc("GET /api/v1/processes/{target}/logs", s.httpLogs)

	mux.HandleFunc("POST /api/v1/scale", s.httpScale)

	mux.HandleFunc("POST /api/v1/daemon/save", s.httpSave)
	mux.HandleFunc("POST /api/v1/daemon/resurrect", s.httpResurrect)

	return mux
}

// --- handlers ---------------------------------------------------------------

func (s *Server) httpHealth(w http.ResponseWriter, r *http.Request) {
	resp, err := s.Ping(r.Context(), &v1.PingRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"version":        resp.GetVersion(),
		"commit":         resp.GetCommit(),
		"pid":            resp.GetPid(),
		"kill_mode":      resp.GetKillMode(),
		"kernel_release": resp.GetKernelRelease(),
		"cgroup_warning": resp.GetCgroupWarning(),
	})
}

// httpList returns every app as a PM2-compatible jlist array (the same JSON
// `pm0 jlist` prints, via the shared renderer).
func (s *Server) httpList(w http.ResponseWriter, r *http.Request) {
	resp, err := s.ListProcesses(r.Context(), &v1.ListProcessesRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}
	b, err := client.JlistRenderer{}.RenderAll(resp.GetProcesses())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRawJSON(w, http.StatusOK, b)
}

func (s *Server) httpDescribe(w http.ResponseWriter, r *http.Request) {
	sel := selectorFromTarget(r.PathValue("target"))
	resp, err := s.DescribeProcess(r.Context(), &v1.DescribeProcessRequest{Selector: sel})
	if err != nil {
		writeErr(w, err)
		return
	}
	b, err := client.JlistRenderer{}.Render(resp.GetProcess())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeRawJSON(w, http.StatusOK, b)
}

func (s *Server) httpStart(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}
	specs, err := parseSpecs(body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	resp, err := s.StartProcess(r.Context(), &v1.StartProcessRequest{Specs: specs})
	// StartProcess may return processes AND an error when part of a batch
	// failed to spawn (failures are still registered, parked errored). Only
	// a total failure with nothing registered is an HTTP error.
	if err != nil && len(resp.GetProcesses()) == 0 {
		writeErr(w, err)
		return
	}
	b, rerr := client.JlistRenderer{}.RenderAll(resp.GetProcesses())
	if rerr != nil {
		writeErr(w, rerr)
		return
	}
	writeRawJSON(w, http.StatusOK, b)
}

func (s *Server) httpStop(w http.ResponseWriter, r *http.Request) {
	sel := selectorFromTarget(r.PathValue("target"))
	resp, err := s.StopProcess(r.Context(), &v1.StopProcessRequest{Selector: sel})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeAffected(w, resp.GetAffectedPmIds())
}

func (s *Server) httpRestart(w http.ResponseWriter, r *http.Request) {
	sel := selectorFromTarget(r.PathValue("target"))
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}
	var updated *v1.ProcessSpec
	if len(bytes.TrimSpace(body)) > 0 {
		sp := &v1.ProcessSpec{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, sp); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid spec body: " + err.Error()})
			return
		}
		updated = sp
	}
	resp, err := s.RestartProcess(r.Context(), &v1.RestartProcessRequest{Selector: sel, UpdatedSpec: updated})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeAffected(w, resp.GetAffectedPmIds())
}

func (s *Server) httpReload(w http.ResponseWriter, r *http.Request) {
	sel := selectorFromTarget(r.PathValue("target"))
	resp, err := s.ReloadProcess(r.Context(), &v1.ReloadProcessRequest{Selector: sel})
	// ReloadProcess can return affected ids alongside a partial error; only a
	// total failure (nothing rolled) is an HTTP error.
	if err != nil && len(resp.GetAffectedPmIds()) == 0 {
		writeErr(w, err)
		return
	}
	writeAffected(w, resp.GetAffectedPmIds())
}

func (s *Server) httpDelete(w http.ResponseWriter, r *http.Request) {
	sel := selectorFromTarget(r.PathValue("target"))
	resp, err := s.DeleteProcess(r.Context(), &v1.DeleteProcessRequest{Selector: sel})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeAffected(w, resp.GetAffectedPmIds())
}

// httpScaleByPath reads the delta from the body for POST
// /processes/{target}/scale (name comes from the path).
func (s *Server) httpScaleByPath(w http.ResponseWriter, r *http.Request) {
	s.scaleCore(w, r, r.PathValue("target"))
}

// httpScale reads {"name","delta"} for POST /scale.
func (s *Server) httpScale(w http.ResponseWriter, r *http.Request) {
	s.scaleCore(w, r, "")
}

// scaleCore centralizes scale handling: nameFromPath wins when set,
// otherwise the body's "name" is used.
func (s *Server) scaleCore(w http.ResponseWriter, r *http.Request, nameFromPath string) {
	var req struct {
		Name  string `json:"name"`
		Delta int32  `json:"delta"`
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "read body: " + err.Error()})
		return
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json: " + err.Error()})
			return
		}
	}
	name := nameFromPath
	if name == "" {
		name = req.Name
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "scale: app name required"})
		return
	}
	resp, err := s.ScaleProcess(r.Context(), &v1.ScaleProcessRequest{Name: name, Delta: req.Delta})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeAffected(w, resp.GetAffectedPmIds())
}

// httpLogs streams app logs to the client as Server-Sent Events. It reuses
// the exact StreamLogs pipeline by adapting the SSE response into the gRPC
// server-stream interface (StreamLogs only ever calls Send and Context).
//
// Query params: lines (backlog, default 100), follow (default true),
// stderr (default false), raw (default false).
func (s *Server) httpLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "streaming unsupported"})
		return
	}
	sel := selectorFromTarget(r.PathValue("target"))
	q := r.URL.Query()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Opening comment so clients/proxies flush headers immediately.
	_, _ = io.WriteString(w, ": pm0 log stream\n\n")
	flusher.Flush()

	stream := &sseStream{ctx: r.Context(), w: w, flusher: flusher}
	req := &v1.StreamLogsRequest{
		Selector:      sel,
		Lines:         int32(atoiDefault(q.Get("lines"), 100)),
		IncludeStderr: queryBool(q.Get("stderr"), false),
		Raw:           queryBool(q.Get("raw"), false),
		Follow:        queryBool(q.Get("follow"), true),
	}
	if err := s.StreamLogs(req, stream); err != nil {
		payload, _ := json.Marshal(map[string]string{"error": err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
		flusher.Flush()
	}
}

func (s *Server) httpSave(w http.ResponseWriter, r *http.Request) {
	resp, err := s.Save(r.Context(), &v1.SaveRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"dump_path":   resp.GetDumpPath(),
		"saved_count": resp.GetSavedCount(),
	})
}

func (s *Server) httpResurrect(w http.ResponseWriter, r *http.Request) {
	resp, err := s.Resurrect(r.Context(), &v1.ResurrectRequest{})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"started_count": resp.GetStartedCount(),
		"errored_count": resp.GetErroredCount(),
	})
}

// --- SSE stream adapter -----------------------------------------------------

// sseStream implements v1.Daemon_StreamLogsServer (= grpc.ServerStreamingServer
// [LogLine]) on top of an http.ResponseWriter. StreamLogs only ever calls
// Send and Context; the embedded (nil) grpc.ServerStream satisfies the rest
// of the interface and would panic if some future code path called it — a
// loud failure, better than silently dropping frames.
type sseStream struct {
	grpc.ServerStream
	ctx     context.Context
	w       http.ResponseWriter
	flusher http.Flusher
}

func (s *sseStream) Context() context.Context { return s.ctx }

func (s *sseStream) Send(l *v1.LogLine) error {
	payload, err := json.Marshal(struct {
		PMID        int32  `json:"pm_id"`
		Name        string `json:"name"`
		Stream      string `json:"stream"`
		Data        string `json:"data"`
		TimestampMs int64  `json:"timestamp_ms,omitempty"`
	}{
		PMID:        l.GetPmId(),
		Name:        l.GetName(),
		Stream:      logStreamName(l),
		Data:        string(l.GetData()),
		TimestampMs: l.GetTimestampMs(),
	})
	if err != nil {
		return err
	}
	// SSE frame: "data: <json>\n\n" (JSON escapes embedded newlines).
	if _, err := s.w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := s.w.Write(payload); err != nil {
		return err
	}
	if _, err := s.w.Write([]byte("\n\n")); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func logStreamName(l *v1.LogLine) string {
	if l.GetStream() == v1.LogLine_STREAM_STDERR {
		return "stderr"
	}
	return "stdout"
}

// --- request helpers --------------------------------------------------------

// selectorFromTarget maps a path target onto a Selector: "all"/"" => all,
// a run of digits => pm_id, anything else => name (resolves the whole
// -0..-N family server-side, exactly like the CLI).
func selectorFromTarget(target string) *v1.Selector {
	if target == "" || target == "all" {
		return &v1.Selector{All: true}
	}
	if isAllDigits(target) {
		n, err := strconv.Atoi(target)
		if err == nil {
			return &v1.Selector{PmIds: []int32{int32(n)}}
		}
	}
	return &v1.Selector{Names: []string{target}}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// parseSpecs accepts three body shapes for POST /processes:
//   - a single spec object: {"script":"app.js","name":"web",...}
//   - a wrapper object:    {"specs":[ {...}, {...} ]}
//   - a bare array:        [ {...}, {...} ]
//
// Each element is decoded with protojson, so both snake_case (script,
// max_restarts) and lowerCamelCase (maxRestarts) field names work, matching
// the daemon.proto contract. Unknown fields are ignored for forward
// compatibility.
func parseSpecs(body []byte) ([]*v1.ProcessSpec, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("empty request body")
	}
	switch trimmed[0] {
	case '[':
		var raw []json.RawMessage
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, fmt.Errorf("invalid spec array: %w", err)
		}
		return unmarshalSpecs(raw)
	case '{':
		var probe struct {
			Specs []json.RawMessage `json:"specs"`
		}
		if err := json.Unmarshal(trimmed, &probe); err == nil && len(probe.Specs) > 0 {
			return unmarshalSpecs(probe.Specs)
		}
		sp := &v1.ProcessSpec{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(trimmed, sp); err != nil {
			return nil, fmt.Errorf("invalid spec: %w", err)
		}
		return []*v1.ProcessSpec{sp}, nil
	default:
		return nil, errors.New("request body must be a JSON spec object or array")
	}
}

func unmarshalSpecs(raw []json.RawMessage) ([]*v1.ProcessSpec, error) {
	out := make([]*v1.ProcessSpec, 0, len(raw))
	for i, r := range raw {
		sp := &v1.ProcessSpec{}
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(r, sp); err != nil {
			return nil, fmt.Errorf("specs[%d]: %w", i, err)
		}
		out = append(out, sp)
	}
	return out, nil
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func queryBool(s string, def bool) bool {
	if s == "" {
		return def
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return def
	}
	return b
}

// --- response helpers -------------------------------------------------------

func writeAffected(w http.ResponseWriter, ids []int32) {
	if ids == nil {
		ids = []int32{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "affected_pm_ids": ids})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeRawJSON(w http.ResponseWriter, code int, b []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b)
}

// writeErr maps a gRPC status onto an HTTP status and emits {"error": msg}
// (the gRPC message, without the "rpc error: code = ..." wrapper).
func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	msg := err.Error()
	if st, ok := status.FromError(err); ok {
		msg = st.Message()
		switch st.Code() {
		case codes.InvalidArgument:
			code = http.StatusBadRequest
		case codes.NotFound:
			code = http.StatusNotFound
		case codes.AlreadyExists:
			code = http.StatusConflict
		case codes.PermissionDenied:
			code = http.StatusForbidden
		case codes.Unauthenticated:
			code = http.StatusUnauthorized
		case codes.Unimplemented:
			code = http.StatusNotImplemented
		case codes.ResourceExhausted:
			code = http.StatusTooManyRequests
		case codes.Unavailable:
			code = http.StatusServiceUnavailable
		case codes.DeadlineExceeded:
			code = http.StatusGatewayTimeout
		}
	}
	writeJSON(w, code, map[string]any{"error": msg})
}
