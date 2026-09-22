//go:build linux

package supervisor

import (
	"strconv"
	"testing"
)

// TestRegistryVersionBumpsOnMutations pins the lazy-tick contract
// (docs/benchmarks2.md queue item 2): the version counter moves exactly
// on registry mutations, so the ops loop can skip per-tick registry
// copies when nothing changed. A missed bump means a runner lifecycle
// event (start/delete) is invisible until the next mutation.
func TestRegistryVersionBumpsOnMutations(t *testing.T) {
	s := newSupervisor(t)

	v0 := s.Version()

	id := startSleep(t, s, "versioned")
	v1 := s.Version()
	if v1 <= v0 {
		t.Fatalf("start did not bump version: %d -> %d", v0, v1)
	}

	// Non-mutating reads must not move it.
	_, _ = s.Describe("0")
	_ = s.List()
	if s.Version() != v1 {
		t.Fatalf("reads bumped version: %d -> %d", v1, s.Version())
	}

	// Restart is machine-level, not a registry mutation.
	if err := s.Restart("0"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if s.Version() != v1 {
		t.Fatalf("restart bumped version: %d -> %d", v1, s.Version())
	}

	if err := s.Delete(strconv.Itoa(id)); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if s.Version() <= v1 {
		t.Fatalf("delete did not bump version: %d -> %d", v1, s.Version())
	}
}
