//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pm0/pm0/internal/store"
)

func TestCmdFlush(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PM0_HOME", home)

	logsDir := filepath.Join(home, store.LogsDir)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	app1Out := filepath.Join(logsDir, "app1-out.log")
	app1Err := filepath.Join(logsDir, "app1-error.log")
	app2Out := filepath.Join(logsDir, "app2-out.log")

	if err := os.WriteFile(app1Out, []byte("app1 stdout output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app1Err, []byte("app1 stderr output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app2Out, []byte("app2 stdout output\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 1. Flush specific app: app1
	cmdFlush([]string{"app1"})

	st1, err := os.Stat(app1Out)
	if err != nil || st1.Size() != 0 {
		t.Fatalf("expected app1Out size 0, got size=%d err=%v", st1.Size(), err)
	}
	st1Err, err := os.Stat(app1Err)
	if err != nil || st1Err.Size() != 0 {
		t.Fatalf("expected app1Err size 0, got size=%d err=%v", st1Err.Size(), err)
	}
	st2, err := os.Stat(app2Out)
	if err != nil || st2.Size() == 0 {
		t.Fatalf("expected app2Out size > 0 (not flushed), got size=%d", st2.Size())
	}

	// 2. Flush all
	cmdFlush([]string{"all"})

	st2After, err := os.Stat(app2Out)
	if err != nil || st2After.Size() != 0 {
		t.Fatalf("expected app2Out size 0 after flush all, got size=%d err=%v", st2After.Size(), err)
	}
}
