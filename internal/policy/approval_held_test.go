// Purpose: the grant fixture (a real SQLite queue with a signing minter, a
//
//	real audit log and effect log, the real witness attestor) and the tests
//	of the held record itself: never listed, redeemed once under a
//	16-goroutine race, refused after the effect index is tampered.
//
// SPORT: internal/policy held-record tests (P1-SEC-23).

package policy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeKeySource hands a copy of a test key, the way the vault reader does.
type fakeKeySource struct{ priv ed25519.PrivateKey }

func (k fakeKeySource) ApprovalKey(context.Context) (string, ed25519.PrivateKey, error) {
	return "fake-key-id", append(ed25519.PrivateKey(nil), k.priv...), nil
}

// testSigningMinter mirrors the daemon's signing minter for this package.
type testSigningMinter struct {
	signer   *ApprovalSigner
	verifier *ApprovalVerifier
}

func (m *testSigningMinter) Mint(ctx context.Context, req ApprovalMintRequest) (ApprovalToken, error) {
	signed, err := m.signer.Sign(ctx, ApprovalRecord{Verb: req.Verb, ParamsDigest: req.ParamsHash,
		Requester: req.Subject.String(), Risk: req.Level, Sensitivity: "internal"})
	if err != nil {
		return ApprovalToken{}, err
	}
	rec, err := m.verifier.Verify(signed)
	if err != nil {
		return ApprovalToken{}, err
	}
	return ApprovalToken{RequestID: rec.RequestID.String(), Nonce: rec.Nonce.String(), ActionHash: req.ActionHash,
		ParamsHash: req.ParamsHash, Issued: rec.Issued, Expires: rec.Expiry, Signed: signed}, nil
}

// countingQueue counts ConsumeToken calls; with accept set it answers every
// call with success and never reaches the real queue.
type countingQueue struct {
	ApprovalQueue
	consumed atomic.Int32
	accept   bool
}

func (q *countingQueue) ConsumeToken(ctx context.Context, req ConsumeRequest) (ConsumeResult, error) {
	q.consumed.Add(1)
	if q.accept {
		return ConsumeResult{RequestID: req.RequestID, Capability: "fake.cap", Level: L2}, nil
	}
	return q.ApprovalQueue.ConsumeToken(ctx, req)
}

type redeemFixture struct {
	*approvalFixture
	priv    ed25519.PrivateKey
	log     *audit.Log
	effects *audit.EffectLog
	issuer  *rpc.WitnessIssuer
	counter *countingQueue
	deps    RPCDeps
}

func newRedeemFixture(t *testing.T) *redeemFixture {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	minter := &testSigningMinter{}
	f := newApprovalFixtureWith(t, ApprovalQueueConfig{Batching: ApprovalBatching{WindowSeconds: 10, Cap: 3}, Minter: minter})
	signer, err := NewApprovalSigner(fakeKeySource{priv: priv}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewApprovalVerifier(priv.Public().(ed25519.PublicKey), f.clock)
	if err != nil {
		t.Fatal(err)
	}
	minter.signer, minter.verifier = signer, verifier
	log := audit.New(f.db, f.clock, nil)
	effects, err := audit.NewEffectLog(log)
	if err != nil {
		t.Fatal(err)
	}
	g := &redeemFixture{approvalFixture: f, priv: priv, log: log, effects: effects, issuer: rpc.NewWitnessIssuer(),
		counter: &countingQueue{ApprovalQueue: f.queue}}
	g.deps = RPCDeps{Queue: g.counter, Registry: f.reg, Grants: f.grants, Clock: f.clock, Verifier: verifier,
		Attestor: WitnessAttestor(g.issuer.Verifier()), Held: f.queue, Effects: effects, AuditVerify: log, Recorder: log}
	return g
}

// approved enqueues action and records the human approval (Decide), the
// step the grant requires and never performs itself.
func (g *redeemFixture) approved(t *testing.T, action string) EnqueueResult {
	t.Helper()
	res := g.enqueue(t, action)
	out, err := g.queue.Decide(context.Background(), []DecisionRequest{{
		RequestID: res.RequestID, Approved: true, PresentedSummary: res.Summary, PresentedLevel: res.Level,
	}})
	if err != nil || len(out) != 1 || out[0].Err != nil {
		t.Fatalf("Decide: %v %+v", err, out)
	}
	return res
}

// witness returns a context carrying a real middleware witness for method.
func (g *redeemFixture) witness(t *testing.T, method string) context.Context {
	t.Helper()
	var got context.Context
	attestedDispatch(t, g.issuer, method, func(ctx context.Context) { got = ctx })
	return got
}

// grant calls the guarded approval.grant handler for the askRequest action.
func (g *redeemFixture) grant(ctx context.Context, deps RPCDeps, id, token string) (any, error) {
	req := askRequest("edit")
	params, _ := json.Marshal(ApprovalGrantParams{RequestID: id, SignedToken: token, Action: req.Action, Params: req.Params})
	return MethodHandlers(deps)["approval.grant"](ctx, params)
}

// denials counts the approval.deny rows in the real audit log.
func (g *redeemFixture) denials(t *testing.T) int {
	t.Helper()
	page, err := g.log.Query(context.Background(), audit.Filter{Kinds: []audit.Kind{audit.KindApprovalDeny}})
	if err != nil {
		t.Fatal(err)
	}
	return len(page.Records)
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

func TestHeldSignedRecordNeverListed(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	signed, err := g.queue.HeldSignedRecord(res.RequestID)
	if err != nil || len(signed) <= ed25519.SignatureSize {
		t.Fatalf("held record = %d bytes, %v; this test proves nothing without one", len(signed), err)
	}
	g2 := g.enqueue(t, "edit-two")
	g.queue.mu.Lock()
	held2 := append([]byte(nil), g.queue.entries[g2.RequestID].signed...)
	g.queue.mu.Unlock()
	if len(held2) <= ed25519.SignatureSize {
		t.Fatal("the pending entry holds no signed record; this test proves nothing for it")
	}
	rec2, err := canonicalDecode(held2[:len(held2)-ed25519.SignatureSize])
	if err != nil {
		t.Fatal(err)
	}
	pending, err := g.queue.GetPending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("GetPending = %v, %v; want the pending entry listed", pending, err)
	}
	list, lerr := MethodHandlers(g.deps)["approval.list"](context.Background(), nil)
	show, serr := MethodHandlers(g.deps)["approval.show"](context.Background(), json.RawMessage(`{"request_id":"`+g2.RequestID+`"}`))
	if lerr != nil || serr != nil {
		t.Fatalf("list %v, show %v", lerr, serr)
	}
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, ""); err != nil {
		t.Fatalf("grant: %v", err)
	}
	page, err := g.log.Query(context.Background(), audit.Filter{})
	if err != nil || len(page.Records) == 0 {
		t.Fatalf("journal: %v (%d rows)", err, len(page.Records))
	}
	surfaces := map[string]any{"GetPending": pending, "approval.list": list, "approval.show": show,
		"bridge": BridgePayload(rec2), "grant journal": page.Records, "queue journal": g.sink.events}
	if len(g.sink.events) == 0 {
		t.Fatal("the queue's own recorder holds no rows; the journal leg proves nothing")
	}
	assertNoHeldMaterial(t, surfaces, append(secretNeedles(t, signed), secretNeedles(t, held2)...))
	// The daemon-memory token keeps its nonce by design; the signed bytes
	// must still never serialise from it.
	tokenJSON, _ := json.Marshal(g2.Token)
	for _, needle := range secretNeedles(t, held2)[1:] {
		if bytes.Contains(tokenJSON, []byte(needle)) {
			t.Fatalf("the token serialises its signed record: %s", tokenJSON)
		}
	}
	if _, err := g.queue.HeldSignedRecord(res.RequestID); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("held record after redemption = %v, want KindNotFound", err)
	}
}

