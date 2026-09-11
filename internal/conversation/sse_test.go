package conversation

// Purpose: unit tests for sse.go's portable RefuseSSEOnEmbedded and
//   emitTurnAppended -- including the two privacy proofs the ticket's
//   trap calls out: an egress-substitution failure never leaks content
//   into the returned error, and a payload marked as containing a
//   vaulted-pattern is refused rather than published unsubstituted.
// SPORT: internal.conversation.sse/ADDED (tests) (P1-E20-W5-S43-T2).

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestRefuseSSEOnEmbedded(t *testing.T) {
	if err := RefuseSSEOnEmbedded(ModeEmbedded); !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("RefuseSSEOnEmbedded(embedded) = %v, want KindUnsupported", err)
	}
	if err := RefuseSSEOnEmbedded(""); err != nil {
		t.Fatalf("RefuseSSEOnEmbedded(\"\") = %v, want nil", err)
	}
}

// fakeBus records every published payload for assertion; it never blocks.
type fakeBus struct {
	published [][]byte
	failWith  error
}

func (f *fakeBus) Publish(_ context.Context, _ string, _ events.EventKind, _ string, payload []byte) (events.Event, error) {
	if f.failWith != nil {
		return events.Event{}, f.failWith
	}
	f.published = append(f.published, payload)
	return events.Event{Seq: uint64(len(f.published))}, nil
}

// passthroughSubst returns content unchanged -- a stand-in for a real
// H/S-16.T1 substitution pass in tests that only exercise the plumbing.
type passthroughSubst struct{}

func (passthroughSubst) Substitute(_ context.Context, content []byte) ([]byte, error) {
	return content, nil
}

// redactingSubst proves substitution actually runs: it replaces the
// literal secretMarker with a fixed tag, so a test can assert the marker
// never reaches the bus.
type redactingSubst struct{ marker string }

func (r redactingSubst) Substitute(_ context.Context, content []byte) ([]byte, error) {
	return []byte(strings.ReplaceAll(string(content), r.marker, "<VAULT_REF>")), nil
}

// failingSubst always fails, with an error message that deliberately
// contains a piece of turn content -- proving emitTurnAppended's caller
// never sees that text (the no-content-leak requirement).
type failingSubst struct{ leaked string }

func (f failingSubst) Substitute(_ context.Context, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindUnavailable, "substitution blew up on %q", f.leaked)
}

func sampleTurn() (Turn, []Segment) {
	turn := Turn{ID: "t1", ThreadID: "th1", Seq: 0, Role: RoleUser, CreatedAt: 1}
	segs := []Segment{{ID: "s1", TurnID: "t1", Seq: 0, Kind: SegmentText, Content: "hello", CreatedAt: 1}}
	return turn, segs
}

func TestEmitTurnAppended_PublishesSubstitutedPayload(t *testing.T) {
	bus := &fakeBus{}
	turn, segs := sampleTurn()
	if _, err := emitTurnAppended(context.Background(), bus, passthroughSubst{}, "", turn, segs); err != nil {
		t.Fatalf("emitTurnAppended: %v", err)
	}
	if len(bus.published) != 1 {
		t.Fatalf("published %d events, want 1", len(bus.published))
	}
	if !strings.Contains(string(bus.published[0]), "hello") {
		t.Fatalf("published payload missing turn content: %s", bus.published[0])
	}
}

// TestEmitTurnAppended_EgressSubstitutionRuns proves the payload actually
// transits the substitutor before it reaches the bus: a marker string
// present in Content must NOT appear verbatim in what fakeBus recorded.
func TestEmitTurnAppended_EgressSubstitutionRuns(t *testing.T) {
	bus := &fakeBus{}
	turn := Turn{ID: "t1", ThreadID: "th1", Seq: 0, Role: RoleUser, CreatedAt: 1}
	segs := []Segment{{ID: "s1", TurnID: "t1", Seq: 0, Kind: SegmentText,
		Content: "AKIA" + "7YQ2XPLM4RZV6WTB", CreatedAt: 1}}
	subst := redactingSubst{marker: "AKIA" + "7YQ2XPLM4RZV6WTB"}
	if _, err := emitTurnAppended(context.Background(), bus, subst, "", turn, segs); err != nil {
		t.Fatalf("emitTurnAppended: %v", err)
	}
	if strings.Contains(string(bus.published[0]), "AKIA") {
		t.Fatalf("vaulted-pattern value survived substitution and reached the bus: %s", bus.published[0])
	}
	if !strings.Contains(string(bus.published[0]), "VAULT_REF") {
		t.Fatalf("substituted tag missing from published payload: %s", bus.published[0])
	}
}

// TestEmitTurnAppended_SubstitutionFailure_NoContentLeak is the ticket's
// explicit trap: an error mid-substitution must never carry conversation
// content into the caller-visible error.
func TestEmitTurnAppended_SubstitutionFailure_NoContentLeak(t *testing.T) {
	bus := &fakeBus{}
	turn, segs := sampleTurn()
	secretFragment := "super-secret-passphrase-content"
	segs[0].Content = secretFragment
	_, gotErr := emitTurnAppended(context.Background(), bus, failingSubst{leaked: secretFragment}, "", turn, segs)
	err := mustFail(t, gotErr)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err kind = %v, want KindUnavailable", err)
	}
	if strings.Contains(err.Error(), secretFragment) {
		t.Fatalf("egress substitution failure leaked content into the error: %v", err)
	}
	if len(bus.published) != 0 {
		t.Fatalf("bus.published = %d, want 0 (nothing must be published on substitution failure)", len(bus.published))
	}
}

func TestEmitTurnAppended_PublishFailure_NoContentLeak(t *testing.T) {
	bus := &fakeBus{failWith: cascade.New(cascade.KindUnavailable, "socket write failed")}
	turn, segs := sampleTurn()
	secretFragment := "another-secret-value"
	segs[0].Content = secretFragment
	_, gotErr := emitTurnAppended(context.Background(), bus, passthroughSubst{}, "", turn, segs)
	err := mustFail(t, gotErr)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err kind = %v, want KindUnavailable", err)
	}
	if strings.Contains(err.Error(), secretFragment) {
		t.Fatalf("sse write failure leaked content into the error: %v", err)
	}
}

func TestEmitTurnAppended_EmbeddedMode_NeverTouchesBusOrSubst(t *testing.T) {
	bus := &fakeBus{failWith: cascade.New(cascade.KindUnavailable, "must never be called")}
	turn, segs := sampleTurn()
	_, gotErr := emitTurnAppended(context.Background(), bus, failingSubst{leaked: "x"}, ModeEmbedded, turn, segs)
	err := mustFail(t, gotErr)
	if !cascade.HasKind(err, cascade.KindUnsupported) {
		t.Fatalf("err kind = %v, want KindUnsupported (ErrSSEUnavailableOnEmbedded)", err)
	}
	if len(bus.published) != 0 {
		t.Fatalf("embedded mode must never publish, got %d", len(bus.published))
	}
}

func TestEmitTurnAppended_NilBus_Unavailable(t *testing.T) {
	turn, segs := sampleTurn()
	_, gotErr := emitTurnAppended(context.Background(), nil, passthroughSubst{}, "", turn, segs)
	err := mustFail(t, gotErr)
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err kind = %v, want KindUnavailable", err)
	}
}

// mustFail asserts err is non-nil and returns it, failing the test with a
// clear message rather than silently ignoring a nil.
func mustFail(t *testing.T, err error) error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	return err
}
