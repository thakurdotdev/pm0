// Command pm0 is the PM2-compatible supervisor binary.
//
// M2 ships the full CLI surface on the gRPC+UDS control plane: the first
// command that needs a daemon spawns one detached (state in $PM0_HOME,
// default ~/.pm0). `pm0 daemon` runs the daemon in the foreground for
// debugging.
package main

import (
	"fmt"
	"os"

	"github.com/pm0/pm0/internal/proc"
)

// version is overridden at release build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags)"
var version = "0.0.0-m6"

var commit = "unknown"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "version":
		fmt.Printf("pm0 %s (%s)\n", version, commit)
	case "doctor":
		doctor()
	case "daemon":
		runDaemon(args)
	case "start":
		cmdStart(args)
	case "stop":
		cmdSelector(args, "stop")
	case "restart":
		cmdSelector(args, "restart")
	case "reload", "gracefulreload", "gracefulReload":
		cmdSelector(args, "reload")
	case "delete", "del":
		cmdSelector(args, "delete")
	case "scale":
		cmdScale(args)
	case "list", "ls", "status":
		cmdList(args)
	case "jlist":
		cmdJlist(args)
	case "describe", "show":
		cmdDescribe(args)
	case "logs":
		cmdLogs(args)
	case "flush":
		cmdFlush(args)
	case "save":
		cmdSave(args)
	case "resurrect":
		cmdResurrect(args)
	case "update":
		cmdUpdate(args)
	case "kill":
		cmdKill(args)
	case "env":
		cmdEnv(args)
	case "startup":
		cmdStartup(args)
	case "unstartup":
		cmdUnstartup(args)
	case "ping":
		cmdPing(args)
	case "help", "-h", "--help":
		usage()
	default:
		// Compat contract: unknown commands error loudly, never no-op.
		fmt.Fprintf(os.Stderr, "pm0: unknown command %q (see `pm0 help`)\n", os.Args[1])
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`pm0 - PM2-compatible process supervisor (Go)

Usage:
  pm0 <command> [flags]

Process control:
  start <script> [args --]  start an app (-i N|max for cluster mode on node
                            apps / fork instance families, --exec-mode, ...)
  stop <name|id|all>        stop apps (graceful kill_signal, then force)
  restart <name|id|all>     stop+start, preserving id and config
  reload <name|all>         rolling replace, one instance at a time (0-downtime
                            for cluster apps: new binds the shared port first,
                            old gets the 'shutdown' IPC handoff, then signals)
  gracefulReload <name|all> alias of reload
  scale <name> <+N|-N|N>    adjust instance count (absolute target or delta)
  delete <name|id|all>      stop and forget (frees the pm_id)

Inspection:
  list, ls, status          process table
  jlist                     JSON array, pm2-compatible (scripts parse it)
  describe, show <name|id>  one app in full
  logs [name]               out/err logs (--lines, --err, --out, --raw)
  flush [name|id|all]       empty all or specific app log files
  env <name|id>             print the app's full child environment

Persistence & daemon:
  save                      dump the app set to ~/.pm0/dump.json
  resurrect                 adopt/start apps from the dump
  update                    re-exec the daemon in place (apps survive)
  kill [--force]            stop apps, unlink socket, exit daemon
  ping                      liveness probe (daemon metadata)
  daemon                    run the daemon in the foreground
  doctor                    report kernel/cgroup kill-path detection
  version                   print version
  help                      this text

  startup                   install (root) or print the systemd unit that
                            resurrects the saved apps on boot
  unstartup                 remove the startup unit

Config files:
  start ecosystem.config.js batch-start apps from a PM2-shaped ecosystem
                            file (--only name[,name], --env <name> selects
                            env_<name>; JS runs sandboxed: no require/process/IO)

Environment: PM0_HOME overrides ~/.pm0 (state root).
  PM0_PPROF_ADDR=127.0.0.1:6060 serves /debug/pprof on the daemon
  (localhost; unset by default). PM0_KILL_MODE forces a kill path.
`)
}

// doctor prints the effective kill-path mode and every fact it derives
// from, so a CI failure or field bug report contains the whole picture.
func doctor() {
	d := proc.Detect()
	fmt.Printf("pm0 doctor\n")
	fmt.Printf("  version:        %s\n", version)
	fmt.Printf("  kernel:         %s\n", d.KernelRelease)
	fmt.Printf("  cgroup v2:      %v\n", d.CgroupV2)
	if d.CgroupV2 {
		fmt.Printf("  own cgroup:     %s\n", d.CgroupPath)
		fmt.Printf("  delegated root: %s (usable=%v)\n", d.CgroupRoot, d.CgroupUsable)
	}
	if v := os.Getenv("PM0_KILL_MODE"); v != "" {
		fmt.Printf("  forced mode:    %s (PM0_KILL_MODE)\n", v)
	}
	fmt.Printf("  kill mode:      %s\n", d.Mode)
	if d.Warning != "" {
		fmt.Printf("  warning:        %s\n", d.Warning)
	}
}