func TestGrantConcurrentRedeemOnce(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	g.counter.accept = true
	const n = 16
	ctxs := make([]context.Context, n)
	for i := range ctxs {
		ctxs[i] = g.witness(t, grantMethod)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var wins, denied atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			<-start
			_, err := g.grant(ctx, g.deps, res.RequestID, "")
			switch {
			case err == nil:
				wins.Add(1)
			case cascade.HasKind(err, cascade.KindPermissionDenied):
				denied.Add(1)
			}
		}(ctxs[i])
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 || denied.Load() != n-1 || g.counter.consumed.Load() != 1 {
		t.Fatalf("wins=%d denied=%d consumed=%d; want 1, %d, 1", wins.Load(), denied.Load(), g.counter.consumed.Load(), n-1)
	}
	got := g.effectRecords(t, GrantEffectKey(res.RequestID))
	if got[string(audit.EffectIntent)] != 1 || got[string(audit.EffectConfirmed)] != 1 || len(got) != 2 {
		t.Fatalf("effect records = %v, want one intent and one terminal", got)
	}
}

func TestGrantRefusesAfterEffectIndexTamper(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	g.counter.accept = true
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, ""); err != nil {
		t.Fatalf("first grant: %v", err)
	}
	if phase, ok, err := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID)); err != nil || !ok || phase != audit.EffectConfirmed {
		t.Fatalf("effect after the first grant = %q %v %v, want confirmed", phase, ok, err)
	}
	if err := g.db.Delete(context.Background(), string(storage.DomainAudit), "eff:"+GrantEffectKey(res.RequestID)); err != nil {
		t.Fatalf("deleting the effect row: %v", err)
	}
	if _, ok, _ := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID)); ok {
		t.Fatal("the effect row is still readable; the tamper did not happen")
	}
	before := g.denials(t)
	_, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, "")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || g.denials(t) != before+1 || g.counter.consumed.Load() != 1 {
		t.Fatalf("grant after tamper = %v (denials %d->%d, consumed %d); want an audited refusal and one consume",
			err, before, g.denials(t), g.counter.consumed.Load())
	}
}

func TestRecoverGrantEffectsClosesOnlyGrantIntents(t *testing.T) {
	g := newRedeemFixture(t)
	ctx := context.Background()
	if _, err := g.effects.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindApprovalGrant, Actor: "a", Action: grantMethod, Key: GrantEffectKey("r1")}); err != nil {
		t.Fatal(err)
	}
	n, err := RecoverGrantEffects(ctx, g.effects)
	if phase, _, _ := g.effects.EffectState(ctx, GrantEffectKey("r1")); err != nil || n != 1 || phase != audit.EffectUnknown {
		t.Fatalf("recover = %d %v, phase %q; want 1 and unknown-outcome", n, err, phase)
	}
}
