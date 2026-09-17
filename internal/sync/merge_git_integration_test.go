//go:build integration

// Purpose: phase-state carriage against REAL git repositories (Art.2 —
//   git is an external contract this repo does not control). Fast-forward
//   eligibility is git's question and git answers it; nothing here
//   reimplements ancestry, because a hand-rolled version would pass its
//   own tests and disagree with the tool that actually moves the refs.
// SPORT: internal/sync TestMergePhaseStateRealGit (P1-E17-W4-S38-T2).

package sync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// realGit shells to the git binary. It lives in a test file because
// internal/sync may not spawn processes in shipped code; production
// injects its own runner through the same seam.
type realGit struct{}

func (realGit) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// newRepo initialises a real repository with one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; this test exercises the real binary")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "--initial-branch=main"},
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := (realGit{}).Run(context.Background(), dir, args...); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}
	commit(t, dir, "phase.yaml", "phase: p1\n", "seed")
	return dir
}

// commit writes a file and commits it.
func commit(t *testing.T, dir, name, body, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", name}, {"commit", "-m", message}} {
		if out, err := (realGit{}).Run(context.Background(), dir, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
}

// head returns dir's current HEAD.
func head(t *testing.T, dir string) string {
	t.Helper()
	out, err := (realGit{}).Run(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, out)
	}
	return strings.TrimSpace(out)
}

// TestMergePhaseStateRealGit drives both outcomes against real
// repositories: a carriage that fast-forwards, and one that cannot.
func TestMergePhaseStateRealGit(t *testing.T) {
	dc, ok := Lookup(storage.DomainConfig, "phase-state")
	if !ok {
		t.Fatal("the phase-state domain is not registered")
	}

	t.Run("fast-forward", func(t *testing.T) {
		local := newRepo(t)
		remote := t.TempDir()
		cloneTo(t, local, remote)
		commit(t, remote, "phase.yaml", "phase: p1\nticket: t1\n", "advance")
		want := head(t, remote)

		got, err := CarryPhaseState(context.Background(), realGit{}, &ConflictJournal{}, dc, local, remote, "main")
		if err != nil {
			t.Fatalf("a clean fast-forward was refused: %v", err)
		}
		if !got.FastForwarded {
			t.Error("the carriage reported no fast-forward")
		}
		if h := head(t, local); h != want {
			t.Errorf("local HEAD = %s, want the remote's %s", h, want)
		}
	})

	t.Run("divergence is refused", func(t *testing.T) {
		local := newRepo(t)
		remote := t.TempDir()
		cloneTo(t, local, remote)
		// Both sides commit independently: a real divergence.
		commit(t, remote, "phase.yaml", "phase: p1\nowner: remote\n", "remote edit")
		commit(t, local, "phase.yaml", "phase: p1\nowner: local\n", "local edit")
		before := head(t, local)

		journal := &ConflictJournal{}
		_, err := CarryPhaseState(context.Background(), realGit{}, journal, dc, local, remote, "main")
		if err == nil {
			t.Fatal("real git fast-forwarded two diverged histories")
		}
		if !errors.Is(err, ErrPhaseStateDiverged) {
			t.Errorf("err = %v, want it to wrap ErrPhaseStateDiverged", err)
		}
		if h := head(t, local); h != before {
			t.Errorf("the refused carriage moved local HEAD from %s to %s", before, h)
		}
		if journal.Len() != 1 {
			t.Errorf("%d journal entr(y/ies) for a refused carriage, want 1", journal.Len())
		}
	})
}

// cloneTo makes dst a clone of src, so the two share history.
func cloneTo(t *testing.T, src, dst string) {
	t.Helper()
	if out, err := (realGit{}).Run(context.Background(), filepath.Dir(dst), "clone", src, dst); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"config", "user.email", "test@example.invalid"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := (realGit{}).Run(context.Background(), dst, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
}
