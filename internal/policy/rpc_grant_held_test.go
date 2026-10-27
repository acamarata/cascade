// Purpose: the tokenless approval.grant records the human approval of a
//
//	pending entry and never redeems: the owner's ConsumeToken redeems once,
//	a second grant refuses, a non-pending entry refuses with its own state's
//	refusal, and the decision binds to the entry's summary and rung.
//
// SPORT: internal/policy held-record decision tests (P1-SEC-41).

package policy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// heldQueue wraps the real queue: it counts Decide and ConsumeToken, records
// every decision it was asked for, and can fail Decide whole (err) or per
// element (elemErr) without reaching the queue.
type heldQueue struct {
	ApprovalQueue
	decides, consumes atomic.Int32
	mu                sync.Mutex
	seen              []DecisionRequest
	err, elemErr      error
}

func (q *heldQueue) Decide(ctx context.Context, reqs []DecisionRequest) ([]DecisionOutcome, error) {
	q.decides.Add(1)
	q.mu.Lock()
	q.seen = append(q.seen, reqs...)
	q.mu.Unlock()
	switch {
	case q.err != nil:
		return nil, q.err
	case q.elemErr != nil:
		return []DecisionOutcome{{RequestID: reqs[0].RequestID, State: ApprovalPending, Err: q.elemErr}}, nil
	}
	return q.ApprovalQueue.Decide(ctx, reqs)
}

func (q *heldQueue) ConsumeToken(ctx context.Context, req ConsumeRequest) (ConsumeResult, error) {
	q.consumes.Add(1)
	return q.ApprovalQueue.ConsumeToken(ctx, req)
}

// heldDeps is the fixture's deps with the counting wrapper as Queue.
func (g *redeemFixture) heldDeps() (*heldQueue, RPCDeps) {
	q := &heldQueue{ApprovalQueue: g.queue}
	deps := g.deps
	deps.Queue = q
	return q, deps
}

// redeem is the owner's one ConsumeToken with the nonce it was handed.
func (g *redeemFixture) redeem(res EnqueueResult, action string) (ConsumeResult, error) {
	req := askRequest(action)
	return g.queue.ConsumeToken(context.Background(), ConsumeRequest{RequestID: res.RequestID,
		Nonce: res.Token.Nonce, Action: req.Action, Params: req.Params})
}

