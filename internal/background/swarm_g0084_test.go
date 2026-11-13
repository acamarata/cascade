package background

import (
	"errors"
	"testing"
)

type fakeGitOps struct {
	refs            map[string]bool
	createdBranches []string
	createdBases    []string
}

func (f *fakeGitOps) RefExists(ref string) bool {
	return f.refs[ref]
}

func (f *fakeGitOps) CreateBranch(name, base string) error {
	f.createdBranches = append(f.createdBranches, name)
	f.createdBases = append(f.createdBases, base)
	f.refs[name] = true
	return nil
}

func TestSwarmBranchFromBase(t *testing.T) {
	t.Run("nil_gitops", func(t *testing.T) {
		branch, err := CreateSwarmBranch(nil, "main", "r1", "t1")
		if branch != "" {
			t.Errorf("expected empty branch, got %q", branch)
		}
		if !errors.Is(err, ErrInvalidSwarmBranch) {
			t.Errorf("expected ErrInvalidSwarmBranch, got %v", err)
		}
	})

	cases := []struct {
		name, base, run, id string
	}{
		{"invalid_base_whitespace", "   ", "r1", "t1"},
		{"invalid_base_dot", "main.", "r1", "t1"},
		{"invalid_run", "main", "r1/x", "t1"},
		{"invalid_id", "main", "r1", "t1/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			branch, err := CreateSwarmBranch(&fakeGitOps{}, tc.base, tc.run, tc.id)
			if branch != "" {
				t.Errorf("expected empty branch, got %q", branch)
			}
			if !errors.Is(err, ErrInvalidSwarmBranch) {
				t.Errorf("expected ErrInvalidSwarmBranch, got %v", err)
			}
		})
	}
}

func TestSwarmBranchFromBaseCreates(t *testing.T) {
	// base="main", run="r1", id="t1"
	t.Run("base_main_run_r1_id_t1", func(t *testing.T) {
		g := &fakeGitOps{refs: map[string]bool{"main": true}}
		branch, err := CreateSwarmBranch(g, "main", "r1", "t1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if branch != "swarm/r1/t1" {
			t.Errorf("expected swarm/r1/t1, got %q", branch)
		}
		if len(g.createdBranches) != 1 {
			t.Errorf("expected 1 call to CreateBranch, got %d", len(g.createdBranches))
		}
		if len(g.createdBranches) > 0 && g.createdBranches[0] != "swarm/r1/t1" {
			t.Errorf("expected CreateBranch(swarm/r1/t1, ...), got %q", g.createdBranches[0])
		}
		if len(g.createdBases) > 0 && g.createdBases[0] != "main" {
			t.Errorf("expected CreateBranch(..., main), got %q", g.createdBases[0])
		}
	})

	// base="", uses master
	t.Run("empty_base_uses_master", func(t *testing.T) {
		g := &fakeGitOps{refs: map[string]bool{"master": true}}
		branch, err := CreateSwarmBranch(g, "", "r1", "t1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if branch != "swarm/r1/t1" {
			t.Errorf("expected swarm/r1/t1, got %q", branch)
		}
		if len(g.createdBranches) != 1 {
			t.Errorf("expected 1 call to CreateBranch, got %d", len(g.createdBranches))
		}
		if len(g.createdBases) > 0 && g.createdBases[0] != "master" {
			t.Errorf("expected CreateBranch(..., master), got %q", g.createdBases[0])
		}
	})
}
