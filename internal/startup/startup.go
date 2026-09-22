//go:build linux

// Package startup generates the boot-time resurrection unit (M4): the
// `pm0 startup` analog of `pm2 startup`. v1 supports systemd only — the
// dominant Linux init — detected via /run/systemd/system.
//
// Unit model (mirrors pm2's systemd unit):
//
//   - Type=oneshot + RemainAfterExit: the unit "runs" `pm0 resurrect`,
//     which dials (or spawns, detached) the per-user daemon, re-adopts
//     surviving trees, starts the rest, and exits 0. The unit then sits in
//     "active (exited)" — there is no long-running foreground process to
//     supervise; the daemon is deliberately detached from systemd's
//     process tree (pm2 works the same way).
//   - ExecStop=`pm0 kill`: a polite system shutdown stops every app tree
//     first (invariant I1), then the daemon exits.
//   - PM0_HOME is pinned per user, so multiple users each get their own
//     unit (pm0-<user>.service), same as pm2-<user>.service.
package startup

import (
	"fmt"
	"os"
	"os/user"
	"strings"
)

// SystemdRunPath is systemd's sentinel directory (exported for the CLI's
// error message).
const SystemdRunPath = "/run/systemd/system"

// Detect reports the init system. "systemd" when its sentinel dir exists;
// "" otherwise (unsupported in v1 — the caller prints manual instructions).
func Detect() string {
	if st, err := os.Stat(SystemdRunPath); err == nil && st.IsDir() {
		return "systemd"
	}
	return ""
}

// UnitName is the systemd unit for one user (pm2 parity: pm2-<user>.service).
func UnitName(userName string) string {
	return "pm0-" + userName + ".service"
}

// Unit renders the unit file. bin is the absolute pm0 binary path, home
// the PM0_HOME to pin, userName the user the daemon runs as.
func Unit(userName, home, bin string) string {
	return fmt.Sprintf(`[Unit]
Description=pm0 process supervisor for %s
After=network.target

[Service]
Type=oneshot
User=%s
Environment=PM0_HOME=%s
ExecStart=%s resurrect
ExecStop=%s kill
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`, userName, userName, home, bin, bin)
}

// CurrentUser resolves the invoking user's name and home directory for the
// default invocation. Falls back to the env when the passwd entry is
// missing (containers, LDAP-less hosts).
func CurrentUser() (name, home string, err error) {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username, u.HomeDir, nil
	}
	name = os.Getenv("USER")
	home = os.Getenv("HOME")
	if name == "" || home == "" {
		return "", "", fmt.Errorf("startup: cannot resolve current user (no passwd entry, no USER/HOME)")
	}
	return name, home, nil
}

// InstallScript prints the exact root commands to install the unit — the
// non-root path of `pm0 startup` (pm2 prints an analogous sudo line).
func InstallScript(userName, home, bin, unitPath string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("cat > %s <<'UNIT'\n%sUNIT\n", unitPath, Unit(userName, home, bin)))
	b.WriteString(fmt.Sprintf("systemctl daemon-reload\nsystemctl enable %s\n", UnitName(userName)))
	return b.String()
}
