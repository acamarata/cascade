// Purpose: the held grant refuses every entry that is not pending with that
//
//	state's own refusal, and fails closed on every read or decision error:
//	the entry stays pending and no approval is recorded.
//
// SPORT: internal/policy held-record decision refusal tests (P1-SEC-41).

package policy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// failingEntries reads the real entry, then overrides its state (when set)
// and answers with err alongside the real view, so a caller that ignored
// the error would act on a live, pending entry.
type failingEntries struct {
	src   ApprovalEntrySource
	err   error
	state ApprovalState
}

func (f failingEntries) EntryView(ctx context.Context, id string) (ApprovalEntryView, error) {
	view, err := f.src.EntryView(ctx, id)
	if err != nil {
		return view, err
	}
	if f.state != 0 {
		view.State = f.state
	}
	return view, f.err
}

// failingEffects fails BeginEffect or ConfirmEffect with the given error
// and passes everything else to the real effect log.
type failingEffects struct {
	audit.EffectWriter
	begin, confirm error
}

func (f failingEffects) BeginEffect(ctx context.Context, req audit.EffectRequest) (audit.EffectHandle, error) {
	if f.begin != nil {
		return audit.EffectHandle{}, f.begin
	}
	return f.EffectWriter.BeginEffect(ctx, req)
}

func (f failingEffects) ConfirmEffect(ctx context.Context, h audit.EffectHandle, outcome string) (audit.Record, error) {
	if f.confirm != nil {
		return audit.Record{}, f.confirm
	}
	return f.EffectWriter.ConfirmEffect(ctx, h, outcome)
}

// failingVerify is a sealed audit history that does not verify.
type failingVerify struct{ err error }

func (f failingVerify) Verify(context.Context) error { return f.err }

// inChain reports target by identity anywhere on err's chain.
func inChain(err, target error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == target { //nolint:errorlint // identity is the point
			return true
		}
	}
	return false
}

// kindInChain reports an error of kind anywhere on err's chain.
func kindInChain(err error, kind cascade.Kind) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if k, ok := cascade.KindOf(e); ok && k == kind {
			return true
		}
	}
	return false
}

// refusedWith matches a queue sentinel by code and its message.
func refusedWith(target error, msg string) func(error) bool {
	return func(err error) bool { return errors.Is(err, target) && strings.Contains(err.Error(), msg) }
}

type nonPendingLeg struct {
	name   string
	setup  func(t *testing.T, g *redeemFixture) string
	state  string
	refuse func(error) bool
	phase  audit.EffectPhase // empty: no effect record at all
}

func nonPendingLegs() []nonPendingLeg {
	ctx := context.Background()
	return []nonPendingLeg{
		{"denied", func(t *testing.T, g *redeemFixture) string {
			res := g.enqueue(t, "deny-me")
			if _, err := g.queue.Decide(ctx, []DecisionRequest{{RequestID: res.RequestID}}); err != nil {
				t.Fatal(err)
			}
			return res.RequestID
		}, "denied", refusedWith(ErrApprovalDecided, "already decided"), audit.EffectFailed},
		{"canceled", func(t *testing.T, g *redeemFixture) string {
			res := g.enqueue(t, "cancel-me")
			if err := g.queue.Cancel(ctx, res.RequestID); err != nil {
				t.Fatal(err)
			}
			return res.RequestID
		}, "canceled", refusedWith(ErrApprovalCanceled, "was withdrawn"), audit.EffectFailed},
		{"expired daemon-minted", func(t *testing.T, g *redeemFixture) string {
			res := g.enqueue(t, "lapse-me")
			g.clock.Advance(MaxApprovalTTL + time.Second)
			return res.RequestID
		}, "expired", func(err error) bool {
			return errors.Is(err, ErrExpired) && strings.Contains(err.Error(), "approval token expired at")
		}, ""},
		{"expired entry before record", func(t *testing.T, g *redeemFixture) string {
			g.queue.cfg.Minter = shortExpiryMinter{inner: g.queue.cfg.Minter}
			res := g.enqueue(t, "short-me")
			g.clock.Advance(2 * time.Minute)
			return res.RequestID
		}, "expired", refusedWith(ErrTokenExpired, "expired"), audit.EffectFailed},
		{"consumed", func(t *testing.T, g *redeemFixture) string {
			res := g.approved(t, "edit")
			if _, err := g.redeem(res, "edit"); err != nil {
				t.Fatal(err)
			}
			return res.RequestID
		}, "consumed", func(err error) bool {
			return kindInChain(err, cascade.KindNotFound) && strings.Contains(err.Error(), "no held approval record")
		}, ""},
	}
}

