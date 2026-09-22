//go:build linux

package startup

import (
	"os"
	"strings"
	"testing"
)

func TestUnitShape(t *testing.T) {
	u := Unit("alice", "/home/alice/.pm0", "/usr/local/bin/pm0")
	checks := []string{
		"[Unit]",
		"Description=pm0 process supervisor for alice",
		"After=network.target",
		"[Service]",
		"Type=oneshot",
		"User=alice",
		"Environment=PM0_HOME=/home/alice/.pm0",
		"ExecStart=/usr/local/bin/pm0 resurrect",
		"ExecStop=/usr/local/bin/pm0 kill",
		"RemainAfterExit=yes",
		"[Install]",
		"WantedBy=multi-user.target",
	}
	for _, want := range checks {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q\ngot:\n%s", want, u)
		}
	}
	if !strings.HasSuffix(u, "\n") {
		t.Error("unit must end with a newline")
	}
}

func TestUnitName(t *testing.T) {
	if got := UnitName("bob"); got != "pm0-bob.service" {
		t.Fatalf("got %q", got)
	}
}

func TestInstallScriptEmbedsUnit(t *testing.T) {
	script := InstallScript("alice", "/home/alice/.pm0", "/usr/local/bin/pm0", "/etc/systemd/system/pm0-alice.service")
	if !strings.Contains(script, "cat > /etc/systemd/system/pm0-alice.service") {
		t.Fatalf("install script missing unit path: %s", script)
	}
	if !strings.Contains(script, "ExecStart=/usr/local/bin/pm0 resurrect") {
		t.Fatalf("install script must embed the full unit: %s", script)
	}
	if !strings.Contains(script, "systemctl enable pm0-alice.service") {
		t.Fatalf("install script missing enable: %s", script)
	}
	// The heredoc must be quoted ('UNIT') so nothing expands during install.
	if !strings.Contains(script, "<<'UNIT'") {
		t.Fatalf("heredoc must be quoted: %s", script)
	}
}

func TestDetectSystemdPresence(t *testing.T) {
	// The sandbox may or may not run systemd; only assert consistency:
	// Detect returns exactly "" or "systemd", and "systemd" implies the
	// sentinel dir exists.
	got := Detect()
	if got != "" && got != "systemd" {
		t.Fatalf("unexpected Detect value %q", got)
	}
	if got == "systemd" {
		if _, err := os.Stat(SystemdRunPath); err != nil {
			t.Fatalf("Detect=systemd but stat failed: %v", err)
		}
	}
}
