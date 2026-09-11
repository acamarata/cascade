package repo

// Purpose: the R-21.180 validation gate. A proposal becomes policy ONLY
//   when (a) schema validation passes AND (b) CORROBORATION holds: a
//   deterministic S-67.T1 detector INDEPENDENTLY produced the same value
//   for that subject, OTHERWISE a human accepts it via the CLI path.
//
//   The corroboration check in this file reads the LIVE S-67.T1
//   Inventory through the real *Store this package already ships
//   (store.go), for the SAME repository id the proposal names -- never a
//   value the ledger itself recorded earlier, never the proposal's own
//   confidence score, and never a second copy of the detector's output
//   duplicated into this file. This is deliberate: this phase's most
//   repeated defect is a validation gate that trusts a number the thing
//   under test itself recorded (a coverage baseline trusting a recorded
//   figure, an allow-list trusting its own prose, a file-write verified
//   against the path it wrote to, a gate scanning its own seeded
//   fixture) rather than deriving the check from an independent source
//   of truth. Corroborate calls store.Get fresh on every call; it holds
//   no cached inventory across calls.
//
//   NOT EVERY SUBJECT IS MECHANICALLY CORROBORABLE from Inventory today.
//   layout_facts and harness_files have no single scalar in Inventory
//   this file can compare a free-text Fact string against without this
//   file inventing its own second parser for the proposal's prose (which
//   would itself be exactly the kind of restated-copy risk this file
//   exists to avoid). corroborable() below names exactly which three
//   subjects (languages, build_cmd/test_cmd/lint_cmd, ci_presence) this
//   file actually checks, and returns false for layout/harness_files --
//   an honest "not derivable here", not a silent pass. A false result
//   from corroborable never accepts a fact; it only means the human-
//   accept path (cmd/cascade/context_facts.go, not built in this pass --
//   see this ticket's journal) is the ONLY path those two subjects can
//   ever reach FactAccepted through.
// Inputs: a proposed InferredFact and the *Store to corroborate it
//   against.
// Outputs: a corroboration verdict (accept/reject/uncorroborated) or a
//   typed error.
// Constraints: fail-closed -- an unresolvable comparison (no stored
//   inventory at all) is UNCORROBORATED, never accepted.
// SPORT: repo/inferred-fact-ledger/ADD (P1-E33-W7-S67-T2).