func TestGrantHeldRecordRefusesNonPending(t *testing.T) {
	for _, leg := range nonPendingLegs() {
		t.Run(leg.name, func(t *testing.T) {
			g := newRedeemFixture(t)
			id := leg.setup(t, g)
			approved := g.approvedRows()
			if st := g.showState(t, id); st != leg.state {
				t.Fatalf("state before = %q, want %q", st, leg.state)
			}
			_, err := g.grant(g.witness(t, grantMethod), g.deps, id, "")
			if !cascade.HasKind(err, cascade.KindPermissionDenied) || !leg.refuse(err) {
				t.Fatalf("grant = %v, want its state's refusal under KindPermissionDenied", err)
			}
			phase, found, _ := g.effects.EffectState(context.Background(), GrantEffectKey(id))
			if (leg.phase == "" && found) || (leg.phase != "" && phase != leg.phase) {
				t.Fatalf("effect = %q (found %v), want %q", phase, found, leg.phase)
			}
			if st := g.showState(t, id); st != leg.state || g.approvedRows() != approved {
				t.Fatalf("state after = %q (approved rows %d->%d), want %q unchanged", st, approved, g.approvedRows(), leg.state)
			}
		})
	}
	g := newRedeemFixture(t)
	_, err := g.grant(g.witness(t, grantMethod), g.deps, "no-such-request", "")
	if !cascade.HasKind(err, cascade.KindPermissionDenied) || !kindInChain(err, cascade.KindNotFound) || !strings.Contains(err.Error(), "no held approval record") {
		t.Fatalf("grant of an unknown id = %v, want the held-record refusal", err)
	}
	if _, found, _ := g.effects.EffectState(context.Background(), GrantEffectKey("no-such-request")); found {
		t.Fatal("a grant of an unknown id began an effect")
	}
	if _, err := MethodHandlers(g.deps)["approval.show"](context.Background(), []byte(`{"request_id":"no-such-request"}`)); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("show of an unknown id = %v, want KindNotFound", err)
	}
	pending := g.enqueue(t, "approve-me")
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, pending.RequestID, ""); err != nil || g.showState(t, pending.RequestID) != "approved" {
		t.Fatalf("held grant of a pending entry = %v, want approved", err)
	}
}

type errorDenyLeg struct {
	name   string
	wire   func(g *redeemFixture, q *heldQueue, d *RPCDeps)
	refuse func(error) bool
	effect bool
}

// errorDenyLegs injects each read, decision and effect failure.
func errorDenyLegs() []errorDenyLeg {
	readErr := cascade.New(cascade.KindUnavailable, "fake entry read failure")
	decideErr := cascade.New(cascade.KindInternal, "fake decide failure")
	elemErr := cascade.New(cascade.KindConflict, "fake element refusal")
	beginErr := cascade.New(cascade.KindUnavailable, "fake effect begin failure")
	auditErr := cascade.New(cascade.KindIntegrity, "fake audit chain break")
	return []errorDenyLeg{
		{"entry read error", func(g *redeemFixture, _ *heldQueue, d *RPCDeps) {
			d.Entries = failingEntries{src: g.queue, err: readErr}
		},
			func(err error) bool { return inChain(err, readErr) }, true},
		{"state outside the valid set", func(g *redeemFixture, _ *heldQueue, d *RPCDeps) {
			d.Entries = failingEntries{src: g.queue, state: ApprovalState(200)}
		}, func(err error) bool {
			return cascade.HasKind(err, cascade.KindIntegrity) && strings.Contains(err.Error(), "no valid state")
		}, true},
		{"decide error", func(_ *redeemFixture, q *heldQueue, _ *RPCDeps) { q.err = decideErr },
			func(err error) bool {
				return inChain(err, decideErr) && cascade.HasKind(err, cascade.KindPermissionDenied)
			}, true},
		{"element error", func(_ *redeemFixture, q *heldQueue, _ *RPCDeps) { q.elemErr = elemErr },
			func(err error) bool {
				return inChain(err, elemErr) && cascade.HasKind(err, cascade.KindPermissionDenied)
			}, true},
		{"nil entries", func(_ *redeemFixture, _ *heldQueue, d *RPCDeps) { d.Entries = nil },
			func(err error) bool { return cascade.HasKind(err, cascade.KindUnavailable) }, false},
		{"audit history does not verify", func(_ *redeemFixture, _ *heldQueue, d *RPCDeps) { d.AuditVerify = failingVerify{err: auditErr} },
			func(err error) bool {
				return inChain(err, auditErr) && cascade.HasKind(err, cascade.KindPermissionDenied)
			}, false},
		{"begin effect error", func(g *redeemFixture, _ *heldQueue, d *RPCDeps) {
			d.Effects = failingEffects{EffectWriter: g.effects, begin: beginErr}
		}, func(err error) bool {
			return inChain(err, beginErr) && cascade.HasKind(err, cascade.KindPermissionDenied)
		}, false},
	}
}

