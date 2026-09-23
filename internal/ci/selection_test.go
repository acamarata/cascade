package ci

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newGitFixtureModule creates a real, throwaway git repository in
// t.TempDir() holding a tiny real Go module, commits it, and returns the
// repo root plus that commit's real content-addressed tree hash
// (`git rev-parse HEAD^{tree}`) -- SelectTargets' currentTreeHash check
// runs against this exact real repo, no test double for the git path.
func newGitFixtureModule(t *testing.T) (root, treeHash string) {
	t.Helper()
	root = t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/selectfixture\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib.go"), []byte("package selectfixture\n\nfunc F() int { return 1 }\n"), 0o644); err != nil {
		t.Fatalf("writing lib.go: %v", err)
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "initial")
	treeHash = gitOutput(t, root, "rev-parse", "HEAD^{tree}")
	return root, treeHash
}

// selectTargetsCase is one TestSelectTargets table row.
type selectTargetsCase struct {
	name              string
	model             RequirementModel
	candidateTreeHash string
	riskClass         string
	wantSelection     TargetSelection
	wantFull          bool
}

// selectTargetsCases builds every R-21.173 branch table row, split out of
// TestSelectTargets to keep that function under Art.10.3's 50-line cap.
func selectTargetsCases(t *testing.T, fixtureModuleRoot, freshHash string) []selectTargetsCase {
	t.Helper()
	goModel := RequirementModel{WorktreeRoot: fixtureModuleRoot, Stack: stackGo}
	return []selectTargetsCase{
		{
			name:              "unrecognised stack is uncomputable",
			model:             RequirementModel{WorktreeRoot: t.TempDir(), Stack: "rust", Cfg: Config{}},
			candidateTreeHash: "irrelevant", riskClass: "normal",
			wantSelection: TargetSelectionFull, wantFull: true,
		},
		{
			name:              "wrong candidate tree hash is stale",
			model:             goModel,
			candidateTreeHash: "0000000000000000000000000000000000000000", riskClass: "normal",
			wantSelection: TargetSelectionFull, wantFull: true,
		},
		{
			name: "high risk forces full despite a fresh set", model: goModel,
			candidateTreeHash: freshHash, riskClass: "high",
			wantSelection: TargetSelectionFull, wantFull: true,
		},
		{
			name: "critical risk forces full despite a fresh set", model: goModel,
			candidateTreeHash: freshHash, riskClass: "critical",
			wantSelection: TargetSelectionFull, wantFull: true,
		},
		{
			name: "low risk with a fresh set selects affected", model: goModel,
			candidateTreeHash: freshHash, riskClass: "low",
			wantSelection: TargetSelectionAffected, wantFull: false,
		},
		{
			name: "normal risk with a fresh set selects affected", model: goModel,
			candidateTreeHash: freshHash, riskClass: "normal",
			wantSelection: TargetSelectionAffected, wantFull: false,
		},
	}
}

// runSelectTargetsCase drives SelectTargets for one table row and asserts
// its Selection/Targets/CandidateTreeHash/RiskClass, split out of
// TestSelectTargets to keep that function under Art.10.3's 50-line cap.
func runSelectTargetsCase(t *testing.T, tc selectTargetsCase) {
	t.Helper()
	plan, err := SelectTargets(context.Background(), tc.model, []string{"lib.go"}, tc.candidateTreeHash, tc.riskClass)
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if plan.Selection != tc.wantSelection {
		t.Fatalf("Selection = %q, want %q", plan.Selection, tc.wantSelection)
	}
	isFull := len(plan.Targets) == 1 && plan.Targets[0] == TargetAll
	if isFull != tc.wantFull {
		t.Fatalf("Targets = %v, want full=%v", plan.Targets, tc.wantFull)
	}
	if !tc.wantFull && len(plan.Targets) == 0 {
		t.Fatalf("Targets = %v, want a concrete non-empty affected set", plan.Targets)
	}
	if plan.CandidateTreeHash != tc.candidateTreeHash {
		t.Fatalf("CandidateTreeHash = %q, want %q", plan.CandidateTreeHash, tc.candidateTreeHash)
	}
	if plan.RiskClass != tc.riskClass {
		t.Fatalf("RiskClass = %q, want %q", plan.RiskClass, tc.riskClass)
	}
}

// TestSelectTargets proves every R-21.173 branch: uncomputable, stale,
// High, and Critical all force Selection=full with []Target{TargetAll};
// only Low/Normal with a fresh, concrete affected set yields
// Selection=affected with the real target list.
func TestSelectTargets(t *testing.T) {
	fixtureModuleRoot, freshHash := newGitFixtureModule(t)
	for _, tc := range selectTargetsCases(t, fixtureModuleRoot, freshHash) {
		t.Run(tc.name, func(t *testing.T) { runSelectTargetsCase(t, tc) })
	}
}

