// Package background provides swarm branch utilities.
package background

import (
	"errors"
	"strings"
	"testing"
)

// testGitOps implements GitOps for testing CreateSwarmBranch.
type testGitOps struct {
	refs           map[string]bool
	createAttempts []branchAttempt
}

type branchAttempt struct {
	name string
	base string
}

func (t *testGitOps) RefExists(ref string) bool {
	return t.refs[ref]
}

func (t *testGitOps) CreateBranch(name, base string) error {
	t.createAttempts = append(t.createAttempts, branchAttempt{name: name, base: base})
	return nil
}

// TestSwarmBranchNaming verifies branch naming and collision detection.
func TestSwarmBranchNaming(t *testing.T) {
	// Test successful creation
	g := &testGitOps{refs: map[string]bool{"master": true}}
	name, err := CreateSwarmBranch(g, "master", "r1", "t1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "swarm/r1/t1" {
		t.Errorf("expected branch name 'swarm/r1/t1', got '%s'", name)
	}
	if len(g.createAttempts) != 1 {
		t.Fatalf("expected 1 create attempt, got %d", len(g.createAttempts))
	}
	if g.createAttempts[0].name != "swarm/r1/t1" {
		t.Errorf("expected create name 'swarm/r1/t1', got '%s'", g.createAttempts[0].name)
	}
	if g.createAttempts[0].base != "master" {
		t.Errorf("expected create base 'master', got '%s'", g.createAttempts[0].base)
	}

	// Test pre-existing branch returns ErrSwarmBranchExists
	g2 := &testGitOps{refs: map[string]bool{"master": true, "swarm/r2/t2": true}}
	name2, err2 := CreateSwarmBranch(g2, "master", "r2", "t2")
	if err2 == nil {
		t.Fatal("expected error for existing branch")
	}
	if !errors.Is(err2, ErrSwarmBranchExists) {
		t.Errorf("expected ErrSwarmBranchExists, got %v", err2)
	}
	if !strings.Contains(err2.Error(), "D2222") {
		t.Errorf("expected error to contain 'D2222', got '%s'", err2.Error())
	}
	if name2 != "" {
		t.Errorf("expected empty branch name on error, got '%s'", name2)
	}
	if len(g2.createAttempts) != 0 {
		t.Error("CreateBranch should not be called when branch already exists")
	}
}

// TestSwarmInvalidBaseFailsD2221 verifies that nonexistent base returns ErrInvalidBase.
func TestSwarmInvalidBaseFailsD2221(t *testing.T) {
	g := &testGitOps{refs: map[string]bool{}} // No refs, so base won't exist

	name, err := CreateSwarmBranch(g, "nonexistent-base", "r1", "t1")
	if err == nil {
		t.Fatal("expected error for nonexistent base")
	}
	if !errors.Is(err, ErrInvalidBase) {
		t.Errorf("expected ErrInvalidBase, got %v", err)
	}
	if !strings.Contains(err.Error(), "D2221") {
		t.Errorf("expected error to contain 'D2221', got '%s'", err.Error())
	}
	if name != "" {
		t.Errorf("expected empty branch name on error, got '%s'", name)
	}
	if len(g.createAttempts) != 0 {
		t.Error("CreateBranch should not be called when base doesn't exist")
	}
}
