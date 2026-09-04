// Purpose: the admission refusals and the §5.24 remote-approvable payload
// — the elevation-class and deny-list local-only refusals, the malformed
// request paths, the verified params digest, and the three-field
// GetPending struct.
//
// SPORT: internal/policy PendingEntry/ADDED (P1-E09-W2-S18-T3).
package policy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// --- the remote-approvable payload ---------------------------------------

// TestGetPendingPayloadIsolation is the §5.24 assertion: the struct any
// caller receives has exactly three fields, and none of them is the token,
// the nonce or the action hash. It is written STRUCTURALLY, over the type
// itself, because a value-level check would pass the day somebody adds a
// field and leaves it empty in this one case.
func TestGetPendingPayloadIsolation(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)
	res := f.enqueue(t, "edit-a")

	pending, err := f.queue.GetPending(ctx)
	if err != nil {
		t.Fatalf("GetPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("GetPending returned %d entries, want 1", len(pending))
	}
	if pending[0].RequestID != res.RequestID || pending[0].Summary != res.Summary {
		t.Errorf("pending entry = %+v, want the queued request id and summary", pending[0])
	}

	typ := reflect.TypeOf(PendingEntry{})
	want := []string{"RequestID", "Summary", "ExpiresAt"}
	if typ.NumField() != len(want) {
		t.Fatalf("PendingEntry has %d fields, want exactly %d (%v) — every field here can cross a bridge",
			typ.NumField(), len(want), want)
	}
	for i, name := range want {
		if got := typ.Field(i).Name; got != name {
			t.Errorf("field %d is %q, want %q", i, got, name)
		}
	}
	for _, forbidden := range []string{"Token", "Nonce", "ActionHash", "ParamsHash", "Subject", "Capability"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Errorf("PendingEntry carries a %s field; §5.24 keeps it in daemon memory", forbidden)
		}
	}
	if !hasKind(f.sink.kinds(), audit.KindApprovalEnqueue) {
		t.Errorf("audit kinds = %v, want an %s row", f.sink.kinds(), audit.KindApprovalEnqueue)
	}
}

// TestGetPendingOrderingIsStable proves the payload order is admission
// order and not map order, so a surface renders the same list every time.
func TestGetPendingOrderingIsStable(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{
		Batching: ApprovalBatching{WindowSeconds: 3600, Cap: 20},
	})
	want := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		want = append(want, f.enqueue(t, fmt.Sprintf("edit-%d", i)).RequestID)
	}
	for pass := 0; pass < 3; pass++ {
		pending, err := f.queue.GetPending(ctx)
		if err != nil {
			t.Fatalf("GetPending: %v", err)
		}
		got := make([]string, 0, len(pending))
		for _, p := range pending {
			got = append(got, p.RequestID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("pass %d order = %v, want admission order %v", pass, got, want)
		}
	}
}

// --- local-only refusals --------------------------------------------------

// TestElevationClassRefusal proves an elevation-class verb never acquires
// a queued approval, and that the refusal names the verb.
func TestElevationClassRefusal(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)

	req := askRequest("rotate")
	req.Verb = "vault.rotate"
	_, err := f.queue.Enqueue(ctx, req)
	if !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("Enqueue of an elevation-class verb = %v, want ErrLocalOnly", err)
	}
	if !strings.Contains(err.Error(), "vault.rotate") {
		t.Errorf("the refusal %q does not name the verb", err)
	}
	if !errors.Is(err, cascade.ErrElevationRequired) {
		t.Errorf("the refusal does not carry KindElevationRequired: %v", err)
	}
	if len(f.sink.events) != 0 {
		t.Errorf("a local-only refusal wrote %d audit rows; nothing may be queued for it", len(f.sink.events))
	}

	// L4 takes the same route: it is authorized in the same turn, locally.
	l4 := askRequest("rm -rf")
	l4.Level = L4
	if _, err := f.queue.Enqueue(ctx, l4); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("Enqueue of an L4 action = %v, want ErrLocalOnly", err)
	}

	// A non-elevated verb is admitted.
	ok := askRequest("edit-a")
	ok.Verb = "workspace.write"
	if _, err := f.queue.Enqueue(ctx, ok); err != nil {
		t.Fatalf("Enqueue of a non-elevated verb: %v", err)
	}
}

// TestDenyListRefusal proves a deny-listed action is refused at the queue
// boundary, and that a deny-list that cannot answer refuses too.
func TestDenyListRefusal(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{
		Batching: ApprovalBatching{WindowSeconds: 10, Cap: 3},
		DenyList: listDenyList{denied: map[string]bool{"edit-forbidden": true}},
	})
	if _, err := f.queue.Enqueue(ctx, askRequest("edit-forbidden")); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("Enqueue of a deny-listed action = %v, want ErrLocalOnly", err)
	}
	if _, err := f.queue.Enqueue(ctx, askRequest("edit-allowed")); err != nil {
		t.Fatalf("Enqueue of an allowed action: %v", err)
	}

	broken := newApprovalFixtureWith(t, ApprovalQueueConfig{
		DenyList: listDenyList{err: errors.New("deny-list unreadable")},
	})
	if _, err := broken.queue.Enqueue(ctx, askRequest("edit-a")); !errors.Is(err, ErrLocalOnly) {
		t.Fatalf("Enqueue with an unreadable deny-list = %v, want ErrLocalOnly — an unanswerable deny-list denies", err)
	}
}

// --- admission validation -------------------------------------------------

