//go:build linux

package proc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveCgroupMount(t *testing.T) {
	for _, tc := range []struct {
		name, path, root, mount, want string
	}{
		{"host", "/user.slice/pm0.service", "/", "/sys/fs/cgroup", "/sys/fs/cgroup/user.slice/pm0.service"},
		{"subtree", "/delegated/apps/web", "/delegated", "/run/cgroups", "/run/cgroups/apps/web"},
		{"namespace-root", "/", "/docker/container", "/sys/fs/cgroup", "/sys/fs/cgroup"},
		{"escaped-mount", "/apps", "/", `/run/cgroup\040mount`, "/run/cgroup mount/apps"},
		{"outside-subtree", "/other/apps", "/delegated", "/run/cgroups", ""},
		{"traversal", "/../../outside", "/", "/sys/fs/cgroup", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := fmt.Sprintf("20 19 0:28 %s %s rw - cgroup2 cgroup rw\n", tc.root, tc.mount)
			got, err := resolveCgroupMount(tc.path, info)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("resolve = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestFindCgroupForLeaderSelectsIncarnation(t *testing.T) {
	root := t.TempDir()
	for name, pid := range map[string]int{"web-0": 100, "web-0-launch-one": 101, "web-0-launch-two": 102, "other": 103} {
		path := filepath.Join(root, name)
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, cgroupProcsFile), []byte(fmt.Sprintf("%d\n", pid)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for pid, name := range map[int]string{100: "web-0", 101: "web-0-launch-one", 102: "web-0-launch-two"} {
		path, err := findCgroupForLeader(root, "web-0", pid)
		if err != nil || path != filepath.Join(root, name) {
			t.Fatalf("leader %d: path=%q err=%v", pid, path, err)
		}
	}
	if _, err := findCgroupForLeader(root, "web-0", 103); !errors.Is(err, ErrNoCgroup) {
		t.Fatalf("adopted unrelated cgroup: %v", err)
	}
}

func TestCgroupOverlappingLaunchesAreIsolated(t *testing.T) {
	for _, mode := range []Mode{ModeCgroupKill, ModeCgroupFreeze} {
		t.Run(string(mode), func(t *testing.T) {
			requireCgroupMode(t, mode)
			l := newTestLauncher(t, mode)
			name := uniqueAppName("overlap")
			spec := Spec{Name: name, BinPath: "sh", Args: []string{"-c", "sleep 60"}, KillTimeout: testKillTimeout}
			first, err := l.Launch(spec)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Stop()
			second, err := l.Launch(spec)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Stop()
			if first.CgroupPath() == second.CgroupPath() {
				t.Fatal("overlapping workers share a cgroup")
			}
			// Wrapper attachment is asynchronous; wait for both identities.
			deadline := time.Now().Add(5 * time.Second)
			for {
				p1, e1 := findCgroupForLeader(l.cgRoot, name, first.PID())
				p2, e2 := findCgroupForLeader(l.cgRoot, name, second.PID())
				if e1 == nil && e2 == nil && p1 == first.cgPath && p2 == second.cgPath {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("workers failed to attach: %v %v", e1, e2)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := first.Stop(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-second.Done():
				t.Fatal("stopping old worker killed replacement")
			default:
			}
			path, err := findCgroupForLeader(l.cgRoot, name, second.PID())
			if err != nil || path != second.cgPath {
				t.Fatalf("adoption lookup selected wrong incarnation: %s, %v", path, err)
			}
		})
	}
}
