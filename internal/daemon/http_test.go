//go:build linux

// Tests for the opt-in HTTP control-plane API. Unit tests cover the pure
// helpers (selector mapping, spec parsing, error mapping); e2e tests boot a
// real Server (via the daemon_test.go harness) and drive the handler through
// httptest against real fakechild processes.
package daemon

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/store"
	"github.com/pm0/pm0/pkg/client"
	"github.com/pm0/pm0/test/harness"
)

// startHTTP boots a Server (fresh home) and serves its HTTP handler through
// httptest, returning the client for gRPC-side assertions.
func startHTTP(t *testing.T) (*Server, *client.Client, *httptest.Server) {
	t.Helper()
	srv, c := startServer(t)
	ts := httptest.NewServer(srv.httpHandler())
	t.Cleanup(ts.Close)
	return srv, c, ts
}

func httpGet(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func httpDo(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestSelectorFromTarget(t *testing.T) {
	cases := []struct {
		in       string
		wantAll  bool
		wantID   int32
		wantName string
	}{
		{"", true, 0, ""},
		{"all", true, 0, ""},
		{"42", false, 42, ""},
		{"web", false, 0, "web"},
		{"web-3", false, 0, "web-3"},
	}
	for _, tc := range cases {
		got := selectorFromTarget(tc.in)
		if got.GetAll() != tc.wantAll {
			t.Errorf("selectorFromTarget(%q).All = %v, want %v", tc.in, got.GetAll(), tc.wantAll)
		}
		ids := got.GetPmIds()
		switch {
		case tc.wantID != 0 && (len(ids) != 1 || ids[0] != tc.wantID):
			t.Errorf("selectorFromTarget(%q).PmIds = %v, want [%d]", tc.in, ids, tc.wantID)
		case tc.wantID == 0 && len(ids) != 0:
			t.Errorf("selectorFromTarget(%q).PmIds = %v, want none", tc.in, ids)
		}
		names := got.GetNames()
		if tc.wantName == "" && len(names) != 0 {
			t.Errorf("selectorFromTarget(%q).Names = %v, want none", tc.in, names)
		}
		if tc.wantName != "" && (len(names) != 1 || names[0] != tc.wantName) {
			t.Errorf("selectorFromTarget(%q).Names = %v, want [%q]", tc.in, names, tc.wantName)
		}
	}
}

func TestParseSpecs(t *testing.T) {
	// Single spec object, snake_case.
	specs, err := parseSpecs([]byte(`{"script":"a.js","name":"web","max_restarts":5}`))
	if err != nil {
		t.Fatalf("single object: %v", err)
	}
	if len(specs) != 1 || specs[0].GetScript() != "a.js" || specs[0].GetName() != "web" || specs[0].GetMaxRestarts() != 5 {
		t.Fatalf("single object decoded wrong: %+v", specs)
	}

	// lowerCamelCase field name.
	if specs, err = parseSpecs([]byte(`{"script":"a.js","maxRestarts":7}`)); err != nil || specs[0].GetMaxRestarts() != 7 {
		t.Fatalf("camelCase decode: specs=%v err=%v", specs, err)
	}

	// Wrapper object.
	specs, err = parseSpecs([]byte(`{"specs":[{"script":"a.js"},{"script":"b.js"}]}`))
	if err != nil || len(specs) != 2 {
		t.Fatalf("wrapper: specs=%v err=%v", specs, err)
	}

	// Bare array.
	specs, err = parseSpecs([]byte(`[{"script":"a.js"},{"script":"b.js"}]`))
	if err != nil || len(specs) != 2 {
		t.Fatalf("array: specs=%v err=%v", specs, err)
	}

	// Unknown fields are tolerated.
	if _, err = parseSpecs([]byte(`{"script":"a.js","not_a_field":1}`)); err != nil {
		t.Fatalf("unknown field should be ignored: %v", err)
	}

	// Errors.
	if _, err = parseSpecs(nil); err == nil {
		t.Error("empty body should error")
	}
	if _, err = parseSpecs([]byte(`nope`)); err == nil {
		t.Error("non-object/array body should error")
	}
}

func TestWriteErrMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{status.Error(codes.NotFound, "no matching app"), http.StatusNotFound},
		{status.Error(codes.InvalidArgument, "bad"), http.StatusBadRequest},
		{status.Error(codes.Internal, "boom"), http.StatusInternalServerError},
		{errors.New("plain"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		writeErr(rec, tc.err)
		if rec.Code != tc.want {
			t.Errorf("writeErr(%v) code = %d, want %d", tc.err, rec.Code, tc.want)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("writeErr(%v) body = %q, want an error field", tc.err, rec.Body.String())
		}
	}
}

func TestHTTPHealth(t *testing.T) {
	_, _, ts := startHTTP(t)
	code, body := httpGet(t, ts.URL+"/api/v1/health")
	if code != http.StatusOK {
		t.Fatalf("health code = %d, body = %s", code, body)
	}
	if !strings.Contains(body, `"ok":true`) {
		t.Fatalf("health body = %s, want ok true", body)
	}
}

func TestHTTPProcessesEmptyIsArray(t *testing.T) {
	_, _, ts := startHTTP(t)
	code, body := httpGet(t, ts.URL+"/api/v1/processes")
	if code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty list = %d %q, want 200 []", code, body)
	}
}

func TestHTTPStartListDescribeActions(t *testing.T) {
	_, c, ts := startHTTP(t)
	name := harness.UniqueName("http")

	// POST /processes — start via the HTTP API.
	code, body := httpDo(t, http.MethodPost, ts.URL+"/api/v1/processes",
		`{"script":"`+fakechild+`","name":"`+name+`","args":["--exit-after","60000"],"autorestart":true}`)
	if code != http.StatusOK {
		t.Fatalf("start code = %d, body = %s", code, body)
	}

	// GET /processes — the new app must be listed.
	waitFor(t, 5*time.Second, "app listed over HTTP", func() bool {
		code, body := httpGet(t, ts.URL+"/api/v1/processes")
		return code == http.StatusOK && strings.Contains(body, `"name":"`+name+`"`)
	})

	// GET /processes/{name} — describe.
	code, body = httpGet(t, ts.URL+"/api/v1/processes/"+name)
	if code != http.StatusOK || !strings.Contains(body, `"pm_id"`) {
		t.Fatalf("describe code = %d, body = %s", code, body)
	}

	// Describe a missing app → 404.
	if code, _ = httpGet(t, ts.URL+"/api/v1/processes/does-not-exist"); code != http.StatusNotFound {
		t.Fatalf("describe missing code = %d, want 404", code)
	}

	// POST restart.
	if code, body = httpDo(t, http.MethodPost, ts.URL+"/api/v1/processes/"+name+"/restart", ""); code != http.StatusOK {
		t.Fatalf("restart code = %d, body = %s", code, body)
	}
	if !strings.Contains(body, "affected_pm_ids") {
		t.Fatalf("restart body = %s, want affected_pm_ids", body)
	}

	// POST stop, then confirm stopped via gRPC.
	if code, body = httpDo(t, http.MethodPost, ts.URL+"/api/v1/processes/"+name+"/stop", ""); code != http.StatusOK {
		t.Fatalf("stop code = %d, body = %s", code, body)
	}
	waitFor(t, 5*time.Second, "app stopped", func() bool {
		for _, p := range jlistOf(t, c) {
			if p["name"] == name {
				return p["pm2_env"].(map[string]any)["status"] == "stopped"
			}
		}
		return false
	})

	// DELETE — remove from supervision.
	if code, body = httpDo(t, http.MethodDelete, ts.URL+"/api/v1/processes/"+name, ""); code != http.StatusOK {
		t.Fatalf("delete code = %d, body = %s", code, body)
	}
	waitFor(t, 5*time.Second, "app deleted", func() bool {
		for _, p := range jlistOf(t, c) {
			if p["name"] == name {
				return false
			}
		}
		return true
	})
}

func TestHTTPLogsSSE(t *testing.T) {
	_, c, ts := startHTTP(t)
	name := harness.UniqueName("sse")
	// fakechild floods stdout ("out-<i>") at startup, then sleeps.
	if _, err := c.Start([]*v1.ProcessSpec{{
		Name:        name,
		Script:      fakechild,
		Args:        []string{"--flood", "5"},
		Autorestart: true,
		Cwd:         t.TempDir(),
	}}); err != nil {
		t.Fatalf("start: %v", err)
	}

	// follow=false: backlog only, so the response completes.
	waitFor(t, 6*time.Second, "log lines over HTTP SSE", func() bool {
		code, body := httpGet(t, ts.URL+"/api/v1/processes/"+name+"/logs?lines=20&follow=false")
		return code == http.StatusOK && strings.Contains(body, "data: ") && strings.Contains(body, "out-1")
	})
}

func TestHTTPAPIAddrResolution(t *testing.T) {
	t.Setenv("PM0_HOME", t.TempDir())

	// Unset env + no config => OFF (the default).
	t.Setenv("PM0_HTTP_ADDR", "")
	if _, on := httpAPIAddr(); on {
		t.Fatal("unset should mean disabled by default")
	}

	// Explicit disable values.
	for _, v := range []string{"off", "OFF", "none", "disabled", "0", "false"} {
		t.Setenv("PM0_HTTP_ADDR", v)
		if _, on := httpAPIAddr(); on {
			t.Errorf("%q should disable the API", v)
		}
	}

	// Env override wins and enables.
	t.Setenv("PM0_HTTP_ADDR", "127.0.0.1:1234")
	if addr, on := httpAPIAddr(); !on || addr != "127.0.0.1:1234" {
		t.Fatalf("tcp override = (%q,%v)", addr, on)
	}
	t.Setenv("PM0_HTTP_ADDR", "unix:/run/x.sock")
	if addr, on := httpAPIAddr(); !on || addr != "unix:/run/x.sock" {
		t.Fatalf("unix override = (%q,%v)", addr, on)
	}

	// Config file is consulted only when the env var is unset.
	t.Setenv("PM0_HTTP_ADDR", "")
	if err := os.WriteFile(store.ConfigPath(),
		[]byte("# pm0 config\nPM0_HTTP_ADDR=127.0.0.1:9615\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if addr, on := httpAPIAddr(); !on || addr != "127.0.0.1:9615" {
		t.Fatalf("config file = (%q,%v), want on 127.0.0.1:9615", addr, on)
	}

	// Env still wins over the config file.
	t.Setenv("PM0_HTTP_ADDR", "127.0.0.1:7777")
	if addr, on := httpAPIAddr(); !on || addr != "127.0.0.1:7777" {
		t.Fatalf("env over config = (%q,%v), want on 127.0.0.1:7777", addr, on)
	}
}
