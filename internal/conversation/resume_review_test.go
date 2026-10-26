package conversation

// Purpose: regression tests for the review of the resume seam: the target
//   tier is validated, and the egress pass sees the SOURCE tier, never the
//   destination's.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestResume_InvalidIntoTier_Refused: an out-of-range destination tier
// would rank as most restrictive and so admit every source (including a
// local-only one); it is refused as invalid input before any read.
func TestResume_InvalidIntoTier_Refused(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThreadAt(t, store, provider.SensitivityLocalOnly, "local only notes")
	sub := &recordingSubst{}
	r, err := NewResumer(store, echoComposer{}, nil, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	res, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityTier(99)})
	if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "is not a sensitivity tier") {
		t.Fatalf("Resume with an invalid IntoTier = %v, want KindInvalidInput naming the tier", err)
	}
	if res.Context != "" || sub.gotTier == provider.SensitivityLocalOnly {
		t.Fatalf("an invalid destination tier admitted the source: result %+v, egress tier %v", res, sub.gotTier)
	}
}

// TestResume_EgressSeesSourceTierNotTarget: a public source resumed into a
// local-only session transits the egress class at PUBLIC, the source's own
// tier. Failing input: a resumer that hands the destination tier to the
// substitution pass.
func TestResume_EgressSeesSourceTierNotTarget(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThreadAt(t, store, provider.SensitivityPublic, "public notes")
	sub := &recordingSubst{}
	r, err := NewResumer(store, echoComposer{}, nil, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityLocalOnly}); err != nil {
		t.Fatalf("Resume public into local-only: %v", err)
	}
	if sub.gotTier != provider.SensitivityPublic {
		t.Fatalf("InterceptClass tier = %v, want public (the source's tier, not the destination's)", sub.gotTier)
	}
}
