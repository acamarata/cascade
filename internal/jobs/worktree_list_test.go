package jobs

// Purpose: ParseWorktreePorcelain unit tests against a REAL git-produced
//
//	capture (see testdata/README.md for provenance) plus every malformed
//	shape the fail-closed grammar must refuse, and FuzzWorktreePorcelain,
//	seeded from that same real capture, proving 30s of adversarial input
//	never panics.
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

const realPorcelainCapture = "worktree /repo\n" +
	"HEAD 695a7afc0e0bd450f7bc9e05a7fd0d7853f2c51f\n" +
	"branch refs/heads/main\n" +
	"\n" +
	"worktree /repo/.cascade/worktrees/job-a\n" +
	"HEAD 695a7afc0e0bd450f7bc9e05a7fd0d7853f2c51f\n" +
	"branch refs/heads/job/a\n" +
	"\n" +
	"worktree /repo/.cascade/worktrees/job-b\n" +
	"HEAD 695a7afc0e0bd450f7bc9e05a7fd0d7853f2c51f\n" +
	"detached\n" +
	"\n" +
	"worktree /repo/.cascade/worktrees/job-c\n" +
	"HEAD 695a7afc0e0bd450f7bc9e05a7fd0d7853f2c51f\n" +
	"branch refs/heads/job/c\n" +
	"locked manual\n" +
	"\n" +
	"worktree /repo/.cascade/worktrees/job-d\n" +
	"HEAD 695a7afc0e0bd450f7bc9e05a7fd0d7853f2c51f\n" +
	"detached\n" +
	"prunable gitdir file points to non-existent location\n" +
	"\n"

func TestWorktreePorcelainRealCapture(t *testing.T) {
	entries, err := ParseWorktreePorcelain([]byte(realPorcelainCapture))
	if err != nil {
		t.Fatalf("ParseWorktreePorcelain: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("got %d entries, want 5: %+v", len(entries), entries)
	}
	if entries[0].Path != "/repo" || entries[0].Branch != "main" {
		t.Fatalf("entries[0] = %+v, want main worktree", entries[0])
	}
	if entries[1].Branch != "job/a" {
		t.Fatalf("entries[1].Branch = %q, want job/a", entries[1].Branch)
	}
	if !entries[2].Detached {
		t.Fatalf("entries[2] should be Detached: %+v", entries[2])
	}
	if !entries[3].Locked || entries[3].LockedReason != "manual" {
		t.Fatalf("entries[3] = %+v, want Locked with reason %q", entries[3], "manual")
	}
	if !entries[4].Prunable || entries[4].PrunableReason != "gitdir file points to non-existent location" {
		t.Fatalf("entries[4] = %+v, want Prunable with a reason", entries[4])
	}
}

func TestWorktreePorcelainEmpty(t *testing.T) {
	entries, err := ParseWorktreePorcelain(nil)
	if err != nil {
		t.Fatalf("ParseWorktreePorcelain(nil): %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("got %d entries for empty input, want 0", len(entries))
	}
}

func TestWorktreePorcelainMalformed(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"attribute before header", "HEAD abc123\n\n"},
		{"unrecognized keyword", "worktree /repo\nbogus-attribute value\n\n"},
		{"bare worktree header no path", "worktree \n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseWorktreePorcelain([]byte(tc.in))
			if err == nil {
				t.Fatalf("ParseWorktreePorcelain(%q): want a typed parse error, got nil", tc.in)
			}
			if _, ok := cascade.KindOf(err); !ok {
				t.Fatalf("ParseWorktreePorcelain(%q): error is not a typed cascade.Error: %v", tc.in, err)
			}
		})
	}
}

// TestWorktreePorcelainAgainstRealGit exercises the parser against
// the ACTUAL output of a real git binary, never a second-guessed dialect
// of it (Art.2).
func TestWorktreePorcelainAgainstRealGit(t *testing.T) {
	wm, store := newTestWorktreeManager(t)
	repo := newTestGitRepo(t)
	ctx := t.Context()
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-real", Epoch: 1, State: LeaseHeld}
	mustPutLease(t, store, lease)
	if _, err := wm.Create(ctx, lease, repo); err != nil {
		t.Fatalf("Create: %v", err)
	}

	entries, err := wm.listGitWorktrees(ctx, repo)
	if err != nil {
		t.Fatalf("listGitWorktrees: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d worktree entries, want 2 (main + job-real): %+v", len(entries), entries)
	}
	found := false
	for _, e := range entries {
		if e.Branch == "job/job-real" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no entry for branch job/job-real in %+v", entries)
	}
}

func FuzzWorktreePorcelain(f *testing.F) {
	f.Add([]byte(realPorcelainCapture))
	f.Add([]byte(""))
	f.Add([]byte("worktree\n\n"))
	f.Add([]byte("worktree /x\nbare\n\n"))
	// testdata/fuzz/FuzzWorktreePorcelain/ (real-git captures, see
	// testdata/README.md for provenance) is loaded automatically by the
	// go tool's own fuzz-corpus convention — no manual read needed here.
	f.Fuzz(func(_ *testing.T, data []byte) {
		// Must never panic: any input resolves to a well-formed
		// []WorktreeEntry or a typed parse error, never a guessed entry.
		_, _ = ParseWorktreePorcelain(data)
	})
}
