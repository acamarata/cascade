// Purpose: approval.grant refuses everything but the daemon's own signed
//
//	record, redeemed once: a random-minter-shaped token, a record signed by
//	another key, a replay and a tokenless call without an approval.grant
//	witness all refuse KindPermissionDenied, audited, with no effect begun.
//
// SPORT: internal/policy approval.grant tests (P1-SEC-23).

package policy

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// permissiveAttestor vouches for everything: it proves the tokenless path
// demands its own witness rather than trusting whatever attestor is wired.
type permissiveAttestor struct{}

func (permissiveAttestor) FreshLocalAttestation(context.Context, string) error { return nil }

// refusedUnchanged asserts an audited KindPermissionDenied that began no
// effect and left the entry pending.
func (g *redeemFixture) refusedUnchanged(t *testing.T, what string, before int, id string, err error) {
	t.Helper()
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("%s = %v, want KindPermissionDenied", what, err)
	}
	if got := g.denials(t); got != before+1 {
		t.Fatalf("%s: %d approval.deny rows after, %d before; want one more", what, got, before)
	}
	if _, found, _ := g.effects.EffectState(context.Background(), GrantEffectKey(id)); found {
		t.Fatalf("%s began a redemption effect", what)
	}
	if _, err := g.queue.HeldSignedRecord(id); err != nil {
		t.Fatalf("%s changed the entry: %v", what, err)
	}
}

func TestGrantRejectsRandomToken(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	random, _ := randomTokenMinter{}.Mint(context.Background(), ApprovalMintRequest{})
	token := base64.StdEncoding.EncodeToString([]byte(random.RequestID + random.Nonce))
	before := g.denials(t)
	_, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, token)
	g.refusedUnchanged(t, "random token", before, res.RequestID, err)
}

func TestGrantRejectsRecordSignedByOtherKey(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	held, _ := g.queue.HeldSignedRecord(res.RequestID)
	rec, err := canonicalDecode(held[:len(held)-ed25519.SignatureSize])
	if err != nil || rec.RequestID.String() != res.RequestID {
		t.Fatalf("decoding the held record: %v", err)
	}
	_, other, _ := ed25519.GenerateKey(nil)
	payload := CanonicalEncode(rec)
	forged := append(append([]byte{}, payload...), ed25519.Sign(other, signingInput(payload))...)
	before := g.denials(t)
	_, err = g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, base64.StdEncoding.EncodeToString(forged))
	g.refusedUnchanged(t, "other-key record", before, res.RequestID, err)
}

func TestGrantReplayRefused(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	held, _ := g.queue.HeldSignedRecord(res.RequestID)
	token := base64.StdEncoding.EncodeToString(held)
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, token); err != nil {
		t.Fatalf("first grant with the signed record: %v", err)
	}
	before := g.denials(t)
	_, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, token)
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || g.denials(t) != before+1 || g.counter.consumed.Load() != 1 {
		t.Fatalf("replay = %v (denials %d->%d, consumed %d); want an audited refusal and one consume",
			err, before, g.denials(t), g.counter.consumed.Load())
	}
}

func TestGrantEmptyTokenRequiresWitness(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	deps := g.deps
	deps.Attestor = permissiveAttestor{}
	before := g.denials(t)
	_, err := g.grant(context.Background(), deps, res.RequestID, "")
	g.refusedUnchanged(t, "tokenless grant without a witness", before, res.RequestID, err)
	before = g.denials(t)
	_, err = g.grant(g.witness(t, "standing_grant.create"), deps, res.RequestID, "")
	g.refusedUnchanged(t, "tokenless grant with another verb's witness", before, res.RequestID, err)
	if g.counter.consumed.Load() != 0 {
		t.Fatal("a refused tokenless grant reached ConsumeToken")
	}
}

func TestGrantEmptyTokenRedeemsHeldRecordOnce(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	out, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, "")
	if err != nil {
		t.Fatalf("tokenless grant: %v", err)
	}
	if got, ok := out.(ApprovalGrantResult); !ok || got.RequestID != res.RequestID || got.Capability != approvalCap().Name {
		t.Fatalf("result = %#v", out)
	}
	if phase, _, _ := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID)); phase != audit.EffectConfirmed {
		t.Fatalf("effect phase = %q, want confirmed", phase)
	}
	before := g.denials(t)
	_, err = g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, "")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || g.denials(t) != before+1 || g.counter.consumed.Load() != 1 {
		t.Fatalf("second grant = %v (consumed %d), want an audited refusal and one consume", err, g.counter.consumed.Load())
	}
}

