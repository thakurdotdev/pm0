//go:build linux

// Node-IPC channel for fork-mode children (wait_ready support, compat.md
// rows 18/19). PM2's fork mode always carries an IPC channel — the child
// gets NODE_CHANNEL_FD and can process.send(...); pm0 reproduces the
// same wire surface for every launch:
//
//	socketpair -> child end as fd 3 (exec.Cmd ExtraFiles)
//	           -> parent end drained by pump()
//
// Frames are newline-delimited JSON (node's default IPC serialization).
// The only message the supervisor semantics care about is the string
// "ready" (process.send('ready')); everything else is discarded, exactly
// like pm2 drops messages it does not handle. The channel stays open and
// drained after 'ready' so the child never sees EPIPE mid-run.
//
// Wrapper-mode note: in cgroup kill paths the spawned binary is pm0
// itself re-exec'ing the target; fd 3 survives that exec (exec.Cmd clears
// CLOEXEC on ExtraFiles dups), and NODE_CHANNEL_FD survives the env
// rebuild — the real target inherits the channel transparently.
package proc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const (
	// NodeChannelFdEnv is the env var node reads for its IPC fd.
	NodeChannelFdEnv = "NODE_CHANNEL_FD"
	// nodeChannelFd is the child's fd number: ExtraFiles index 0 -> fd 3.
	nodeChannelFd = 3
)

// ipcChannel is the parent side of one child IPC channel.
type ipcChannel struct {
	parent *os.File    // parent-end socket (read side)
	fd     int         // parent-end raw fd, captured once (see close)
	closed atomic.Bool // close() is idempotent (Stop and Launch error paths)
	// ready closes exactly once, when the child sends the string "ready"
	// (the wait_ready gate, row 19). nil-safe via Handle.Ready().
	ready chan struct{}
	// done closes when pump() exits (EOF or error).
	done chan struct{}
}

// newIPCChannel creates the socketpair. Returns the channel (parent side)
// and the child-end file to hand to exec.Cmd.ExtraFiles (caller closes the
// parent-process copy right after Start — the child owns its dup).
//
// The PARENT end is set nonblocking BEFORE os.NewFile: Go registers only
// nonblocking descriptors with the netpoller, and a pollable fd makes the
// pump's blocked ReadString park the goroutine thread-free. With a blocking
// socketpair every pump pinned one OS thread for the app's lifetime — the
// daemon's thread count scaled 1:1 with the app count and hit
// pthread_create EAGAIN (SIGABRT) at ~500 apps on hosts with a tight
// threads-max (found by the suite2 benchmark at scale N=500). The child
// end keeps its default blocking mode (node/libuv sets O_NONBLOCK on its
// own copy; foreign children never touch fd 3).
func newIPCChannel() (*ipcChannel, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("ipc socketpair: %w", err)
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		unix.Close(fds[0])
		unix.Close(fds[1])
		return nil, nil, fmt.Errorf("ipc parent nonblock: %w", err)
	}
	c := &ipcChannel{
		parent: os.NewFile(uintptr(fds[0]), "pm0-ipc-parent"),
		fd:     fds[0],
		ready:  make(chan struct{}),
		done:   make(chan struct{}),
	}
	child := os.NewFile(uintptr(fds[1]), "pm0-ipc-child")
	return c, child, nil
}

// pump drains frames until EOF. Run once, in its own goroutine. A blocked
// read is released by close() (shutdown) or by the child side closing on
// tree death — the goroutine never outlives the channel for long.
func (c *ipcChannel) pump() {
	defer func() {
		_ = c.parent.Close()
		close(c.done)
	}()
	r := bufio.NewReaderSize(c.parent, 8192)
	for {
		line, err := r.ReadString('\n')
		if isReadyMessage(line) {
			select {
			case <-c.ready:
			default:
				close(c.ready)
			}
		}
		if err != nil {
			return
		}
	}
}

// close releases the parent end (best effort, idempotent). shutdown(SHUT_RD)
// first so a blocked pump read returns immediately even if Close alone
// would not wake it; then Close unblocks it definitively. The raw fd is
// captured at creation — calling parent.Fd() here would race the pump's
// concurrent reads/Close (os.File.fd is not synchronized); a stale fd
// number after Close is harmless because the shutdown runs at most once
// and EBADF is ignored.
func (c *ipcChannel) close() {
	if c.closed.CompareAndSwap(false, true) && c.fd >= 0 {
		_ = unix.Shutdown(c.fd, unix.SHUT_RD)
	}
	_ = c.parent.Close()
}

// sendMsg writes one newline-delimited JSON frame to the child — the
// daemon->worker direction of the node IPC channel (PM2 sends the string
// 'shutdown' to the old worker during cluster reload, God/Reload.js
// softCleanDeleteProcess). The parent end is pollable (nonblocking, see
// newIPCChannel), so Write parks in the netpoller when the socket buffer
// is momentarily full; node drains its side continuously.
func (c *ipcChannel) sendMsg(frame string) error {
	if c.closed.Load() {
		return fmt.Errorf("ipc: channel closed")
	}
	_, err := fmt.Fprintf(c.parent, "%s\n", frame)
	return err
}

// isReadyMessage parses one IPC frame and reports whether it carries the
// string "ready" — what a Node child emits for process.send('ready').
// Malformed frames (non-JSON, empty lines) are not ready and are dropped.
func isReadyMessage(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	var v any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		return false
	}
	s, ok := v.(string)
	return ok && s == "ready"
}
