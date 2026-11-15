//go:build linux

// Listening-socket discovery and SO_REUSEPORT shareability probing for the
// rolling reload. A reload overlaps the old and new incarnation of an app;
// that only works when the new one can bind the same port while the old
// still holds it, i.e. when the old socket was opened with SO_REUSEPORT
// (cluster mode via the preload, or an app that opts in itself, e.g. Elysia
// on Bun). A plain fork-mode server (node http, Bun.serve, Hono, Express…)
// would crash-loop on EADDRINUSE for the whole overlap window.
package proc

import (
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// ListenSocket is one TCP listening socket held by a process tree.
type ListenSocket struct {
	IP   net.IP
	Port int
	V6   bool
}

func (l ListenSocket) String() string {
	return net.JoinHostPort(l.IP.String(), strconv.Itoa(l.Port))
}

// TreeListeners returns the TCP listening sockets whose fds are held by any
// of pids (deduplicated, sorted by port). Unreadable pids are skipped.
func TreeListeners(pids []int) []ListenSocket {
	if len(pids) == 0 {
		return nil
	}
	byInode := make(map[uint64]ListenSocket)
	for _, f := range []struct {
		path string
		v6   bool
	}{{"/proc/net/tcp", false}, {"/proc/net/tcp6", true}} {
		data, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		for inode, ls := range parseProcNetListeners(string(data), f.v6) {
			byInode[inode] = ls
		}
	}
	if len(byInode) == 0 {
		return nil
	}
	seen := make(map[uint64]bool)
	var out []ListenSocket
	for _, pid := range pids {
		dir := fmt.Sprintf("/proc/%d/fd", pid)
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inode, err := strconv.ParseUint(target[len("socket:["):len(target)-1], 10, 64)
			if err != nil || seen[inode] {
				continue
			}
			if ls, ok := byInode[inode]; ok {
				seen[inode] = true
				out = append(out, ls)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].String() < out[j].String()
	})
	return out
}

// parseProcNetListeners maps socket inode -> listening socket for one
// /proc/net/tcp{,6} table.
func parseProcNetListeners(table string, v6 bool) map[uint64]ListenSocket {
	out := make(map[uint64]ListenSocket)
	lines := strings.Split(table, "\n")
	if len(lines) < 2 {
		return out
	}
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != "0A" { // 0A = TCP_LISTEN
			continue
		}
		hostHex, portHex, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port == 0 {
			continue
		}
		ip, err := decodeProcNetIP(hostHex)
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		out[inode] = ListenSocket{IP: ip, Port: int(port), V6: v6}
	}
	return out
}

// decodeProcNetIP decodes the kernel's hex address: the address is printed
// as 32-bit words, each in host (little-endian on every pm0 target) order.
func decodeProcNetIP(h string) (net.IP, error) {
	raw, err := hex.DecodeString(h)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return nil, fmt.Errorf("bad address %q", h)
	}
	ip := make(net.IP, len(raw))
	for w := 0; w < len(raw); w += 4 {
		ip[w], ip[w+1], ip[w+2], ip[w+3] = raw[w+3], raw[w+2], raw[w+1], raw[w]
	}
	return ip, nil
}

// Shareable reports whether a new process could bind this address while the
// current holder keeps it: it binds (never listens on, so no connection is
// ever routed to it) a probe socket with SO_REUSEPORT at the same address.
// The kernel only allows that when the holder also set SO_REUSEPORT under
// the same uid. Any error other than a clean bind is treated as "not
// shareable" — the caller falls back to stop-then-start, which is always
// correct, merely not zero-downtime.
func (l ListenSocket) Shareable() bool {
	family := unix.AF_INET
	var sa unix.Sockaddr
	if l.V6 {
		family = unix.AF_INET6
		s6 := &unix.SockaddrInet6{Port: l.Port}
		copy(s6.Addr[:], l.IP.To16())
		sa = s6
	} else {
		s4 := &unix.SockaddrInet4{Port: l.Port}
		copy(s4.Addr[:], l.IP.To4())
		sa = s4
	}
	fd, err := unix.Socket(family, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
		return false
	}
	return unix.Bind(fd, sa) == nil
}

// UnshareableListeners returns the listening sockets held by pids that a
// second incarnation could not bind concurrently (see Shareable).
func UnshareableListeners(pids []int) []ListenSocket {
	var out []ListenSocket
	for _, ls := range TreeListeners(pids) {
		if !ls.Shareable() {
			out = append(out, ls)
		}
	}
	return out
}
