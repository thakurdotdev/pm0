package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestShebangResolvesDirectExec(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "tool.py", "#!/usr/bin/env python3\nprint(1)\n")
	cfg := DefaultFor("t", script)

	execPath, execArgs, effective, err := ResolveInterpreter(cfg)
	if err != nil {
		t.Fatalf("ResolveInterpreter: %v", err)
	}
	if execPath != "" || len(execArgs) != 0 {
		t.Errorf("shebang script must exec directly, got %q %v", execPath, execArgs)
	}
	if effective != "none" {
		t.Errorf("effective = %q, want none", effective)
	}
}

func TestJsExtensionResolvesNode(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "app.js", "console.log(1)\n")
	cfg := DefaultFor("t", script)
	cfg.InterpreterArgs = []string{"--harmony"}

	execPath, execArgs, effective, err := ResolveInterpreter(cfg)
	if err != nil {
		t.Fatalf("ResolveInterpreter: %v", err)
	}
	if effective != "node" {
		t.Fatalf("effective = %q, want node", effective)
	}
	if execPath != "node" {
		t.Errorf("execPath = %q, want node", execPath)
	}
	// Script must ride as first arg after interpreter args (row 3/5).
	want := []string{"--harmony", script}
	if len(execArgs) != 2 || execArgs[0] != want[0] || execArgs[1] != want[1] {
		t.Errorf("execArgs = %v, want %v", execArgs, want)
	}
}

func TestExplicitInterpreter(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "app.txt", "hello\n")
	cfg := DefaultFor("t", script)
	cfg.Interpreter = "python3"

	execPath, execArgs, effective, err := ResolveInterpreter(cfg)
	if err != nil {
		t.Fatalf("ResolveInterpreter: %v", err)
	}
	if effective != "python3" || execPath != "python3" {
		t.Errorf("effective=%q execPath=%q", effective, execPath)
	}
	if len(execArgs) != 1 || execArgs[0] != script {
		t.Errorf("script must be first arg, got %v", execArgs)
	}
}

func TestExplicitNone(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "blob.bin", "\x7fELF...")
	cfg := DefaultFor("t", script)
	cfg.Interpreter = "none"

	execPath, _, effective, err := ResolveInterpreter(cfg)
	if err != nil {
		t.Fatalf("ResolveInterpreter: %v", err)
	}
	if execPath != "" || effective != "none" {
		t.Errorf("explicit none: %q %q", execPath, effective)
	}
}

func TestUnknownInterpreterFailsAtResolve(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "a.js", "x\n")
	cfg := DefaultFor("t", script)
	cfg.Interpreter = "definitely-not-a-binary-xyz"

	if _, _, _, err := ResolveInterpreter(cfg); err == nil {
		t.Fatal("missing interpreter must fail at resolution time, not first spawn")
	}
}

func TestUnknownExtensionDirectExec(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "weirdthing", "data\n")
	cfg := DefaultFor("t", script)

	execPath, _, effective, err := ResolveInterpreter(cfg)
	if err != nil {
		t.Fatalf("ResolveInterpreter: %v", err)
	}
	if execPath != "" || effective != "none" {
		t.Errorf("unknown ext: %q %q (want direct exec; ENOEXEC surfaces at spawn)", execPath, effective)
	}
}

func TestWithResolvedExecAbsolutizesScript(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "svc.sh", "#!/bin/sh\n")
	cfg := DefaultFor("t", script) // relative path in a temp cwd is fine — Abs uses cwd

	resolved, err := WithResolvedExec(cfg)
	if err != nil {
		t.Fatalf("WithResolvedExec: %v", err)
	}
	if !filepath.IsAbs(resolved.Script) {
		t.Errorf("script = %q, want absolute", resolved.Script)
	}
}

func TestWithResolvedExecPathBinary(t *testing.T) {
	cfg := DefaultFor("t", "sh")
	resolved, err := WithResolvedExec(cfg)
	if err != nil {
		t.Fatalf("WithResolvedExec: %v", err)
	}
	shBin, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH")
	}
	if resolved.Script != shBin {
		t.Errorf("script = %q, want %q", resolved.Script, shBin)
	}
}