func TestGrantRefusesWhenNotFullyWired(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	for name, strip := range map[string]func(*RPCDeps){
		"verifier": func(d *RPCDeps) { d.Verifier = nil }, "effects": func(d *RPCDeps) { d.Effects = nil },
		"audit verify": func(d *RPCDeps) { d.AuditVerify = nil }, "held": func(d *RPCDeps) { d.Held = nil },
	} {
		deps := g.deps
		strip(&deps)
		if _, err := g.grant(g.witness(t, grantMethod), deps, res.RequestID, ""); !cascade.HasKind(err, cascade.KindPermissionDenied) {
			t.Fatalf("grant without %s = %v, want KindPermissionDenied", name, err)
		}
	}
	if g.counter.consumed.Load() != 0 {
		t.Fatal("an unwired grant reached ConsumeToken")
	}
}

func TestGrantBeforeApprovalSpendsNothing(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.enqueue(t, "edit")
	if _, err := g.queue.HeldSignedRecord(res.RequestID); !errors.Is(err, ErrApprovalNotApproved) || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("held record of a pending entry = %v, want the not-approved refusal", err)
	}
	_, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, "")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || g.counter.consumed.Load() != 0 {
		t.Fatalf("grant of a pending entry = %v (consumed %d), want a refusal before ConsumeToken", err, g.counter.consumed.Load())
	}
	if _, found, _ := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID)); found {
		t.Fatal("a grant of a pending entry claimed the redemption effect")
	}
	g.queue.mu.Lock()
	signed := append([]byte(nil), g.queue.entries[res.RequestID].signed...)
	g.queue.mu.Unlock()
	_, err = g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, base64.StdEncoding.EncodeToString(signed))
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || g.counter.consumed.Load() != 0 {
		t.Fatalf("token grant of a pending entry = %v (consumed %d), want a refusal before ConsumeToken", err, g.counter.consumed.Load())
	}
	if _, found, _ := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID)); found {
		t.Fatal("a token grant of a pending entry claimed the redemption effect")
	}
	if _, err := g.queue.Decide(context.Background(), []DecisionRequest{{RequestID: res.RequestID, Approved: true,
		PresentedSummary: res.Summary, PresentedLevel: res.Level}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, ""); err != nil {
		t.Fatalf("grant after approval: %v", err)
	}
}

func TestHeldSignedRecordRefusesDeadEntries(t *testing.T) {
	g := newRedeemFixture(t)
	for name, id := range map[string]string{"unknown": "no-such-request"} {
		if _, err := g.queue.HeldSignedRecord(id); !cascade.HasKind(err, cascade.KindNotFound) {
			t.Fatalf("%s: %v, want KindNotFound", name, err)
		}
	}
	denied := g.enqueue(t, "deny-me")
	if _, err := g.queue.Decide(context.Background(), []DecisionRequest{{RequestID: denied.RequestID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.queue.HeldSignedRecord(denied.RequestID); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("denied: %v, want KindNotFound", err)
	}
	live := g.approved(t, "expire-me")
	g.clock.Advance(MaxApprovalTTL + time.Second)
	if _, err := g.queue.HeldSignedRecord(live.RequestID); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("expired: %v, want KindNotFound", err)
	}
	var nilQueue *StoreApprovals
	if _, err := nilQueue.HeldSignedRecord("x"); err == nil {
		t.Fatal("a nil queue served a held record")
	}
	if _, err := RecoverGrantEffects(context.Background(), nil); err == nil {
		t.Fatal("recovery without an effect log succeeded")
	}
}

func TestGrantWrongActionDoesNotBurnApproval(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	req := askRequest("edit")
	params, _ := json.Marshal(ApprovalGrantParams{RequestID: res.RequestID, Action: "a different action", Params: req.Params})
	before := g.denials(t)
	_, err := MethodHandlers(g.deps)["approval.grant"](g.witness(t, grantMethod), params)
	g.refusedUnchanged(t, "grant with the wrong action", before, res.RequestID, err)
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, ""); err != nil || g.counter.consumed.Load() != 1 {
		t.Fatalf("grant with the right action after a mistyped one = %v (consumed %d), want success", err, g.counter.consumed.Load())
	}
}

// secretNeedles are the encodings a leak of one held record would take:
// the full record and its signature raw, base64 (std and URL) and hex, plus
// the record's nonce.
func secretNeedles(t *testing.T, signed []byte) []string {
	t.Helper()
	rec, err := canonicalDecode(signed[:len(signed)-ed25519.SignatureSize])
	if err != nil {
		t.Fatal(err)
	}
	sig := signed[len(signed)-ed25519.SignatureSize:]
	out := []string{rec.Nonce.String()}
	for _, b := range [][]byte{signed, sig} {
		out = append(out, string(b), base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b), hex.EncodeToString(b))
	}
	return out
}
