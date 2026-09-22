//go:build linux

// CLI plumbing: daemon auto-spawn (first command boots a detached daemon),
// pm2-style argument partitioning, and small parsers.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	v1 "github.com/pm0/pm0/api/v1"
	"github.com/pm0/pm0/internal/store"
	"github.com/pm0/pm0/pkg/client"
)

// dialDaemon connects, spawning a detached daemon when none answers yet —
// pm2's `pm2 start` behavior on a fresh host.
func dialDaemon() (*client.Client, error) {
	c, err := client.Dial("")
	if err == nil {
		if _, perr := c.Ping(); perr == nil {
			return c, nil
		}
		_ = c.Close()
	}
	if serr := spawnDaemon(); serr != nil {
		return nil, fmt.Errorf("daemon is not running and could not be started: %v", serr)
	}
	// Wait for the socket to come up.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		c, err = client.Dial("")
		if err == nil {
			if _, perr := c.Ping(); perr == nil {
				return c, nil
			}
			_ = c.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, fmt.Errorf("daemon did not come up on %s within 8s", store.SocketPath())
}

// spawnDaemon launches `pm0 daemon` detached: own session, stdio into
// the daemon log, so it survives the CLI process and never inherits a tty.
func spawnDaemon() error {
	if err := store.EnsureHome(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	logFile, err := os.OpenFile(store.DaemonLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	// Do not wait: the daemon is reparented and outlives the CLI.
	go func() { _, _ = cmd.Process.Wait() }() // reap when the CLI lingers
	return nil
}

// splitArgs partitions pm2-style arguments: flags may appear before or
// after positional arguments; `--` ends flag parsing, everything after it
// belongs to the app (script arguments).
//
// Value-taking flags consume their argument even when it does not start
// with "-" (pm0 start app.js --name x): the flag set is consulted for
// which names take values. Unknown flags are left for fs.Parse to reject
// loudly (compat divergence 2).
func splitArgs(args []string, fs *flag.FlagSet) (flags, positional, appArgs []string) {
	flags = []string{}
	positional = []string{}
	appArgs = []string{}

	takesValue := map[string]bool{}
	if fs != nil {
		fs.VisitAll(func(f *flag.Flag) {
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				return
			}
			takesValue["-"+f.Name] = true
		})
	}

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			appArgs = append(appArgs, args[i+1:]...)
			return flags, positional, appArgs
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if _, _, ok := strings.Cut(name, "="); ok {
				continue // inline value (-name=x)
			}
			if takesValue["-"+name] && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			}
		default:
			positional = append(positional, a)
		}
	}
	return flags, positional, appArgs
}

// selector builds the Selector for control commands: "all" or a list of
// names/pm_ids (CLI grammar: <name> | <pm_id> | all).
func selector(targets []string) *v1.Selector {
	sel := &v1.Selector{}
	if len(targets) == 0 {
		sel.All = true
		return sel
	}
	for _, t := range targets {
		if id, err := strconv.Atoi(t); err == nil && id >= 0 {
			sel.PmIds = append(sel.PmIds, int32(id))
			continue
		}
		if t == "all" {
			sel.All = true
			continue
		}
		sel.Names = append(sel.Names, t)
	}
	return sel
}

// parseSize accepts pm2's memory syntax: plain bytes, or K/M/G/T suffixes
// (case-insensitive, optional B).
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}
	mul := int64(1)
	for i, suf := range []string{"T", "G", "M", "K"} {
		if strings.HasSuffix(s, suf) || strings.HasSuffix(s, suf+"B") {
			mul = 1 << (10 * (4 - i))
			s = strings.TrimSuffix(s, "B")
			s = strings.TrimSuffix(s, suf)
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return int64(v * float64(mul)), nil
}

// parseEnvFlag consumes repeated --env KEY=VALUE flags.
func parseEnvFlag(v string, into map[string]string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("bad --env %q (want KEY=VALUE)", v)
	}
	into[k] = val
	return nil
}

// fatalf prints and exits — CLI error discipline (compat divergence 2:
// unknown flags and bad input error loudly, never no-op).
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pm0: "+format+"\n", args...)
	os.Exit(1)
}
