package policy

// Purpose: the report vocabulary's own contract — the fail-closed zero
//   shapes, the §5.16 tier resolution, the reach combinator's behaviour on
//   inputs the store cannot currently produce but the type can express,
//   and the read-only preview's two remaining answers (a coalescing
//   duplicate and a params digest that does not match the bytes).
// SPORT: internal/policy DryRunResult/ADDED, GrantRef/ADDED
//   (P1-E09-W2-S18-T4).

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// TestDeniedSimulationHasNoPermissiveZero proves every terminal report is
// filled rather than left as a zero value that would have to be read as
// something.
func TestDeniedSimulationHasNoPermissiveZero(t *testing.T) {
	var zero DryRunResult
	if safeVerdict(zero.Verdict) != VerdictDeny || safeLevel(zero.RiskLevel) != L4 {
		t.Fatal("the zero report does not resolve to a deny at L4")
	}
	for _, level := range []RiskLevel{0, L0, L2, RiskLevel(200)} {
		res := deniedSimulation(level, "because")
		if res.Verdict != VerdictDeny {
			t.Errorf("deniedSimulation(%v).Verdict = %s, want deny", level, res.Verdict)
		}
		if !res.RiskLevel.Valid() {
			t.Errorf("deniedSimulation(%v).RiskLevel = %s, want a valid rung", level, res.RiskLevel)
		}
		if res.EffectiveScope != corpus.VisibilityPrivate {
			t.Errorf("deniedSimulation(%v).EffectiveScope = %q, want private", level, res.EffectiveScope)
		}
		if res.MatchedRule != LayerFailClosed.String() {
			t.Errorf("deniedSimulation(%v).MatchedRule = %q", level, res.MatchedRule)
		}
	}
}

// TestResolveSensitivityFailsClosed covers §5.16 rung by rung: every value
// the enum recognises passes through, and everything else is restricted.
func TestResolveSensitivityFailsClosed(t *testing.T) {
	for _, v := range []corpus.VisibilityClass{
		corpus.VisibilityPrivate, corpus.VisibilityScopeLocal,
		corpus.VisibilityShared, corpus.VisibilityTeam,
	} {
		if got := resolveSensitivity(v); got != v {
			t.Errorf("resolveSensitivity(%q) = %q, want it unchanged", v, got)
		}
	}
	for _, v := range []corpus.VisibilityClass{"", "world", "TEAM", "inherit"} {
		if got := resolveSensitivity(v); got != corpus.VisibilityPrivate {
			t.Errorf("resolveSensitivity(%q) = %q, want private", v, got)
		}
	}
}

// TestEffectiveScopeNarrowsAcrossEveryGrant proves the reported reach is
// the narrowest of everything involved, including across more grants than
// the current store can hold on one capability — the combinator must not
// depend on that limit.
func TestEffectiveScopeNarrowsAcrossEveryGrant(t *testing.T) {
	cases := []struct {
		name     string
		grants   []GrantRef
		override corpus.VisibilityClass
		want     corpus.VisibilityClass
	}{
		{"no grant is private", nil, corpus.VisibilityTeam, corpus.VisibilityPrivate},
		{"one grant under a wider tier keeps its own reach",
			[]GrantRef{{ScopeClass: corpus.VisibilityShared}},
			corpus.VisibilityTeam, corpus.VisibilityShared},
		{"the narrowest of several grants wins", []GrantRef{
			{ScopeClass: corpus.VisibilityTeam},
			{ScopeClass: corpus.VisibilityScopeLocal},
			{ScopeClass: corpus.VisibilityShared},
		}, corpus.VisibilityTeam, corpus.VisibilityScopeLocal},
		{"an unrankable grant collapses the result", []GrantRef{
			{ScopeClass: corpus.VisibilityTeam},
			{ScopeClass: "world"},
		}, corpus.VisibilityTeam, corpus.VisibilityPrivate},
		{"an unresolvable tier restricts a team grant",
			[]GrantRef{{ScopeClass: corpus.VisibilityTeam}}, "", corpus.VisibilityPrivate},
	}
	for _, tc := range cases {
		if got := effectiveScope(tc.grants, tc.override); got != tc.want {
			t.Errorf("%s: effectiveScope = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPreviewCoalescesWithAnOpenPrompt covers the preview's duplicate
// answer: an action already queued inside the open batch is reported
// against the request id the user is ALREADY being asked about, rather
// than as a second question — and reporting it still files nothing.
func TestPreviewCoalescesWithAnOpenPrompt(t *testing.T) {
	ctx := context.Background()
	f := newDryRunFixture(t)
	live, err := f.queue.Enqueue(ctx, askRequest("a.txt"))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	before := f.pending(t)

	res, err := f.queue.previewEnqueue(ctx, askRequest("a.txt"))
	if err != nil {
		t.Fatalf("previewEnqueue: %v", err)
	}
	if !res.Deduplicated || res.RequestID != live.RequestID {
		t.Errorf("preview = %+v, want a coalesce onto %q", res, live.RequestID)
	}
	if got := len(f.pending(t)); got != len(before) {
		t.Errorf("previewing a duplicate changed the pending set to %d entries", got)
	}
}

// TestPreviewRefusesAMismatchedParamsDigest covers the digest check the
// preview runs through the live path's own function: a caller-supplied
// hash is verified against the bytes, never believed, in a prediction as
// much as in the real thing.
func TestPreviewRefusesAMismatchedParamsDigest(t *testing.T) {
	ctx := context.Background()
	f := newDryRunFixture(t)
	req := askRequest("a.txt")
	req.ParamsHash = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := f.queue.previewEnqueue(ctx, req); err == nil {
		t.Fatal("previewEnqueue believed a params digest that does not match the bytes")
	}
	// And the same request refused by the live path refuses identically,
	// which is the point: one check, asked twice.
	_, liveErr := f.queue.Enqueue(ctx, req)
	if liveErr == nil {
		t.Fatal("the live path believed the mismatched digest")
	}
	_, previewErr := f.queue.previewEnqueue(ctx, req)
	if !errors.Is(previewErr, liveErr) && previewErr.Error() != liveErr.Error() {
		t.Errorf("preview refused with %v, live path with %v", previewErr, liveErr)
	}
}
