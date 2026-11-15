//go:build linux

package proc

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func listenTCP(t *testing.T, network, addr string, reuse bool) net.Listener {
	t.Helper()
	lc := net.ListenConfig{}
	if reuse {
		lc.Control = func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return serr
		}
	}
	ln, err := lc.Listen(context.Background(), network, addr)
	if err != nil {
		t.Skipf("listen %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln
}

func findListener(t *testing.T, port int) ListenSocket {
	t.Helper()
	for _, ls := range TreeListeners([]int{os.Getpid()}) {
		if ls.Port == port {
			return ls
		}
	}
	t.Fatalf("listener on port %d not discovered", port)
	return ListenSocket{}
}

func TestListenerShareability(t *testing.T) {
	cases := []struct {
		name, network, addr string
		reuse               bool
	}{
		{"v4 plain", "tcp4", "127.0.0.1:0", false},
		{"v4 reuseport", "tcp4", "127.0.0.1:0", true},
		{"v6 plain", "tcp6", "[::1]:0", false},
		{"v6 reuseport", "tcp6", "[::1]:0", true},
		{"dual-stack plain", "tcp", ":0", false},
		{"dual-stack reuseport", "tcp", ":0", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ln := listenTCP(t, tc.network, tc.addr, tc.reuse)
			port := ln.Addr().(*net.TCPAddr).Port
			ls := findListener(t, port)
			if got := ls.Shareable(); got != tc.reuse {
				t.Fatalf("%s Shareable() = %v, want %v", ls, got, tc.reuse)
			}
			un := UnshareableListeners([]int{os.Getpid()})
			found := false
			for _, u := range un {
				found = found || u.Port == port
			}
			if found == tc.reuse {
				t.Fatalf("UnshareableListeners contains %s = %v, want %v", ls, found, !tc.reuse)
			}
			// The probe must not have disturbed the real listener.
			c, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				t.Fatalf("listener broken after probe: %v", err)
			}
			c.Close()
		})
	}
}

func TestDecodeProcNetIP(t *testing.T) {
	ip, err := decodeProcNetIP("0100007F")
	if err != nil || !ip.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("v4 = %v, %v", ip, err)
	}
	ip, err = decodeProcNetIP("00000000000000000000000001000000")
	if err != nil || !ip.Equal(net.ParseIP("::1")) {
		t.Fatalf("v6 = %v, %v", ip, err)
	}
}
