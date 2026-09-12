// Purpose: DEFECT-recall-broken-fresh-install's proof — `cascade recall`
// with no --scope must succeed and follow --help's own documented "no
// match" path, an explicit --scope must never be overridden, and a
// session with no resolvable working directory must refuse by name
// rather than leak corpus.Membership.Validate()'s internal wording. Split
// from recall_test.go under the 300-line file cap.
//
// SPORT: cmd.cascade.cmd.recall (ADD, per T-3 sport_updates).
package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRecallDefaultsScopeOnFreshInstall is DEFECT-recall-broken-fresh-
// install's own repro, driven through the REAL CLI entry point (the real
// root-mounted cobra RunE, the real rpc.Registry, the real corpus.Query
// path) with NO --scope flag: exactly `cascade recall <query>` on a fresh
// install. Before the fix this failed unconditionally with "corpus:
// membership has an invalid scope reference"; --help documents that a
// query matching nothing prints that and exits 0, and this proves the
// fresh-install default reaches that documented path rather than the
// validator's refusal.
func TestRecallDefaultsScopeOnFreshInstall(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t)}
	stdout, _, err := h.run(t, "reciprocal rank fusion")
	if err != nil {
		t.Fatalf("cascade recall with no --scope must succeed on a fresh install: %v", err)
	}
	if strings.TrimSpace(stdout) != "no results" {
		t.Errorf("stdout = %q, want the documented empty-match message", stdout)
	}
}

// TestRecallExplicitScopeIsNeverOverridden pins that the fresh-install
// default this ticket adds never displaces a caller-supplied --scope: the
// harness's Getwd is made to fail, so if resolveDefaultScope ever
// consulted it despite an explicit --scope, this would refuse instead of
// returning the real, in-scope match.
func TestRecallExplicitScopeIsNeverOverridden(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t), getwdErr: errors.New("must not be consulted")}
	stdout, _, err := h.run(t, "reciprocal rank fusion", "--scope", "project/cascade")
	if err != nil {
		t.Fatalf("an explicit --scope must not be overridden or refused: %v", err)
	}
	if !strings.Contains(stdout, "handbook/fusion.md") {
		t.Fatalf("the explicit scope's own match did not come back:\n%s", stdout)
	}
}

// TestRecallNoScopeAndNoWorkingDirIsRefusedByName is the honest-failure
// half of the fix: when no --scope is given AND no working directory can
// be resolved, resolveDefaultScope has nothing to default and must refuse
// naming --scope, never forwarding another empty scope for
// corpus.Membership.Validate() to reject with ITS internal wording.
func TestRecallNoScopeAndNoWorkingDirIsRefusedByName(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t), getwdErr: errors.New("no such directory")}
	_, _, err := h.run(t, "reciprocal rank fusion")
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want invalid-input", err)
	}
	if !strings.Contains(err.Error(), "--scope") {
		t.Errorf("refusal does not name --scope: %v", err)
	}
	if strings.Contains(err.Error(), "membership has an invalid scope reference") {
		t.Errorf("refusal leaked the validator's internal wording instead of an actionable message: %v", err)
	}
}
