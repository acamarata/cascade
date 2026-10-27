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
	_, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, g.heldToken(t, res.RequestID))
	if !errors.Is(err, ErrApprovalNotApproved) || !strings.Contains(err.Error(), "only an approved request") || g.counter.consumed.Load() != 0 {
		t.Fatalf("token grant of a pending entry = %v (consumed %d), want ErrApprovalNotApproved before ConsumeToken", err, g.counter.consumed.Load())
	}
	if _, found, _ := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID)); found {
		t.Fatal("a token grant of a pending entry claimed the redemption effect")
	}
}

// TestHeldSignedRecordRefusesDeadEntries pins the accessor's states: a copy
// for every retained signed entry that is not consumed or consuming, none
// for an unknown, consumed, consuming or unsigned one.
func TestHeldSignedRecordRefusesDeadEntries(t *testing.T) {
	g := newRedeemFixture(t)
	ids := g.seedEveryState(t)
	for _, state := range []string{"pending", "approved", "denied", "canceled"} {
		if st := g.showState(t, ids[state]); st != state {
			t.Fatalf("%s entry reads %q", state, st)
		}
		g.assertHeldCopy(t, state, ids[state])
	}
	g.clock.Advance(MaxApprovalTTL + time.Second)
	if st := g.showState(t, ids["pending"]); st != "expired" {
		t.Fatalf("lapsed entry reads %q, want expired", st)
	}
	g.assertHeldCopy(t, "expired", ids["pending"])
	unsigned := newApprovalFixtureWith(t, ApprovalQueueConfig{Batching: ApprovalBatching{WindowSeconds: 10, Cap: 3}, Minter: randomTokenMinter{}})
	u := unsigned.enqueue(t, "unsigned")
	for name, held := range map[string]struct {
		q  *StoreApprovals
		id string
	}{"unknown": {g.queue, "no-such-request"}, "consumed": {g.queue, ids["consumed"]},
		"consuming": {g.queue, ids["consuming"]}, "unsigned": {unsigned.queue, u.RequestID}} {
		if _, err := held.q.HeldSignedRecord(held.id); !cascade.HasKind(err, cascade.KindNotFound) {
			t.Fatalf("%s: %v, want KindNotFound", name, err)
		}
	}
	var nilQueue *StoreApprovals
	if _, err := nilQueue.HeldSignedRecord("x"); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("a nil queue = %v, want KindInvalidInput", err)
	}
	if _, err := RecoverGrantEffects(context.Background(), nil); err == nil {
		t.Fatal("recovery without an effect log succeeded")
	}
}

// assertHeldCopy: the accessor returns the held bytes, and writing into the
// returned copy leaves them unchanged.
func (g *redeemFixture) assertHeldCopy(t *testing.T, name, id string) {
	t.Helper()
	g.queue.mu.Lock()
	want := append([]byte(nil), g.queue.entries[id].signed...)
	g.queue.mu.Unlock()
	got, err := g.queue.HeldSignedRecord(id)
	if err != nil || len(want) <= ed25519.SignatureSize || string(got) != string(want) {
		t.Fatalf("%s: held record = %d bytes, %v; want the %d held bytes", name, len(got), err, len(want))
	}
	got[0] ^= 0xff
	if again, _ := g.queue.HeldSignedRecord(id); string(again) != string(want) {
		t.Fatalf("%s: writing into the returned copy changed the held bytes", name)
	}
}

func TestGrantWrongActionDoesNotBurnApproval(t *testing.T) {
	g := newRedeemFixture(t)
	res := g.approved(t, "edit")
	token := g.heldToken(t, res.RequestID)
	req := askRequest("edit")
	params, _ := json.Marshal(ApprovalGrantParams{RequestID: res.RequestID, SignedToken: token, Action: "a different action", Params: req.Params})
	before := g.denials(t)
	_, err := MethodHandlers(g.deps)["approval.grant"](g.witness(t, grantMethod), params)
	g.refusedUnchanged(t, "grant with the wrong action", before, res.RequestID, err)
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, token); err != nil || g.counter.consumed.Load() != 1 {
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