// TestEnqueueRefusesMalformedRequests covers the remaining admission error
// paths: an unknown capability, an invalid subject, empty and oversized
// text, a rung with nothing to ask about, and a bad params digest.
func TestEnqueueRefusesMalformedRequests(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)

	cases := []struct {
		name string
		req  EnqueueRequest
		want error
	}{
		{"unknown capability", withCapability(askRequest("a"), "nope.missing"), ErrCapabilityNotFound},
		{"invalid subject", withSubject(askRequest("a"), Subject{}), ErrSubjectUnknown},
		{"empty action", withAction(askRequest("a"), ""), cascade.ErrInvalidInput},
		{"oversized action", withAction(askRequest("a"), longText()), cascade.ErrInvalidInput},
		{"control character", withAction(askRequest("a"), "edit\x00a"), cascade.ErrInvalidInput},
		{"L0 needs no approval", withLevel(askRequest("a"), L0), ErrNotAskTier},
		{"invalid rung", withLevel(askRequest("a"), RiskLevel(200)), ErrLocalOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.queue.Enqueue(ctx, tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("Enqueue = %v, want %v", err, tc.want)
			}
		})
	}

	t.Run("empty summary", func(t *testing.T) {
		req := askRequest("a")
		req.Summary = ""
		if _, err := f.queue.Enqueue(ctx, req); !errors.Is(err, cascade.ErrInvalidInput) {
			t.Fatalf("Enqueue with no summary = %v, want an invalid-input refusal", err)
		}
	})
}

// TestEnqueueVerifiesSuppliedParamsHash proves a caller-supplied digest is
// CHECKED rather than believed: a wrong-length one and a wrong-value one
// are both refused, and a correct one passes.
func TestEnqueueVerifiesSuppliedParamsHash(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixture(t)

	bad := askRequest("edit-a")
	bad.ParamsHash = "deadbeef"
	if _, err := f.queue.Enqueue(ctx, bad); !errors.Is(err, ErrInvalidParamsHash) {
		t.Fatalf("Enqueue with a short digest = %v, want ErrInvalidParamsHash", err)
	}

	wrong := askRequest("edit-a")
	wrong.ParamsHash = hashApproval([]byte("something else"))
	if _, err := f.queue.Enqueue(ctx, wrong); !errors.Is(err, ErrInvalidParamsHash) {
		t.Fatalf("Enqueue with a non-matching digest = %v, want ErrInvalidParamsHash", err)
	}

	good := askRequest("edit-a")
	good.ParamsHash = hashApproval(good.Params)
	if _, err := f.queue.Enqueue(ctx, good); err != nil {
		t.Fatalf("Enqueue with a correct digest: %v", err)
	}
}

// TestEnqueueRefusesAMinterThatFails proves a minter failure and a minter
// that returns a malformed id both deny rather than filing an entry.
func TestEnqueueRefusesAMinterThatFails(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{
		Minter: &seqMinter{err: errors.New("no entropy")},
	})
	if _, err := f.queue.Enqueue(ctx, askRequest("edit-a")); err == nil {
		t.Fatal("Enqueue with a failing minter = nil error")
	}
	bad := newApprovalFixtureWith(t, ApprovalQueueConfig{Minter: badIDMinter{}})
	if _, err := bad.queue.Enqueue(ctx, askRequest("edit-a")); !errors.Is(err, cascade.ErrInvalidInput) {
		t.Fatalf("Enqueue with a malformed minted id = %v, want an invalid-input refusal", err)
	}
}

// badIDMinter mints an id the storage-key grammar forbids.
type badIDMinter struct{}

func (badIDMinter) Mint(_ context.Context, _ ApprovalMintRequest) (ApprovalToken, error) {
	return ApprovalToken{RequestID: "req/../escape", Nonce: "nonce/../escape"}, nil
}

// TestEnqueueClampsAMinterThatOverreaches proves the §5.24 five-minute
// ceiling is enforced by the QUEUE, not trusted from the minter.
func TestEnqueueClampsAMinterThatOverreaches(t *testing.T) {
	ctx := context.Background()
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{Minter: greedyMinter{}})
	res, err := f.queue.Enqueue(ctx, askRequest("edit-a"))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if got := res.Token.Expires.Sub(baseTime); got > MaxApprovalTTL {
		t.Fatalf("exp is %s after issue, over the §5.24 ceiling of %s", got, MaxApprovalTTL)
	}
}

// greedyMinter asks for a token that outlives the ceiling.
type greedyMinter struct{}

func (greedyMinter) Mint(_ context.Context, req ApprovalMintRequest) (ApprovalToken, error) {
	return ApprovalToken{
		RequestID: "req-greedy", Nonce: "nonce-greedy",
		ActionHash: req.ActionHash, ParamsHash: req.ParamsHash,
		Issued: req.Issued, Expires: req.Issued.Add(24 * time.Hour),
	}, nil
}

// --- small helpers --------------------------------------------------------

func withCapability(r EnqueueRequest, name string) EnqueueRequest { r.Capability = name; return r }
func withSubject(r EnqueueRequest, s Subject) EnqueueRequest      { r.Subject = s; return r }
func withAction(r EnqueueRequest, a string) EnqueueRequest        { r.Action = a; return r }
func withLevel(r EnqueueRequest, l RiskLevel) EnqueueRequest      { r.Level = l; return r }

// longText returns a string over the display limit.
func longText() string {
	buf := make([]byte, maxApprovalTextLen+1)
	for i := range buf {
		buf[i] = 'x'
	}
	return string(buf)
}
