//go:build linux

// Daemon pidfile (perf queue item 3, docs/benchmarks2.md): the control
// socket's ownership proof. A daemon that died uncleanly (SIGKILL, crash,
// power loss) leaves both its socket file and this record behind; the
// next boot reads the record and — when the recorded owner is provably
// gone — skips the conservative 2s mid-boot grace, taking the socket over
// in ~0.3s. This is what daemon-crash respawn and reboot resurrect
// measure against PM2's 0.33s takeover.
//
// Record shape: "<pid> <starttime>" — the I6 anchor pair. A bare pid
// could be reused by an unrelated process and read as "alive"; the
// starttime (stat field 22) pins the identity: same pid AND same
// starttime means the recorded daemon still runs.
//
// Boot order (daemon.Run): write the record BEFORE binding the socket.
// A concurrent boot then always sees a live owner and keeps the grace,
// closing the two-CLI-commands race the old stat→ping→unlink flow guarded
// with its wait alone. The record is removed on polite exits; a stale
// record naming a dead pid is harmless by construction.
package daemon

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/pm0/pm0/internal/proc"
	"github.com/pm0/pm0/internal/store"
)

// writePidFile records this daemon's identity. Advisory: a write failure
// only costs takeover speed after an unclean death, never correctness.
func writePidFile() error {
	st, err := proc.StartTime(os.Getpid())
	if err != nil {
		return fmt.Errorf("pidfile: own starttime: %w", err)
	}
	line := fmt.Sprintf("%d %d\n", os.Getpid(), st)
	return os.WriteFile(store.PidPath(), []byte(line), 0o644)
}

// removePidFileIfOurs deletes the record when it names THIS process.
// Never removes a live successor's record (two daemons racing write in
// boot order; the loser must not erase the winner's proof).
func removePidFileIfOurs() {
	data, err := os.ReadFile(store.PidPath())
	if err != nil {
		return
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		_ = os.Remove(store.PidPath())
		return
	}
	pid, err := strconv.Atoi(fields[0])
	if err == nil && pid == os.Getpid() {
		_ = os.Remove(store.PidPath())
	}
}

// pidFileOwnerDead reports whether the pidfile names a process that is
// provably gone: the pid no longer exists, or its starttime differs (pid
// reused — the recorded owner is dead either way). Returns false when the
// record is missing or unparseable: unknown ownership keeps the
// conservative grace (a pre-pidfile daemon or a foreign file must never
// be unlinked blind).
func pidFileOwnerDead() bool {
	data, err := os.ReadFile(store.PidPath())
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return false
	}
	pid, err1 := strconv.Atoi(fields[0])
	want, err2 := strconv.ParseUint(fields[1], 10, 64)
	if err1 != nil || err2 != nil || pid <= 0 {
		return false
	}
	have, err := proc.StartTime(pid)
	if err != nil {
		return true // pid gone
	}
	return have != want // pid reused: recorded owner dead
}
