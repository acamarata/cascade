package conversation

// Purpose: unit tests for resume_topic.go -- thread resolution, the
//   T/S-44.T2 privacy refusal, and turn-slot ordering. Split from
//   resume_test.go under Art.10.3's 300-line cap.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestResume_Topic_NotFound: the failing input is a topic id no thread
// in the store carries.
func TestResume_Topic_NotFound(t *testing.T) {
	store := newResumeStore(t)
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{TopicID: "no-such-thread"})
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("Resume(unknown topic) = %v, want KindNotFound", err)
	}
}

// TestResume_Topic_LocalOnlyRefused: the failing input is a local-only
// thread resumed into a restricted destination. Removing resume_topic.go's
// MoreRestrictiveThan check turns this refusal into a success.
func TestResume_Topic_LocalOnlyRefused(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThreadAt(t, store, provider.SensitivityLocalOnly, "local secret")
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityRestricted})
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("Resume(local-only source, restricted destination) = %v, want KindPolicyDenied", err)
	}
}

// TestResumeRefusesLocalOnlyIntoExternalLane (contract name): a local-only
// source into a restricted destination (one whose content may ride an
// external lane) is refused with ErrResumePrivacyBlocked, and the message
// names the source tier.
func TestResumeRefusesLocalOnlyIntoExternalLane(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThreadAt(t, store, provider.SensitivityLocalOnly, "local secret")
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityRestricted})
	if !errors.Is(err, ErrResumePrivacyBlocked) {
		t.Fatalf("Resume(local-only source, restricted/external-capable destination) = %v, want ErrResumePrivacyBlocked", err)
	}
	if !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("error %q does not name the source tier (Kind alone is not enough proof)", err.Error())
	}
}

// TestResumeLocalOnlyIntoLocalOnlyAllowed (contract name): the same
// local-only thread into an equally local-only destination succeeds, so
// the refusal above is conditioned on the destination, not a blanket ban.
func TestResumeLocalOnlyIntoLocalOnlyAllowed(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThreadAt(t, store, provider.SensitivityLocalOnly, "local secret")
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityLocalOnly}); err != nil {
		t.Fatalf("Resume(local-only source, local-only destination) = %v, want nil (allowed)", err)
	}
}

// TestResume_Topic_RestrictedThread_RestrictedDestination_Allowed: a
// restricted thread (here unmarked, which reads restricted) into an
// equally restricted destination is admitted.
func TestResume_Topic_RestrictedThread_RestrictedDestination_Allowed(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "no privacy row set") // absence means restricted
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityRestricted}); err != nil {
		t.Fatalf("Resume(restricted source, restricted destination) = %v, want nil", err)
	}
}

// TestResume_Topic_RestrictedThread_PublicDestination_Refused: the rule is
// not a local-only special case -- a restricted thread into a public
// destination is refused.
func TestResume_Topic_RestrictedThread_PublicDestination_Refused(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "no privacy row set") // absence means restricted
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityPublic})
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("Resume(restricted source, public destination) = %v, want KindPolicyDenied", err)
	}
}

