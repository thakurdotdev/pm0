package client

import (
	"bytes"
	"strings"
	"testing"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
)

func TestVisibleLen(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"hello", 5},
		{"\033[1;32monline\033[0m", 6},
		{"\033[1;31merrored\033[0m", 7},
		{"\033[90m•\033[0m", 1},
		{"\033[1;32m● online\033[0m", 8},
		{"\033[1;31m✖ errored\033[0m", 9},
		{"12.5%", 5},
		{"", 0},
	}

	for _, tc := range tests {
		got := visibleLen(tc.input)
		if got != tc.want {
			t.Errorf("visibleLen(%q) = %d; want %d", tc.input, got, tc.want)
		}
	}
}

func TestFormatPorts(t *testing.T) {
	// No ports
	raw, disp := formatPorts(nil, false)
	if raw != "-" || disp != "-" {
		t.Errorf("expected -/-, got %q/%q", raw, disp)
	}

	// Single port
	raw, _ = formatPorts([]int{3000}, false)
	if raw != ":3000" {
		t.Errorf("expected :3000, got %q", raw)
	}

	// Two ports
	raw, _ = formatPorts([]int{2222, 8080}, false)
	if raw != ":2222, :8080" {
		t.Errorf("expected :2222, :8080, got %q", raw)
	}

	// 3+ ports compact format
	raw, _ = formatPorts([]int{2222, 8088, 9090}, false)
	if raw != ":2222 (+2)" {
		t.Errorf("expected :2222 (+2), got %q", raw)
	}
}

func TestWriteListTable(t *testing.T) {
	nowMs := time.Now().Add(-5 * time.Minute).UnixMilli()

	processes := []*v1.ProcessInfo{
		{
			PmId:   0,
			Name:   "app-one",
			Pid:    1234,
			Status: v1.ProcessStatus_PROCESS_STATUS_ONLINE,
			Pm2Env: &v1.ProcessEnv{
				ExecMode:    "fork_mode",
				PmUptime:    nowMs,
				RestartTime: 2,
			},
			Monit: &v1.Monit{
				Cpu:    1.5,
				Memory: 45 * 1024 * 1024,
			},
		},
		{
			PmId:   1,
			Name:   "app-two",
			Pid:    0,
			Status: v1.ProcessStatus_PROCESS_STATUS_ERRORED,
			Pm2Env: &v1.ProcessEnv{
				ExecMode:    "cluster_mode",
				PmUptime:    0,
				RestartTime: 5,
			},
			Monit: &v1.Monit{
				Cpu:    0.0,
				Memory: 0,
			},
		},
	}

	var buf bytes.Buffer
	WriteListTable(&buf, processes)
	out := buf.String()

	// 1. Must contain unicode box borders
	for _, char := range []string{"┌", "┬", "┐", "├", "┼", "┤", "└", "┴", "┘", "│"} {
		if !strings.Contains(out, char) {
			t.Errorf("expected output to contain border char %q, got:\n%s", char, out)
		}
	}

	// 2. Must contain headers in pm0 order
	headers := []string{"id", "app", "mode", "status", "port", "pid", "uptime", "↺", "cpu", "mem"}
	for _, h := range headers {
		if !strings.Contains(out, h) {
			t.Errorf("expected header %q in output, got:\n%s", h, out)
		}
	}

	// 3. Must contain row content
	expectedContent := []string{"app-one", "1234", "online", "1.5%", "45.0mb", "app-two", "errored"}
	for _, c := range expectedContent {
		if !strings.Contains(out, c) {
			t.Errorf("expected content %q in output, got:\n%s", c, out)
		}
	}

	// 4. Must contain summary metrics
	if !strings.Contains(out, "processes") {
		t.Errorf("expected processes label in summary, got:\n%s", out)
	}
	if !strings.Contains(out, "1 online") {
		t.Errorf("expected online count in summary, got:\n%s", out)
	}
}

func TestWriteListTableEmpty(t *testing.T) {
	var buf bytes.Buffer
	WriteListTable(&buf, nil)
	out := buf.String()

	if !strings.Contains(out, "No processes managed by pm0") {
		t.Errorf("expected empty message, got:\n%s", out)
	}
	if !strings.Contains(out, "0 processes") {
		t.Errorf("expected 0 processes, got:\n%s", out)
	}
}

func TestReadSystemStats(t *testing.T) {
	stats := readSystemStats()
	if stats.CPUCores <= 0 {
		t.Errorf("expected CPUCores > 0, got %d", stats.CPUCores)
	}
	if stats.HasMem && stats.TotalMem == 0 {
		t.Errorf("expected TotalMem > 0 when HasMem is true")
	}
}
