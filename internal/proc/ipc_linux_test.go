//go:build linux

package proc

import (
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestIPCCleanupAfterEOFDoesNotShutdownReusedFD(t *testing.T) {
	c, child, err := newIPCChannel()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.parent.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var oldFD int
	if err := raw.Control(func(fd uintptr) { oldFD = int(fd) }); err != nil {
		t.Fatal(err)
	}
	go c.pump()
	_ = child.Close()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("pump did not exit after EOF")
	}
	if !c.closed.Load() {
		t.Fatal("EOF cleanup did not retire descriptor ownership")
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	if fds[0] != oldFD && fds[1] != oldFD {
		t.Logf("IPC fd %d was reused elsewhere; checking cleanup leaves new sockets intact", oldFD)
	}
	c.close()
	if _, err := unix.Write(fds[1], []byte("x")); err != nil {
		t.Fatalf("unrelated socket was shut down: %v", err)
	}
	buf := make([]byte, 1)
	if n, err := unix.Read(fds[0], buf); err != nil || n != 1 || buf[0] != 'x' {
		t.Fatalf("unrelated socket read = %d, %v, %q", n, err, buf)
	}
}

func TestIPCConcurrentCloseReleasesBlockedPump(t *testing.T) {
	c, child, err := newIPCChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	go c.pump()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(c.close)
	}
	wg.Wait()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("close did not release the blocked IPC pump")
	}
}

func TestIsReadyMessage(t *testing.T) {
	yes := []string{
		"\"ready\"\n",   // process.send('ready') — the canonical frame
		"\"ready\"",     // no trailing newline (still a complete JSON frame)
		"  \"ready\"\n", // whitespace tolerance
	}
	no := []string{
		"",
		"\n",
		"ready\n",        // bare string, not JSON
		"{\"a\":1}\n",    // any other object
		"[\"ready\"]\n",  // container
		"\"notready\"\n", // different string
		"{\"topic\":\"ready\"}\n",
		"garbage{\n", // malformed JSON
	}
	for _, in := range yes {
		if !isReadyMessage(in) {
			t.Errorf("isReadyMessage(%q) = false, want true", in)
		}
	}
	for _, in := range no {
		if isReadyMessage(in) {
			t.Errorf("isReadyMessage(%q) = true, want false", in)
		}
	}
}

// End-to-end over a real socketpair: a child that writes the ready frame
// to fd 3 unlocks Handle.Ready().
func TestIPCReadyEndToEnd(t *testing.T) {
	l := newTestLauncher(t, ModeAuto)
	h, err := l.Launch(Spec{
		Name:        uniqueAppName("ipc-e2e"),
		BinPath:     "/bin/sh",
		Args:        []string{"-c", `printf '"ready"\n' >&3; sleep 5`},
		KillTimeout: testKillTimeout,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	defer func() { _ = h.Stop() }()

	select {
	case <-h.Ready():
		// ready frame observed
	case <-h.Done():
		t.Fatal("child exited before sending ready")
	case <-time.After(5 * time.Second):
		t.Fatal("Ready() never closed after child wrote the frame")
	}

	// One-shot: the same channel keeps reporting closed (no re-arm).
	select {
	case <-h.Ready():
	case <-time.After(time.Second):
		t.Fatal("ready channel re-armed unexpectedly")
	}
}

// A child that never sends ready: Ready() stays armed; Stop() kills the
// tree, the child side closes, and the pump drains to EOF (no leak).
func TestIPCStopReleasesPump(t *testing.T) {
	l := newTestLauncher(t, ModeAuto)
	h, err := l.Launch(Spec{
		Name:        uniqueAppName("ipc-stop"),
		BinPath:     "/bin/sh",
		Args:        []string{"-c", "sleep 30"},
		KillTimeout: testKillTimeout,
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	select {
	case <-h.Ready():
		t.Fatal("Ready() closed without any frame from the child")
	case <-time.After(200 * time.Millisecond):
		// expected: still armed
	}

	if err := h.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-h.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done() never closed after Stop")
	}
	select {
	case <-h.Ready():
		t.Fatal("Ready() fired after Stop")
	default:
	}
}

func TestParseMemoryEvents(t *testing.T) {
	full := "populated 12\nscanned 340\nok 0\noom_kill 7\noom 1\n"
	if n := parseMemoryEvents(full); n != 7 {
		t.Fatalf("parseMemoryEvents(full) = %d, want 7", n)
	}
	if n := parseMemoryEvents("oom_kill 42\n"); n != 42 {
		t.Fatalf("parseMemoryEvents(single) = %d, want 42", n)
	}
	if n := parseMemoryEvents("nok 3\noom 1\n"); n != 0 {
		t.Fatalf("parseMemoryEvents(no oom_kill) = %d, want 0", n)
	}
	if n := parseMemoryEvents("oom_kill x\n"); n != 0 {
		t.Fatalf("parseMemoryEvents(bad value) = %d, want 0", n)
	}
	if n := parseMemoryEvents(""); n != 0 {
		t.Fatalf("parseMemoryEvents(empty) = %d, want 0", n)
	}
}