import (
	"context"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CorroborationVerdict is the outcome of comparing a proposed fact
// against the live deterministic inventory.
type CorroborationVerdict int

const (
	// VerdictUncorroborated means no detector covers this subject (or no
	// inventory is stored at all), so the fact stays FactProposed. This
	// is a normal terminal state, never an error.
	VerdictUncorroborated CorroborationVerdict = iota
	// VerdictCorroborated means a detector independently produced the
	// same value.
	VerdictCorroborated
	// VerdictContradicted means a detector produced a DIFFERENT value
	// for the same subject -- a rejection, with the reason recorded.
	VerdictContradicted
)

// corroborableSubjects is the closed set of subjects this file can
// mechanically check against Inventory today. See this file's doc
// comment for why layout/harness_files are deliberately absent.
var corroborableSubjects = map[FactSubject]bool{
	SubjectLanguages:  true,
	SubjectBuildCmd:   true,
	SubjectTestCmd:    true,
	SubjectLintCmd:    true,
	SubjectCIPresence: true,
}

// Corroborate compares proposed against the LIVE inventory store.Get
// returns for proposed.RepositoryID, right now -- never a value this
// ledger recorded on an earlier call. reason is populated only for
// VerdictContradicted.
func Corroborate(ctx context.Context, store *Store, proposed InferredFact) (verdict CorroborationVerdict, reason string, err error) {
	if store == nil {
		return VerdictUncorroborated, "", cascade.New(cascade.KindInvalidInput, "repo: Corroborate requires a non-nil Store")
	}
	if err := proposed.Validate(); err != nil {
		return VerdictUncorroborated, "", err
	}
	if !corroborableSubjects[proposed.Subject] {
		return VerdictUncorroborated, "", nil
	}

	inv, ok, err := store.Get(ctx, proposed.RepositoryID)
	if err != nil {
		return VerdictUncorroborated, "", err
	}
	if !ok {
		// No deterministic inventory has been scanned for this
		// repository at all -- fail closed to uncorroborated, never to
		// "no contradiction found, so accept".
		return VerdictUncorroborated, "", nil
	}

	switch proposed.Subject {
	case SubjectLanguages:
		return corroborateLanguages(inv, proposed.Fact)
	case SubjectBuildCmd:
		return corroborateCommand(inv, proposed.Fact, func(c Commands) string { return c.Build })
	case SubjectTestCmd:
		return corroborateCommand(inv, proposed.Fact, func(c Commands) string { return c.Test })
	case SubjectLintCmd:
		return corroborateCommand(inv, proposed.Fact, func(c Commands) string { return c.Lint })
	case SubjectCIPresence:
		return corroborateCIPresence(inv, proposed.Fact)
	case SubjectLayout, SubjectHarnessFiles:
		// Deliberately not mechanically corroborable here -- see this
		// file's doc comment. Falls through to the same fail-closed
		// uncorroborated result as the default case below.
		return VerdictUncorroborated, "", nil
	default:
		// Unreachable given corroborableSubjects above, but fail closed
		// rather than falling through silently if that map ever drifts
		// from this switch.
		return VerdictUncorroborated, "", nil
	}
}

// corroborateLanguages checks whether proposedFact names one of the
// languages the LIVE inventory's detectors actually found.
func corroborateLanguages(inv Inventory, proposedFact string) (CorroborationVerdict, string, error) {
	want := strings.TrimSpace(strings.ToLower(proposedFact))
	for _, lf := range inv.Languages {
		if !lf.Detected {
			continue
		}
		if strings.EqualFold(string(lf.Language), want) {
			return VerdictCorroborated, "", nil
		}
	}
	if len(inv.Languages) == 0 {
		return VerdictUncorroborated, "", nil
	}
	return VerdictContradicted, "no detected language in the live inventory matches the proposed value", nil
}

// corroborateCommand checks proposedFact against pick's value across
// every detected language family in the live inventory (a repo can have
// more than one, e.g. a Go backend with a JS frontend; a proposal
// matching ANY detected family's command corroborates).
func corroborateCommand(inv Inventory, proposedFact string, pick func(Commands) string) (CorroborationVerdict, string, error) {
	want := strings.TrimSpace(proposedFact)
	var sawAny bool
	for _, lf := range inv.Languages {
		if !lf.Detected {
			continue
		}
		got := strings.TrimSpace(pick(lf.Commands))
		if got == "" {
			continue
		}
		sawAny = true
		if got == want {
			return VerdictCorroborated, "", nil
		}
	}
	if !sawAny {
		return VerdictUncorroborated, "", nil
	}
	return VerdictContradicted, "no detected command in the live inventory matches the proposed value", nil
}

// corroborateCIPresence checks proposedFact ("true"/"false") against
// whether the live inventory's CI facts show any CI system present.
func corroborateCIPresence(inv Inventory, proposedFact string) (CorroborationVerdict, string, error) {
	want := strings.TrimSpace(strings.ToLower(proposedFact))
	if want != "true" && want != "false" {
		return VerdictUncorroborated, "", cascade.Newf(cascade.KindInvalidInput,
			"repo: ci_presence fact %q is neither \"true\" nor \"false\"", proposedFact)
	}
	got := inv.CI.GitHubActions || inv.CI.GitLabCI || inv.CI.CircleCI
	if (want == "true") == got {
		return VerdictCorroborated, "", nil
	}
	return VerdictContradicted, "proposed ci_presence disagrees with the live inventory's CI facts", nil
}

// Accept transitions f to FactAccepted, recording acceptedBy (a
// corroborating detector id, or a human approver identity for the CLI
// path) and bumping Version. It does not itself decide WHETHER f should
// be accepted -- callers run Corroborate or the human-accept path first
// and only call Accept on a verdict/decision that warrants it.
func Accept(f InferredFact, acceptedBy string) (InferredFact, error) {
	if acceptedBy == "" {
		return InferredFact{}, cascade.New(cascade.KindInvalidInput, "repo: Accept requires a non-empty acceptedBy")
	}
	if f.State != FactProposed {
		return InferredFact{}, cascade.Newf(cascade.KindConflict,
			"repo: cannot accept a fact in state %q, want %q", f.State, FactProposed)
	}
	f.State = FactAccepted
	f.AcceptedBy = acceptedBy
	return f, nil
}

// Reject transitions f to FactRejected, recording reason (a detector
// contradiction, per Corroborate's VerdictContradicted path).
func Reject(f InferredFact, reason string) (InferredFact, error) {
	if reason == "" {
		return InferredFact{}, cascade.New(cascade.KindInvalidInput, "repo: Reject requires a non-empty reason")
	}
	if f.State != FactProposed {
		return InferredFact{}, cascade.Newf(cascade.KindConflict,
			"repo: cannot reject a fact in state %q, want %q", f.State, FactProposed)
	}
	f.State = FactRejected
	f.RejectReason = reason
	return f, nil
}

// Supersede accepts newVersion (via Accept) and marks prior as
// FactSuperseded, recording prior's ID as newVersion's Rollback target.
// prior must already be FactAccepted; newVersion must be FactProposed at
// exactly prior.Version+1.
func Supersede(prior, newVersion InferredFact, acceptedBy string) (superseded, accepted InferredFact, err error) {
	if prior.State != FactAccepted {
		return InferredFact{}, InferredFact{}, cascade.Newf(cascade.KindConflict,
			"repo: cannot supersede a prior fact in state %q, want %q", prior.State, FactAccepted)
	}
	if newVersion.ID != prior.ID {
		return InferredFact{}, InferredFact{}, cascade.New(cascade.KindInvalidInput,
			"repo: Supersede requires newVersion.ID == prior.ID")
	}
	if newVersion.Version != prior.Version+1 {
		return InferredFact{}, InferredFact{}, cascade.Newf(cascade.KindConflict,
			"repo: newVersion.Version %d must be prior.Version+1 (%d)", newVersion.Version, prior.Version+1)
	}
	accepted, err = Accept(newVersion, acceptedBy)
	if err != nil {
		return InferredFact{}, InferredFact{}, err
	}
	accepted.Rollback = rowKeyFor(prior)
	prior.State = FactSuperseded
	return prior, accepted, nil
}

// Rollback reports the fact version rollbackTo should restore, given the
// currently accepted version current. Rolling back a version-1 fact (one
// with no prior version to restore) is a typed error.
func Rollback(current InferredFact) (InferredFact, error) {
	if current.State != FactAccepted {
		return InferredFact{}, cascade.Newf(cascade.KindConflict,
			"repo: cannot roll back a fact in state %q, want %q", current.State, FactAccepted)
	}
	if current.Version <= 1 {
		return InferredFact{}, cascade.Newf(cascade.KindInvalidInput,
			"repo: cannot roll back version %d: no prior version exists", current.Version)
	}
	if current.Rollback == "" {
		return InferredFact{}, cascade.New(cascade.KindInvalidInput,
			"repo: fact carries no rollback reference")
	}
	restored := current
	restored.Version = current.Version - 1
	restored.State = FactAccepted
	return restored, nil
}

// rowKeyFor renders the storage-facing identity of f, for use as a
// Rollback reference. Mirrors ledger_store.go's rowID exactly so a
// Rollback reference is always a valid LedgerStore.Get key.
func rowKeyFor(f InferredFact) string {
	return rowID(f.ID, f.Version)
}
