//go:build linux

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pm0/pm0/internal/config"
)

// The dump is the wire for rotation knobs: memory 0 = off must round-trip
// as -1 (never as 0, which means "legacy default"), and legacy dumps
// (field absent → 0) must lift to the pinned defaults. Regression for the
// M4 knob surface (divergence 12).
func TestDumpRotateKnobRoundTrip(t *testing.T) {
	setHome(t)

	off := config.DefaultFor("off", "/bin/x")
	off.LogRotateMaxBytes = 0 // rotation explicitly off
	custom := config.DefaultFor("custom", "/bin/x")
	custom.LogRotateMaxBytes = 4096
	custom.LogRotateRetain = 5
	deflt := config.DefaultFor("deflt", "/bin/x") // untouched defaults

	if err := Save(Dump{Apps: []Entry{
		{PMID: 0, App: off},
		{PMID: 1, App: custom},
		{PMID: 2, App: deflt},
	}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Encoding on disk: off → -1, custom → bytes, default → bytes.
	rawB, err := os.ReadFile(DumpPath())
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	raw := string(rawB)
	if !strings.Contains(raw, `"log_rotate_max_bytes": -1`) {
		t.Errorf("explicit off must encode as -1:\n%s", raw)
	}
	if !strings.Contains(raw, `"log_rotate_max_bytes": 4096`) {
		t.Errorf("custom max must encode as bytes:\n%s", raw)
	}

	loaded, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("load: %v ok=%v", err, ok)
	}
	apps := map[string]config.App{}
	for _, e := range loaded.Apps {
		apps[e.App.Name] = e.App
	}
	if got := apps["off"].LogRotateMaxBytes; got != 0 {
		t.Errorf("off: got %d, want 0 (rotation off)", got)
	}
	if got := apps["custom"].LogRotateMaxBytes; got != 4096 || apps["custom"].LogRotateRetain != 5 {
		t.Errorf("custom: got %d retain %d", got, apps["custom"].LogRotateRetain)
	}
	if got := apps["deflt"].LogRotateMaxBytes; got != config.DefaultRotateMax {
		t.Errorf("default: got %d, want %d", got, config.DefaultRotateMax)
	}
	if got := apps["deflt"].LogRotateRetain; got != config.DefaultRotateRetain {
		t.Errorf("default retain: got %d, want %d", got, config.DefaultRotateRetain)
	}
}

// A legacy M3 dump (no knob fields at all) must come back with rotation ON
// at the pinned defaults — rotation shipping by default survives the
// schema's silent-absent case.
func TestLegacyDumpLiftsKnobDefaults(t *testing.T) {
	setHome(t)
	legacy := `{
  "version": 1,
  "redact_env": false,
  "apps": [
    {
      "pm_id": 0,
      "name": "legacy",
      "pm_exec_path": "/bin/x",
      "env": {}
    }
  ]
}`
	writeDump(t, legacy)

	loaded, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("load: %v ok=%v", err, ok)
	}
	if len(loaded.Apps) != 1 {
		t.Fatalf("apps: %d", len(loaded.Apps))
	}
	a := loaded.Apps[0].App
	if a.LogRotateMaxBytes != config.DefaultRotateMax || a.LogRotateRetain != config.DefaultRotateRetain {
		t.Errorf("legacy dump must lift to defaults, got %d/%d", a.LogRotateMaxBytes, a.LogRotateRetain)
	}
}

// --- helpers --------------------------------------------------------------

func setHome(t *testing.T) {
	t.Helper()
	t.Setenv("PM0_HOME", filepath.Join(t.TempDir(), "pm0-home"))
}

func writeDump(t *testing.T, body string) {
	t.Helper()
	if err := EnsureHome(); err != nil {
		t.Fatalf("ensure home: %v", err)
	}
	if err := os.WriteFile(DumpPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("write dump: %v", err)
	}
}
