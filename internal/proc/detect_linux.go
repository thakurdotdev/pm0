//go:build linux

package proc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// EnvMarkerKey tags every process spawned by a launcher so tree members
// can be attributed during the pgid-mode orphan sweep. Grandchildren
// inherit it unless they clear their own environment.
const (
	EnvMarkerKey = "_PM0_APP"
	EnvLaunchKey = "_PM0_LAUNCH"
)

// Internal env keys used by the cgroup attach wrapper. The wrapper strips
// them before exec'ing the target so the app never sees them.
const (
	envExecPath  = "_PM0_INTERNAL_EXEC_PATH"
	envExecArgv  = "_PM0_INTERNAL_EXEC_ARGV"
	envExecProcs = "_PM0_INTERNAL_CGROUP_PROCS"
)

// Detection reports what Detect found about the running host.
type Detection struct {
	Mode          Mode   // effective kill mode
	KernelRelease string // e.g. "5.10.134-013.15.kangaroo.al8.x86_64"
	CgroupV2      bool   // unified hierarchy visible in /proc/self/cgroup
	CgroupPath    string // our own cgroup path ("" when not v2)
	CgroupRoot    string // delegated subtree root usable for app cgroups ("" when none)
	CgroupUsable  bool   // CgroupRoot writable (probe succeeded)
	Warning       string // human-readable degradation note for doctor/Ping
}

var (
	detectOnce sync.Once
	detectRes  Detection
)

// Detect reports host capabilities. It is process-wide cached; tests that
// need a fresh probe should call detectUncached.
func Detect() Detection {
	detectOnce.Do(func() { detectRes = detectUncached() })
	return detectRes
}

// detectUncached performs the real probe. Order of decisions:
//
//  1. PM0_KILL_MODE override (tests, debugging) — validated loosely,
//     invalid values fall through to auto-detection.
//  2. unified hierarchy in /proc/self/cgroup? No -> pgid.
//  3. delegation probe under our own cgroup -> fail -> pgid + warning.
//  4. kernel >= 5.14 -> cgroup-kill; >= 5.2 -> cgroup-freeze;
//     older -> pgid + warning (v2 freezer appeared in 5.2).
func detectUncached() Detection {
	d := Detection{Mode: ModePgid}
	d.KernelRelease = kernelRelease()

	// Force override (test hook / ops escape hatch).
	switch Mode(os.Getenv("PM0_KILL_MODE")) {
	case ModeCgroupKill, ModeCgroupFreeze, ModePgid:
		d.Mode = Mode(os.Getenv("PM0_KILL_MODE"))
		d.CgroupV2, d.CgroupPath, _ = ownCgroup()
		if d.Mode != ModePgid {
			root := os.Getenv("PM0_CGROUP_ROOT")
			if root == "" {
				root = d.CgroupPath
			}
			d.CgroupRoot, d.CgroupUsable = probeCgroupRoot(root)
			d.CgroupUsable = true // forced: paths will surface real errors
		}
		return d
	}

	ok, path, err := ownCgroup()
	d.CgroupV2 = ok
	d.CgroupPath = path
	if !ok {
		d.Warning = "no cgroup v2 unified hierarchy (cgroup v1 or none); using pgid fallback"
		return d
	}
	if err != nil {
		d.Warning = "cgroup v2 present but unreadable: " + err.Error()
		return d
	}

	root, usable := probeCgroupRoot(path)
	d.CgroupRoot = root
	d.CgroupUsable = usable
	if !usable {
		d.Warning = "cgroup v2 present but no delegated writable subtree; using pgid fallback"
		return d
	}

	major, minor, perr := kernelVersion(d.KernelRelease)
	if perr != nil {
		d.Warning = "kernel release unparseable (" + perr.Error() + "); using pgid fallback"
		return d
	}
	switch {
	case major > 5 || (major == 5 && minor >= 14):
		d.Mode = ModeCgroupKill
	case major > 5 || (major == 5 && minor >= 2):
		d.Mode = ModeCgroupFreeze
		d.Warning = fmt.Sprintf("kernel %d.%d lacks cgroup.kill (needs 5.14); using freeze-drain", major, minor)
	default:
		d.Warning = fmt.Sprintf("kernel %d.%d lacks cgroup v2 freezer (needs 5.2); using pgid fallback", major, minor)
	}
	return d
}

// ownCgroup parses /proc/self/cgroup for the unified `0::<path>` entry.
func ownCgroup() (ok bool, path string, err error) {
	data, rerr := os.ReadFile("/proc/self/cgroup")
	if rerr != nil {
		return false, "", rerr
	}
	for _, line := range strings.Split(string(data), "\n") {
		// Format: hierarchy-ID:controller-list:cgroup-path (v1)
		//         0::path (v2 unified)
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" && parts[1] == "" {
			return true, parts[2], nil
		}
	}
	return false, "", nil
}

// probeCgroupRoot checks whether dir exists and accepts cgroup subtree
// creation. It creates and removes a throwaway child directory; success
// means delegation is available and app cgroups can live under it.
func probeCgroupRoot(dir string) (root string, usable bool) {
	if dir == "" {
		dir = "/sys/fs/cgroup"
	}
	probe := filepath.Join(dir, fmt.Sprintf("pm0-probe-%d", os.Getpid()))
	if err := os.Mkdir(probe, 0o755); err != nil {
		return dir, false
	}
	// A usable v2 subtree always exposes cgroup.procs.
	if _, err := os.Stat(filepath.Join(probe, "cgroup.procs")); err != nil {
		_ = os.Remove(probe)
		return dir, false
	}
	_ = os.Remove(probe)
	return dir, true
}

func kernelRelease() string {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return ""
	}
	return utsChars(uts.Release)
}

func utsChars(b [65]byte) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}

// kernelVersion parses "5.10.134-..." into (5, 10).
func kernelVersion(release string) (major, minor int, err error) {
	if release == "" {
		return 0, 0, fmt.Errorf("empty release")
	}
	dot := strings.SplitN(release, ".", 3)
	if len(dot) < 2 {
		return 0, 0, fmt.Errorf("malformed release %q", release)
	}
	major, err = strconv.Atoi(dot[0])
	if err != nil {
		return 0, 0, err
	}
	minor, err = strconv.Atoi(dot[1])
	if err != nil {
		return 0, 0, err
	}
	return major, minor, nil
}

// SanitizeName renders an app name safe for use as a cgroup directory name.
// Dots are allowed (systemd-style slice names use them) but the result must
// never be "." or ".." — a cgroup dir of ".." would be the parent.
func SanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 || b.String() == "." || b.String() == ".." {
		return "app"
	}
	return b.String()
}
