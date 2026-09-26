//go:build linux

package proc

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestParseStatCommWithSpaces pins the parser against the classic trap:
// comm may contain spaces and parentheses (kernel threads like
// "[kworker/0:1]" are the common case, names with spaces exist too) —
// field indexing must start after the LAST ')'.
func TestParseStatCommWithSpaces(t *testing.T) {
	line := []byte("4242 (gnome-shell (kms)) R 1 4242 100 0 -1 4194304 0 0 0 0 12 34 0 0 20 0 1 0 1234567890 987654321 12345")
	si, err := parseStat(line)
	if err != nil {
		t.Fatalf("parseStat: %v", err)
	}
	if si.State != 'R' {
		t.Errorf("State = %q, want R", si.State)
	}
	if si.PPid != 1 {
		t.Errorf("PPid = %d, want 1", si.PPid)
	}
	if si.Pgrp != 4242 {
		t.Errorf("Pgrp = %d, want 4242", si.Pgrp)
	}
	if si.Utime != 12 || si.Stime != 34 {
		t.Errorf("ticks = (%d,%d), want (12,34)", si.Utime, si.Stime)
	}
	if si.Starttime != 1234567890 {
		t.Errorf("Starttime = %d, want 1234567890", si.Starttime)
	}
	if si.RSS != 12345 {
		t.Errorf("RSS = %d, want 12345", si.RSS)
	}
	if _, err := parseStat([]byte("1 (short) S 0")); err == nil {
		t.Error("short stat must error")
	}
}

// TestReadSnapshotSelf checks the pass against live truth for this
// process: self is present, RSS matches the statm-based ResidentBytes
// (same mm counter — the §2.1 monit pin), and the pgrp/reparented
// indexes see us and our children.
func TestReadSnapshotSelf(t *testing.T) {
	snap := ReadSnapshot()
	info, ok := snap.Info(os.Getpid())
	if !ok {
		t.Fatalf("self pid %d missing from snapshot", os.Getpid())
	}
	if info.State == 'Z' || info.State == 0 {
		t.Errorf("self state = %q, want a live letter", info.State)
	}

	want := ResidentBytes(os.Getpid())
	delta := int64(info.RSSBytes) - int64(want)
	if delta < 0 {
		delta = -delta
	}
	// Two reads of the same counter microseconds apart; allow a small
	// page drift (GC may be running).
	if delta > 1<<20 {
		t.Errorf("snapshot RSS %d vs ResidentBytes %d (delta %d too large)", info.RSSBytes, want, delta)
	}

	if _, _, err := statPPidGrp(os.Getpid()); err != nil {
		t.Fatalf("statPPidGrp(self): %v", err)
	}
	_, pgrp, err := statPPidGrp(os.Getpid())
	if err != nil {
		t.Fatalf("statPPidGrp: %v", err)
	}
	members := snap.PgrpMembers(pgrp)
	found := false
	for _, pid := range members {
		if pid == os.Getpid() {
			found = true
		}
	}
	if !found {
		t.Errorf("PgrpMembers(%d) does not contain self: %v", pgrp, members)
	}

	// A direct child shows up as a reparented candidate (ppid == self).
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("spawn child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	snap2 := ReadSnapshot()
	cands := snap2.ReparentedCandidates(os.Getpid())
	childSeen := false
	for _, pid := range cands {
		if pid == cmd.Process.Pid {
			childSeen = true
		}
	}
	if !childSeen {
		t.Errorf("child %d missing from ReparentedCandidates(self): %v", cmd.Process.Pid, cands)
	}
}

// TestAttributeTreePgid pins the monitoring attribution semantics: pgrp
// members (zombies excluded), marker-matched escapees, and the leader
// double-count guard (the launcher's leader is a direct child carrying
// the marker AND its own group leader — it must appear exactly once).
func TestAttributeTreePgid(t *testing.T) {
	snap := &Snapshot{
		At: time.Now(),
		ByPid: map[int]ProcInfo{
			100: {State: 'S', PPid: 1, Pgrp: 100},    // leader (our direct child)
			101: {State: 'S', PPid: 100, Pgrp: 100},  // in-group member
			102: {State: 'Z', PPid: 100, Pgrp: 100},  // corpse: excluded
			103: {State: 'S', PPid: 9999, Pgrp: 500}, // setsid escapee
			104: {State: 'S', PPid: 9999, Pgrp: 501}, // other app's escapee
		},
	}
	ti := TreeInfo{Mode: ModePgid, LeaderPid: 100}
	orphans := map[int]string{
		100: "app-0", // leader via marker: must be skipped (pgrp guard)
		103: "app-0", // escapee via marker: included
		104: "other", // someone else's: excluded
	}
	got := snap.AttributeTree(ti, "app-0", orphans)
	want := []int{100, 101, 103}
	if len(got) != len(want) {
		t.Fatalf("AttributeTree = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AttributeTree = %v, want %v", got, want)
		}
	}
}

// TestAttributeTreeCgroup: cgroup modes read cgroup.procs fresh; a
// missing dir (tree gone) yields nil, never a guess.
func TestAttributeTreeCgroup(t *testing.T) {
	snap := &Snapshot{At: time.Now(), ByPid: map[int]ProcInfo{}}
	ti := TreeInfo{Mode: ModeCgroupKill, CgroupPath: t.TempDir() + "/nonexistent"}
	if got := snap.AttributeTree(ti, "app-0", nil); got != nil {
		t.Errorf("AttributeTree(missing cgroup) = %v, want nil", got)
	}
}

func TestEnvMarkerValue(t *testing.T) {
	env := []byte("_PM0_APP=web-3\x00PATH=/bin\x00HOME=/root\x00")
	if v, ok := EnvMarkerValue(env); !ok || v != "web-3" {
		t.Errorf("EnvMarkerValue = (%q,%v), want (web-3,true)", v, ok)
	}
	if _, ok := EnvMarkerValue([]byte("PATH=/bin\x00")); ok {
		t.Error("missing marker must report false")
	}
	if v, ok := EnvMarkerValue([]byte("_PM0_APP=\x00")); !ok || v != "" {
		t.Errorf("empty marker value = (%q,%v), want (\"\",true)", v, ok)
	}
}