func TestGrantHeldRecordErrorsDeny(t *testing.T) {
	legs := errorDenyLegs()
	for _, leg := range legs {
		t.Run(leg.name, func(t *testing.T) {
			g := newRedeemFixture(t)
			q, deps := g.heldDeps()
			leg.wire(g, q, &deps)
			res := g.enqueue(t, "edit")
			_, err := g.grant(g.witness(t, grantMethod), deps, res.RequestID, "")
			if err == nil || !leg.refuse(err) {
				t.Fatalf("grant = %v, want the injected refusal", err)
			}
			view, verr := g.queue.EntryView(context.Background(), res.RequestID)
			phase, found, _ := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID))
			if verr != nil || view.State != ApprovalPending || g.approvedRows() != 0 || found != leg.effect ||
				(leg.effect && phase != audit.EffectFailed) {
				t.Fatalf("after the refusal: state %v (%v), approved rows %d, effect %q found %v",
					view.State, verr, g.approvedRows(), phase, found)
			}
			_, retry := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, "")
			switch {
			case leg.effect && !isEffectExists(retry): // a failed effect is terminal: queue the action again
				t.Fatalf("retry after a failed effect = %v, want ErrEffectExists", retry)
			case !leg.effect && (retry != nil || g.showState(t, res.RequestID) != "approved"):
				t.Fatalf("grant with the collaborators wired = %v, want approved", retry)
			}
		})
	}
	assertConfirmFailureRefuses(t)
}

// assertConfirmFailureRefuses: a decision whose confirm cannot be written is
// a refusal, and its open effect refuses every later grant.
func assertConfirmFailureRefuses(t *testing.T) {
	t.Helper()
	g := newRedeemFixture(t)
	confirmErr := cascade.New(cascade.KindUnavailable, "fake effect confirm failure")
	deps := g.deps
	deps.Effects = failingEffects{EffectWriter: g.effects, confirm: confirmErr}
	res := g.enqueue(t, "edit")
	if _, err := g.grant(g.witness(t, grantMethod), deps, res.RequestID, ""); !inChain(err, confirmErr) ||
		!cascade.HasKind(err, cascade.KindPermissionDenied) || !strings.Contains(err.Error(), "the approval was recorded") {
		t.Fatalf("grant with a failing confirm = %v, want the refusal", err)
	}
	phase, _, _ := g.effects.EffectState(context.Background(), GrantEffectKey(res.RequestID))
	if _, err := g.grant(g.witness(t, grantMethod), g.deps, res.RequestID, ""); phase != audit.EffectIntent || !isEffectExists(err) {
		t.Fatalf("after a failed confirm: effect %q, second grant %v; want an open intent that refuses", phase, err)
	}
	// The decision itself was recorded: the refusal says so, and the owner
	// can still redeem the approved entry once.
	if st := g.showState(t, res.RequestID); st != "approved" || g.approvedRows() != 1 {
		t.Fatalf("after a failed confirm: state %q, %d approved rows; want the recorded approval", st, g.approvedRows())
	}
}