// heldToken is the entry's signed record as a caller-held token.
func (g *redeemFixture) heldToken(t *testing.T, id string) string {
	t.Helper()
	raw, err := g.queue.HeldSignedRecord(id)
	if err != nil {
		t.Fatalf("held record of %s: %v", id, err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// showState is approval.show's state for id.
func (g *redeemFixture) showState(t *testing.T, id string) string {
	t.Helper()
	out, err := MethodHandlers(g.deps)["approval.show"](context.Background(), json.RawMessage(`{"request_id":"`+id+`"}`))
	view, ok := out.(ApprovalShowResult)
	if err != nil || !ok {
		t.Fatalf("approval.show %s = %#v, %v", id, out, err)
	}
	return view.State
}

// queueRows counts the queue journal's approval.grant rows with outcome.
func (g *redeemFixture) queueRows(outcome string) int {
	n := 0
	for _, e := range g.sink.events {
		if e.Kind == audit.KindApprovalGrant && e.Outcome == outcome {
			n++
		}
	}
	return n
}

// approvedRows counts the recorded approval.grant decisions.
func (g *redeemFixture) approvedRows() int { return g.queueRows(ApprovalApproved.String()) }

// isEffectExists reports the effect log's create-only refusal by identity
// and message, never by kind alone.
func isEffectExists(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == audit.ErrEffectExists { //nolint:errorlint // identity is the point
			return strings.Contains(err.Error(), "already begun")
		}
	}
	return false
}

// effectRecords counts the effect records of one key by phase.
func (g *redeemFixture) effectRecords(t *testing.T, key string) map[string]int {
	t.Helper()
	page, err := g.log.Query(context.Background(), audit.Filter{Kinds: []audit.Kind{audit.KindApprovalGrant}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range page.Records {
		if r.EffectKey == key {
			out[r.EffectPhase]++
		}
	}
	return out
}

// grantRedeemShow is TestHeldSignedRecordNeverListed's grant leg: the
// witnessed held grant of the pending entry, the owner's one redemption of
// it, then approval.show of the approved entry shown.
func (g *redeemFixture) grantRedeemShow(t *testing.T, pending EnqueueResult, action, shown string) any {
	t.Helper()
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, pending.RequestID, ""); err != nil {
		t.Fatalf("held grant of the pending entry: %v", err)
	}
	if _, err := g.redeem(pending, action); err != nil {
		t.Fatalf("the owner's redemption: %v", err)
	}
	out, err := MethodHandlers(g.deps)["approval.show"](context.Background(), json.RawMessage(`{"request_id":"`+shown+`"}`))
	if err != nil {
		t.Fatalf("show of the approved entry: %v", err)
	}
	return out
}

// assertNoHeldMaterial fails when any surface's full JSON carries a needle.
func assertNoHeldMaterial(t *testing.T, surfaces map[string]any, needles []string) {
	t.Helper()
	for name, v := range surfaces {
		raw, _ := json.Marshal(v)
		for _, needle := range needles {
			if bytes.Contains(raw, []byte(needle)) {
				t.Fatalf("%s carries held-record material (%d-byte needle): %s", name, len(needle), raw)
			}
		}
	}
}

func TestGrantHeldRecordApprovesNotRedeems(t *testing.T) {
	g := newRedeemFixture(t)
	q, deps := g.heldDeps()
	res := g.enqueue(t, "edit")
	key := GrantEffectKey(res.RequestID)
	unwitnessed := deps
	unwitnessed.Attestor = permissiveAttestor{} // the guard passes; only the handler's own witness check is left
	if _, err := g.grant(context.Background(), unwitnessed, res.RequestID, ""); !cascade.HasKind(err, cascade.KindPermissionDenied) ||
		!strings.Contains(err.Error(), "local attestation for approval.grant") {
		t.Fatalf("grant without a witness = %v, want the witness refusal", err)
	}
	if st := g.showState(t, res.RequestID); st != "pending" || g.approvedRows() != 0 || len(g.effectRecords(t, key)) != 0 {
		t.Fatalf("after an unwitnessed grant: state %q, %d approved rows, effects %v; want pending, 0, none",
			st, g.approvedRows(), g.effectRecords(t, key))
	}
	out, err := g.grant(g.witness(t, grantMethod), deps, res.RequestID, "")
	if err != nil || out != (DecisionResult{RequestID: res.RequestID, State: "approved"}) {
		t.Fatalf("witnessed held grant = %#v, %v; want the approved DecisionResult", out, err)
	}
	effects := g.effectRecords(t, key)
	if st := g.showState(t, res.RequestID); st != "approved" || g.approvedRows() != 1 || q.consumes.Load() != 0 ||
		effects[string(audit.EffectIntent)] != 1 || effects[string(audit.EffectConfirmed)] != 1 {
		t.Fatalf("after the grant: state %q, %d approved rows, %d consumes, effects %v", st, g.approvedRows(), q.consumes.Load(), effects)
	}
	before := g.denials(t)
	_, err = g.grant(g.witness(t, grantMethod), deps, res.RequestID, "")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || !isEffectExists(err) {
		t.Fatalf("second grant = %v, want KindPermissionDenied from ErrEffectExists", err)
	}
	effects = g.effectRecords(t, key)
	if st := g.showState(t, res.RequestID); st != "approved" || g.approvedRows() != 1 || g.queueRows("consumed") != 0 ||
		effects[string(audit.EffectIntent)] != 1 || effects[string(audit.EffectConfirmed)] != 1 || len(effects) != 2 ||
		g.denials(t) > before+1 {
		t.Fatalf("second grant changed state: %q, %d approved rows, effects %v, denials %d->%d",
			st, g.approvedRows(), g.effectRecords(t, key), before, g.denials(t))
	}
	g.assertRefusalRowsClean(t, res)
	if _, err := g.redeem(res, "edit"); err != nil {
		t.Fatalf("the owner's ConsumeToken: %v", err)
	}
	_, err = g.redeem(res, "edit")
	if !errors.Is(err, ErrTokenReplayed) || !strings.Contains(err.Error(), "already been redeemed") {
		t.Fatalf("second ConsumeToken = %v, want ErrTokenReplayed", err)
	}
	if st := g.showState(t, res.RequestID); st != "consumed" || q.consumes.Load() != 0 {
		t.Fatalf("after the owner redeemed: state %q, grant consumes %d", st, q.consumes.Load())
	}
}

// assertRefusalRowsClean: every grant refusal row names the request id and
// none carries its nonce.
func (g *redeemFixture) assertRefusalRowsClean(t *testing.T, res EnqueueResult) {
	t.Helper()
	page, err := g.log.Query(context.Background(), audit.Filter{Kinds: []audit.Kind{audit.KindApprovalDeny}})
	if err != nil || len(page.Records) == 0 {
		t.Fatalf("refusal rows: %v (%d)", err, len(page.Records))
	}
	for _, r := range page.Records {
		if !strings.Contains(r.Outcome, res.RequestID) || strings.Contains(r.Outcome, res.Token.Nonce) {
			t.Fatalf("refusal row %q must name the request id and not its nonce", r.Outcome)
		}
	}
}

func TestGrantHeldRecordNeverConsumes(t *testing.T) {
	g := newRedeemFixture(t)
	q, deps := g.heldDeps()
	signed := g.approved(t, "edit")
	if _, err := g.grant(g.witness(t, grantMethod), deps, signed.RequestID, g.heldToken(t, signed.RequestID)); err != nil ||
		q.consumes.Load() != 1 {
		t.Fatalf("signed-token grant of an approved entry = %v (consumes %d), want one redemption", err, q.consumes.Load())
	}
	pend := g.enqueue(t, "pend")
	_, err := g.grant(g.witness(t, grantMethod), deps, pend.RequestID, g.heldToken(t, pend.RequestID))
	if !errors.Is(err, ErrApprovalNotApproved) || !strings.Contains(err.Error(), "only an approved request") || q.consumes.Load() != 1 {
		t.Fatalf("signed-token grant of a pending entry = %v (consumes %d), want ErrApprovalNotApproved", err, q.consumes.Load())
	}
	held := g.enqueue(t, "held")
	if _, err := g.grant(g.witness(t, grantMethod), deps, held.RequestID, ""); err != nil {
		t.Fatalf("held grant: %v", err)
	}
	q.mu.Lock()
	seen := append([]DecisionRequest(nil), q.seen...)
	q.mu.Unlock()
	want := DecisionRequest{RequestID: held.RequestID, Approved: true, PresentedSummary: held.Summary, PresentedLevel: held.Level}
	if q.consumes.Load() != 1 || q.decides.Load() != 1 || len(seen) != 1 || seen[0] != want {
		t.Fatalf("held grant: consumes %d decides %d, decisions %+v; want no new consume and exactly %+v",
			q.consumes.Load(), q.decides.Load(), seen, want)
	}
}

// shortExpiryMinter proposes an entry exp one minute after issue, earlier
// than the signed record's own Expiry; clampExpiry keeps the earlier one.
type shortExpiryMinter struct{ inner TokenMinter }

func (m shortExpiryMinter) Mint(ctx context.Context, req ApprovalMintRequest) (ApprovalToken, error) {
	tok, err := m.inner.Mint(ctx, req)
	tok.Expires = tok.Issued.Add(time.Minute)
	return tok, err
}
