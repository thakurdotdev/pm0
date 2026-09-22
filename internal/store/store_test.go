package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pm0/pm0/internal/config"
)

func testHome(t *testing.T) {
	t.Helper()
	t.Setenv("PM0_HOME", t.TempDir())
	if err := EnsureHome(); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
}

func sampleEntry(pmid int, name string) Entry {
	cfg := config.DefaultFor(name, "/opt/app/"+name+".js")
	cfg.Args = []string{"--port", "3000"}
	cfg.MinUptime = 1500 * time.Millisecond
	cfg.KillTimeout = 900 * time.Millisecond
	cfg.Env = map[string]string{"K": "v"}
	cfg.Instances = 2
	return Entry{PMID: pmid, App: cfg}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	testHome(t)

	d := Dump{Apps: []Entry{sampleEntry(0, "web"), sampleEntry(3, "api")}}
	if err := Save(d); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok, err := Load()
	if err != nil || !ok {
		t.Fatalf("Load: ok=%v err=%v", ok, err)
	}
	if got.Version != DumpVersion {
		t.Errorf("version = %d", got.Version)
	}
	if len(got.Apps) != 2 {
		t.Fatalf("apps = %d, want 2", len(got.Apps))
	}
	if got.Apps[0].PMID != 0 || got.Apps[1].PMID != 3 {
		t.Errorf("pm_ids = %d,%d", got.Apps[0].PMID, got.Apps[1].PMID)
	}
	a := got.Apps[1].App
	if a.Name != "api" || a.MinUptime != 1500*time.Millisecond || a.KillTimeout != 900*time.Millisecond {
		t.Errorf("config round trip drifted: %+v", a)
	}
	if a.Env["K"] != "v" || len(a.Args) != 2 {
		t.Errorf("args/env round trip: %+v", a)
	}
}

func TestLoadMissingDump(t *testing.T) {
	testHome(t)
	_, ok, err := Load()
	if ok || err != nil {
		t.Fatalf("missing dump: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestSaveAtomicNoTempLeft(t *testing.T) {
	testHome(t)
	if err := Save(Dump{Apps: []Entry{sampleEntry(0, "x")}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(Home(), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestLogPathsLayout(t *testing.T) {
	testHome(t)
	out, errF, pid := LogPaths("web", 4)
	if want := filepath.Join(Home(), "logs", "web-out.log"); out != want {
		t.Errorf("out = %s, want %s", out, want)
	}
	if want := filepath.Join(Home(), "logs", "web-error.log"); errF != want {
		t.Errorf("err = %s, want %s", errF, want)
	}
	if want := filepath.Join(Home(), "pids", "web-4.pid"); pid != want {
		t.Errorf("pid = %s, want %s", pid, want)
	}
}

func TestLoadCorruptDumpFailsLoudly(t *testing.T) {
	testHome(t)
	if err := os.WriteFile(DumpPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load()
	if err == nil {
		t.Fatal("corrupt dump must error, not silently pass")
	}
}

func TestHomeEnvOverride(t *testing.T) {
	t.Setenv("PM0_HOME", "/custom/pm0-home")
	if got := Home(); got != "/custom/pm0-home" {
		t.Errorf("Home() = %q", got)
	}
}