// TestResume_Topic_RestrictedSource_RestrictedDestination_SubstitutesAndSucceeds:
// a restricted source into a restricted destination succeeds and a real
// vaulted secret is substituted out of the block -- the conversation-resume
// egress class admits every tier, so it is not a second refusal. Uses a
// REAL egress.Engine over a real, seeded vault (scrub_test.go's
// newScrubTestCustody, ForceFileVault -- no real keychain). Failing input:
// narrowing the class's AllowedTiers to {internal, public}.
func TestResume_Topic_RestrictedSource_RestrictedDestination_SubstitutesAndSucceeds(t *testing.T) {
	dir := t.TempDir()
	const name, value = "resume_topic_test_secret", "sk-resume-topic-test-canary-0000000000000000"
	broker, err := secrets.NewBroker(newScrubTestCustody(t, dir), nil)
	if err != nil {
		t.Fatalf("NewBroker: %v", err)
	}
	if _, err := broker.Set(context.Background(), name, []byte(value), secrets.SetUpdate); err != nil {
		t.Fatalf("seed vault: %v", err)
	}
	vault, err := secrets.NewEgressVault(broker)
	if err != nil {
		t.Fatalf("NewEgressVault: %v", err)
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("NewDetector: %v", err)
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), vault, detector)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "unmarked thread carries "+value+" in the clear")
	// No marker: an unmarked thread reads as restricted.
	r, err := NewResumer(store, echoComposer{}, nil, engine)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	res, err := r.Resume(context.Background(), ResumeRequest{TopicID: threadID, IntoTier: provider.SensitivityRestricted})
	if err != nil {
		t.Fatalf("Resume(restricted source, restricted destination) = %v, want nil", err)
	}
	if strings.Contains(res.Context, value) {
		t.Fatalf("Resume leaked the vaulted value: %q", res.Context)
	}
	if !strings.Contains(res.Context, "unmarked thread carries") {
		t.Fatalf("Resume dropped the surrounding text along with the secret: %q", res.Context)
	}
}

// TestBuildHistorySlots_NewestFirst: the failing input is a thread with
// turns in insertion order -- if buildHistorySlots iterated forward
// instead of in reverse, the FIRST (highest-Compose-priority) slot would
// be the OLDEST turn instead of the newest, which this test would catch
// by asserting the Label order directly.
func TestBuildHistorySlots_NewestFirst(t *testing.T) {
	store := newResumeStore(t)
	ctx := context.Background()
	threadID, err := NewThreadID()
	if err != nil {
		t.Fatalf("NewThreadID: %v", err)
	}
	for i := int64(0); i < 3; i++ {
		turn := Turn{ID: NewTurnID(threadID, i, RoleUser), ThreadID: threadID, Seq: i, Role: RoleUser, CreatedAt: i + 1}
		if err := store.AppendTurn(ctx, turn); err != nil {
			t.Fatalf("AppendTurn %d: %v", i, err)
		}
		seg := Segment{ID: NewSegmentID(turn.ID, 0, SegmentText), TurnID: turn.ID, Seq: 0, Kind: SegmentText, Content: "turn body", CreatedAt: i + 1}
		if err := store.AppendSegment(ctx, seg); err != nil {
			t.Fatalf("AppendSegment %d: %v", i, err)
		}
	}
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	slots, err := r.buildHistorySlots(ctx, threadID)
	if err != nil {
		t.Fatalf("buildHistorySlots: %v", err)
	}
	if len(slots) != 3 {
		t.Fatalf("buildHistorySlots returned %d slots, want 3", len(slots))
	}
	wantOrder := []string{
		"turn:" + NewTurnID(threadID, 2, RoleUser),
		"turn:" + NewTurnID(threadID, 1, RoleUser),
		"turn:" + NewTurnID(threadID, 0, RoleUser),
	}
	for i, want := range wantOrder {
		if slots[i].Label != want {
			t.Errorf("slots[%d].Label = %q, want %q (newest-first)", i, slots[i].Label, want)
		}
	}
}

// TestBuildHistorySlots_SkipsEmptySegments proves an empty-content turn
// never becomes an empty Slot (which would waste a Compose priority slot
// on nothing and could trip Compose's own highest-priority-empty
// refusal for no reason).
func TestBuildHistorySlots_SkipsEmptySegments(t *testing.T) {
	store := newResumeStore(t)
	threadID := seedResumeThread(t, store, "")
	r, err := NewResumer(store, echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	slots, err := r.buildHistorySlots(context.Background(), threadID)
	if err != nil {
		t.Fatalf("buildHistorySlots: %v", err)
	}
	if len(slots) != 0 {
		t.Fatalf("buildHistorySlots = %d slots for an empty-content turn, want 0", len(slots))
	}
}
