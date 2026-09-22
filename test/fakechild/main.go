// Command fakechild is the test backbone of pm0 (see plan: testing
// strategy). It is a misbehavior-injectable child: every test that needs a
// process to outlive, ignore, escape or spam in a specific way runs this
// binary with the matching flag.
//
// M0 flag set (one per planned misbehavior; the rest arrive with their
// milestones):
//
//	--exit-after <ms>        exit (with --exit-code) after the delay;
//	                         without this flag the child sleeps forever
//	--exit-code <n>          exit code used on delayed/forced exit
//	--ignore-sigterm         SIGTERM is ignored (I5: ignorer trees die at
//	                         timeout only)
//	--spawn-orphan           fork a grandchild (--exit-after 2000) that
//	                         outlives this process, then keep going; the
//	                         grandchild is reaped via the subreaper
//	--setsid-child           double-fork: grandchild calls setsid() and
//	                         sleeps forever (pgid escape artist)
//	--fork-during-kill       on first SIGTERM/SIGINT: fork a grandchild
//	                         (sleeps forever) before doing whatever the
//	                         signal would otherwise do
//	--pid-reuse-target <p>   write "pid starttime" to <p>, then ignore
//	                         SIGTERM and sleep forever (I6 victim)
//	--flood <n>              write n lines ("out-<i>") to stdout at startup
//	                         (I8 log-flood tests); combine with --exit-after
//	                         to keep living after the flood
//	--flood-err <n>          write n lines ("err-<i>") to stderr at startup
//
// M5 flags (wait_ready / max_memory_restart support):
//
//	--send-ready             write the node-IPC frame "ready" to fd 3
//	                         (NODE_CHANNEL_FD channel) at startup
//	--send-ready-after <ms>  same, after the delay (wait_ready timing tests)
//	--alloc-mb <n>           allocate and touch n MiB, then keep living
//	                         (max_memory_restart enforcement tests)
//
// Plumbing flags (identity reporting, shared by the scenarios above):
//
//	--report-pid <p>         write "pid starttime" to <p> at startup
//	--orphan-pidfile <p>     where this process's spawned descendants
//	                         write their --report-pid
//
// Default behavior (no flags): sleep forever, exit 0 on SIGINT/SIGTERM —
// a well-behaved app.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	var (
		exitAfterMs      int
		exitCode         int
		ignoreSigterm    bool
		spawnOrphan      bool
		setsidChild      bool
		doSetsid         bool
		forkDuringKill   bool
		pidReuseTarget   string
		reportPid        string
		orphanPidfile    string
		floodOut         int
		floodErr         int
		sendReady        bool
		sendReadyAfterMs int
		allocMB          int
	)
	flag.IntVar(&exitAfterMs, "exit-after", 0, "exit after N ms (0: sleep forever)")
	flag.IntVar(&exitCode, "exit-code", 0, "exit code for delayed exit")
	flag.BoolVar(&ignoreSigterm, "ignore-sigterm", false, "ignore SIGTERM")
	flag.BoolVar(&spawnOrphan, "spawn-orphan", false, "spawn a grandchild that outlives us")
	flag.BoolVar(&setsidChild, "setsid-child", false, "fork a setsid'd grandchild, then exit")
	flag.BoolVar(&doSetsid, "do-setsid", false, "internal: call setsid() at startup")
	flag.BoolVar(&forkDuringKill, "fork-during-kill", false, "fork a grandchild on first signal")
	flag.StringVar(&pidReuseTarget, "pid-reuse-target", "", "report pid+starttime here, then ignore SIGTERM forever")
	flag.StringVar(&reportPid, "report-pid", "", "write 'pid starttime' to this file at startup")
	flag.StringVar(&orphanPidfile, "orphan-pidfile", "", "descendants write their identity here")
	flag.IntVar(&floodOut, "flood", 0, "write N lines to stdout at startup")
	flag.IntVar(&floodErr, "flood-err", 0, "write N lines to stderr at startup")
	flag.BoolVar(&sendReady, "send-ready", false, "write node-IPC 'ready' to fd 3 at startup")
	flag.IntVar(&sendReadyAfterMs, "send-ready-after", 0, "delay in ms before writing 'ready' (needs --send-ready)")
	flag.IntVar(&allocMB, "alloc-mb", 0, "allocate and touch N MiB, then keep living")
	flag.Parse()

	if doSetsid {
		// Only legal as the forked child of --setsid-child: we are not a
		// group leader there, so setsid() succeeds and gives us our own
		// session/pgid (the pgid-mode escape under test).
		_, _ = syscall.Setsid()
	}

	// Descendant spawning. Orphan: dies on its own after 2s (reaping test).
	// Setsid child and fork-during-kill grandchild: sleep forever, waiting
	// for the kill path to come for them.
	//
	// waitForIdentity: the spawned descendant writes the identity file at
	// ITS startup (exec + Go runtime init, a few ms) — long after fork(2)
	// returns here. The kill path legitimately kills tree members the
	// moment it sees them, so a spawner that exits (or lets the stop
	// proceed) before the descendant's write races the kill and can
	// consume the file forever (this deterministic loss surfaced when the
	// supervisor's pgrp scan got 3x faster). Waiting for the file keeps
	// every test's readiness barrier deterministic.
	waitForIdentity := func() {
		if orphanPidfile == "" {
			return
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(orphanPidfile); err == nil {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	spawnDescendant := func(lifetimeMs int, extra ...string) {
		if orphanPidfile == "" {
			return
		}
		args := []string{"--report-pid", orphanPidfile}
		if lifetimeMs > 0 {
			args = append(args, "--exit-after", strconv.Itoa(lifetimeMs))
		}
		args = append(args, extra...)
		cmd := exec.Command(selfExe(), args...)
		cmd.Stdout = nil
		cmd.Stderr = nil
		_ = cmd.Start()
		waitForIdentity()
	}

	if spawnOrphan {
		spawnDescendant(2000)
	}
	if setsidChild {
		// Double-fork: the intermediate exits immediately, so the setsid
		// grandchild reparents to the subreaper (the daemon under test).
		cmd := exec.Command(selfExe(), "--do-setsid", "--report-pid", orphanPidfile)
		_ = cmd.Start()
		waitForIdentity()
		os.Exit(0)
	}

	// Signal discipline FIRST, identity report SECOND: the identity file
	// is the tests' readiness barrier — its presence guarantees handlers
	// are installed, so a signal racing startup cannot hit the default
	// disposition (that race is a test artifact, not supervisor behavior).
	sigCh := make(chan os.Signal, 4)
	watch := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if ignoreSigterm || pidReuseTarget != "" {
		// Ignorer: SIGTERM must NOT reach a handler (kernel default for
		// SIGTERM would kill us — ignored instead).
		signal.Ignore(syscall.SIGTERM)
		watch = []os.Signal{syscall.SIGINT}
	}
	signal.Notify(sigCh, watch...)

	forkedOnSignal := false
	go func() {
		for sig := range sigCh {
			if forkDuringKill && !forkedOnSignal {
				forkedOnSignal = true
				spawnDescendant(0) // escape artist grandchild, sleeps forever
			}
			if sig == syscall.SIGINT || sig == syscall.SIGTERM {
				// Well-behaved child: graceful exit 0.
				os.Exit(0)
			}
		}
	}()

	if reportPid != "" {
		writeIdentity(reportPid)
	}
	if pidReuseTarget != "" {
		writeIdentity(pidReuseTarget)
	}

	if allocMB > 0 {
		hold := allocAndTouch(allocMB)
		_ = hold // keep reachable for the process lifetime
	}

	if sendReady {
		if sendReadyAfterMs > 0 {
			time.Sleep(time.Duration(sendReadyAfterMs) * time.Millisecond)
		}
		// Node IPC frame: process.send('ready') emits the JSON string
		// "ready" plus newline on the NODE_CHANNEL_FD (fd 3 here).
		if f := os.NewFile(3, "ipc"); f != nil {
			_, _ = f.WriteString("\"ready\"\n")
		}
	}

	if floodOut > 0 {
		flood(os.Stdout, "out", floodOut)
	}
	if floodErr > 0 {
		flood(os.Stderr, "err", floodErr)
	}

	if exitAfterMs > 0 {
		time.Sleep(time.Duration(exitAfterMs) * time.Millisecond)
		os.Exit(exitCode)
	}
	// Sleep forever; signals above handle termination. (A plain select{}
	// can trip the runtime deadlock detector; a long timer cannot.)
	for {
		time.Sleep(time.Hour)
	}
}

// doSetsid is declared as a real (undocumented in usage) flag above so
// flag.Parse accepts it in the forked child.

func selfExe() string {
	if len(os.Args) == 0 {
		return "fakechild"
	}
	return os.Args[0]
}

// flood writes n tagged lines through a large buffered writer: the
// fastest possible sane output for I8 (bounded-buffer) tests.
func flood(f *os.File, tag string, n int) {
	w := bufio.NewWriterSize(f, 64<<10)
	for i := 0; i < n; i++ {
		fmt.Fprintf(w, "%s-%d\n", tag, i)
	}
	_ = w.Flush()
}

// writeIdentity records "pid starttime" — the pair the I6 guard is built
// on — atomically (temp+rename, same discipline as dump.json).
func writeIdentity(path string) {
	st, err := startTimeSelf()
	if err != nil {
		fmt.Fprintf(os.Stderr, "fakechild: starttime: %v\n", err)
		return
	}
	tmp := path + ".tmp"
	content := fmt.Sprintf("%d %d\n", os.Getpid(), st)
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "fakechild: write %s: %v\n", tmp, err)
		return
	}
	_ = os.Rename(tmp, path)
}

func startTimeSelf() (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", os.Getpid()))
	if err != nil {
		return 0, err
	}
	// Field 22; parse after last ')' as in internal/proc.
	s := string(data)
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ')' {
			fields := splitFields(s[i+1:])
			// fields[0] is field 3; starttime is index 19.
			if len(fields) < 20 {
				return 0, fmt.Errorf("short stat")
			}
			return strconv.ParseUint(fields[19], 10, 64)
		}
	}
	return 0, fmt.Errorf("malformed stat")
}

func splitFields(s string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	return out
}

// allocAndTouch allocates mb MiB and writes every page so the RSS is real
// (untouched pages stay unmapped and invisible to statm).
func allocAndTouch(mb int) [][]byte {
	chunks := make([][]byte, 0, mb)
	for i := 0; i < mb; i++ {
		b := make([]byte, 1<<20)
		for j := 0; j < len(b); j += 4096 {
			b[j] = 1
		}
		chunks = append(chunks, b)
	}
	return chunks
}
