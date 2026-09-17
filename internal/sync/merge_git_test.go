package sync

// Purpose (this file): phase-state carriage against a fake runner, for the
//   branches a real repository cannot be talked into cheaply. The
//   fast-forward and divergence cases against REAL git live in the tagged
//   lane (merge_git_integration_test.go).
// SPORT: internal/sync tests (ADD) — P1-E17-W4-S38-T2.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage"
)

// scriptedGit answers each git invocation from a script keyed by the
// subcommand, so one branch can fail while the rest succeed.
type scriptedGit struct {
	out  map[string]string
	fail map[string]error
	ran  []string
}

func (g *scriptedGit) Run(_ context.Context, _ string, args ...string) (string, error) {
	key := args[0]
	g.ran = append(g.ran, strings.Join(args, " "))
	if err, bad := g.fail[key]; bad {
		return g.out[key], err
	}
	return g.out[key], nil
}

// phaseDomain is the registered phase-state class.
func phaseDomain(t *testing.T) DomainClass {
	t.Helper()
	dc, ok := Lookup(storage.DomainConfig, "phase-state")
	if !ok {
		t.Fatal("the phase-state domain is not registered")
	}
	return dc
}

// TestErrPhaseStateDiverged is the refusal. The engine never merges phase
// state: a three-way merge of two ticket trees can produce a tree that is
// valid YAML and describes a phase nobody planned.
func TestErrPhaseStateDiverged(t *testing.T) {
	git := &scriptedGit{
		out: map[string]string{
			"remote":    "",
			"fetch":     "",
			"rev-parse": "aaaa111",
			"merge":     "fatal: Not possible to fast-forward, aborting.",
		},
		fail: map[string]error{"merge": errors.New("exit status 128")},
	}
	journal := &ConflictJournal{}

	_, err := CarryPhaseState(context.Background(), git, journal, phaseDomain(t), "/repo", "/remote", "main")
	if err == nil {
		t.Fatal("a non-fast-forward carriage succeeded")
	}
	if !errors.Is(err, ErrPhaseStateDiverged) {
		t.Errorf("err = %v, want it to wrap ErrPhaseStateDiverged", err)
	}
	if !strings.Contains(err.Error(), "aaaa111") {
		t.Errorf("err = %v, want it to name the refs so the divergence is inspectable", err)
	}

	entries := journal.List()
	if len(entries) != 1 {
		t.Fatalf("%d journal entr(y/ies), want 1: a refused carriage must be visible in `sync conflicts list`",
			len(entries))
	}
	e := entries[0]
	if e.Resolution != ResolutionRefused {
		t.Errorf("resolution = %q, want %q", e.Resolution, ResolutionRefused)
	}
	if e.Winner.Ref == "" || e.Loser.Ref == "" {
		t.Errorf("the journal entry carries no refs: %+v", e)
	}
	if e.Strategy != StrategyGitCarried {
		t.Errorf("strategy = %q, want %q", e.Strategy, StrategyGitCarried)
	}
}

// TestCarriageNeverAttemptsAThreeWayMerge is the assertion that stops the
// refusal being softened later: git must be invoked with --ff-only, and
// nothing else may run afterwards.
func TestCarriageNeverAttemptsAThreeWayMerge(t *testing.T) {
	git := &scriptedGit{
		out: map[string]string{
			"remote": "", "fetch": "", "rev-parse": "bbbb222",
			"merge": "fatal: Not possible to fast-forward, aborting.",
		},
		fail: map[string]error{"merge": errors.New("exit status 128")},
	}
	_, _ = CarryPhaseState(context.Background(), git, &ConflictJournal{}, phaseDomain(t), "/repo", "/remote", "main")

	var sawFFOnly bool
	for _, cmd := range git.ran {
		if strings.HasPrefix(cmd, "merge ") {
			if !strings.Contains(cmd, "--ff-only") {
				t.Errorf("carriage ran %q; phase state is never engine-merged", cmd)
			}
			sawFFOnly = true
		}
		for _, forbidden := range []string{"rebase", "cherry-pick", "checkout --theirs", "reset --hard"} {
			if strings.Contains(cmd, forbidden) {
				t.Errorf("carriage ran %q, which resolves a divergence this engine must refuse", cmd)
			}
		}
	}
	if !sawFFOnly {
		t.Error("no merge was attempted at all; the test proved nothing")
	}
}

// TestAFailedFetchIsRefusedNotTreatedAsUpToDate covers the branch where
// the remote is unreachable. Carrying on would fast-forward onto whatever
// FETCH_HEAD happened to hold from a previous run.
func TestAFailedFetchIsRefusedNotTreatedAsUpToDate(t *testing.T) {
	git := &scriptedGit{
		out:  map[string]string{"remote": "", "fetch": "fatal: could not read from remote repository"},
		fail: map[string]error{"fetch": errors.New("exit status 128")},
	}
	_, err := CarryPhaseState(context.Background(), git, &ConflictJournal{}, phaseDomain(t), "/repo", "/gone", "main")
	if err == nil {
		t.Fatal("a failed fetch was treated as up to date")
	}
	for _, cmd := range git.ran {
		if strings.HasPrefix(cmd, "merge") {
			t.Errorf("a merge ran after the fetch failed: %q", cmd)
		}
	}
}

// TestCarriageUsesItsOwnRemoteName proves it never re-points an operator's
// own `origin`, which would redirect their pushes.
func TestCarriageUsesItsOwnRemoteName(t *testing.T) {
	git := &scriptedGit{out: map[string]string{"remote": "", "fetch": "", "rev-parse": "c1", "merge": ""}}
	if _, err := CarryPhaseState(
		context.Background(), git, &ConflictJournal{}, phaseDomain(t), "/repo", "/remote", "main",
	); err != nil {
		t.Fatalf("CarryPhaseState: %v", err)
	}
	for _, cmd := range git.ran {
		if strings.HasPrefix(cmd, "remote ") && strings.Contains(cmd, " origin ") {
			t.Errorf("carriage touched the operator's own remote: %q", cmd)
		}
	}
	if !strings.Contains(strings.Join(git.ran, "|"), carriageRemote) {
		t.Errorf("carriage did not use its own namespaced remote: %v", git.ran)
	}
}