// TestSelectTargets_EmptyAffectedWithChangedForcesFull proves D2's
// independent guard: a real affected_cmd that exits zero with no stdout
// (a legitimate misconfiguration, not a synthetic double -- "exit 0" is
// a real subprocess run through the platform shell) must never read as
// Selection=affected with zero Targets. SelectTargets treats "changed is
// non-empty but Affected returned an empty, non-TargetAll set" as
// AffectedStatusUncomputable on its own, independent of D1's Go-path
// fix or whatever bug produced the empty set.
func TestSelectTargets_EmptyAffectedWithChangedForcesFull(t *testing.T) {
	fixtureModuleRoot, freshHash := newGitFixtureModule(t)
	model := RequirementModel{WorktreeRoot: fixtureModuleRoot, Stack: "generic", Cfg: Config{AffectedCmd: "exit 0"}}
	plan, err := SelectTargets(context.Background(), model, []string{"lib.go"}, freshHash, "normal")
	if err != nil {
		t.Fatalf("SelectTargets: %v", err)
	}
	if plan.Selection != TargetSelectionFull {
		t.Fatalf("Selection = %q, want %q", plan.Selection, TargetSelectionFull)
	}
	if len(plan.Targets) != 1 || plan.Targets[0] != TargetAll {
		t.Fatalf("Targets = %v, want [%s]", plan.Targets, TargetAll)
	}
}

// TestSelectTargets_AffectedErrorPropagates proves the confirm round 2
// item-3 design decision (selection.go's own doc comment on this
// branch): a real m.Affected error -- here, a real `exit 1` affected_cmd
// subprocess producing ErrAffectedCmdFailed, no test double (Art.2) --
// is returned VERBATIM by SelectTargets, not folded into
// Selection=full. This is the current, intentional caller-visible
// contract; a future change to fold configuration-error paths into
// fail-closed-full must update this test deliberately, not discover it
// broke by accident.
func TestSelectTargets_AffectedErrorPropagates(t *testing.T) {
	fixtureModuleRoot, freshHash := newGitFixtureModule(t)
	model := RequirementModel{WorktreeRoot: fixtureModuleRoot, Stack: "generic", Cfg: Config{AffectedCmd: "exit 1"}}
	plan, err := SelectTargets(context.Background(), model, []string{"lib.go"}, freshHash, "normal")
	if err == nil {
		t.Fatal("SelectTargets: want a non-nil error from a failing affected_cmd, got nil")
	}
	if !errors.Is(err, ErrAffectedCmdFailed) {
		t.Fatalf("SelectTargets error = %v, want it to wrap ErrAffectedCmdFailed", err)
	}
	if plan.Targets != nil || plan.Selection != "" || plan.CandidateTreeHash != "" {
		t.Fatalf("SelectTargets plan = %+v, want the zero value on error", plan)
	}
}

// TestTargetSelectionFailClosed proves neither TargetSelection nor
// AffectedStatus has a permissive zero-value: an unset TargetSelection
// resolves to full, and an unset AffectedStatus resolves to uncomputable.
func TestTargetSelectionFailClosed(t *testing.T) {
	var selection TargetSelection
	if got := selection.Resolved(); got != TargetSelectionFull {
		t.Fatalf("zero TargetSelection.Resolved() = %q, want %q", got, TargetSelectionFull)
	}
	if TargetSelectionAffected.Resolved() != TargetSelectionAffected {
		t.Fatalf("a set TargetSelection must resolve to itself")
	}

	var status AffectedStatus
	if got := status.Resolved(); got != AffectedStatusUncomputable {
		t.Fatalf("zero AffectedStatus.Resolved() = %q, want %q", got, AffectedStatusUncomputable)
	}
	if AffectedStatusOK.Resolved() != AffectedStatusOK {
		t.Fatalf("a set AffectedStatus must resolve to itself")
	}
}

// TestRiskRequiresFull proves the exact riskClass strings that force
// Selection=full, and that Low/Normal do not.
func TestRiskRequiresFull(t *testing.T) {
	cases := map[string]bool{"low": false, "normal": false, "high": true, "critical": true, "": false, "bogus": false}
	for risk, want := range cases {
		if got := riskRequiresFull(risk); got != want {
			t.Errorf("riskRequiresFull(%q) = %v, want %v", risk, got, want)
		}
	}
}
