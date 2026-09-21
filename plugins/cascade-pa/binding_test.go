package cascadepa

// Purpose (this file): BindingStore's tests — the fail-closed dispatch answer,
//   the per-user allowlist, restart survival, and the rule the review found
//   broken: a registrar failure must fail the bind.
//
// SPORT: plugins/cascade-pa binding-tests/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingRegistrar records every paired device it was asked to write.
type recordingRegistrar struct {
	mu   sync.Mutex
	seen []string
}

func (r *recordingRegistrar) RegisterPairedDevice(
	_ context.Context, subject, senderID string, _ time.Time,
) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, subject+"/"+senderID)
	return "node-" + senderID, nil
}

func (r *recordingRegistrar) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// refusingRegistrar always refuses.
type refusingRegistrar struct{}

func (refusingRegistrar) RegisterPairedDevice(context.Context, string, string, time.Time) (string, error) {
	return "", errStoreDown
}

func newTestBindingStore(reg DeviceRegistrar, state BridgeState) *BindingStore {
	return NewBindingStore(fixedTestClock{t0()}, reg, state)
}

func TestBindingStore_UnboundSubjectRefused(t *testing.T) {
	s := newTestBindingStore(&recordingRegistrar{}, newMemState())
	allowed, err := s.IsAllowed(context.Background(), testSubject, "user-1")
	if err != nil {
		t.Fatalf("IsAllowed: %v", err)
	}
	if allowed {
		t.Fatal("IsAllowed on an unbound subject = true")
	}
}

func TestBindingStore_BindThenAllowed(t *testing.T) {
	ctx := context.Background()
	s := newTestBindingStore(&recordingRegistrar{}, newMemState())
	b, err := s.Bind(ctx, testSubject, "user-1")
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b.TrustTier != trustTierPairedDevice {
		t.Fatalf("TrustTier = %q, want %q", b.TrustTier, trustTierPairedDevice)
	}
	if b.PairedAt != t0() {
		t.Fatalf("PairedAt = %v, want the injected clock's instant", b.PairedAt)
	}
	if allowed, _ := s.IsAllowed(ctx, testSubject, "user-1"); !allowed {
		t.Fatal("IsAllowed(paired sender) = false")
	}
	if bound, _ := s.Bound(ctx, testSubject); !bound {
		t.Fatal("Bound = false after a successful Bind")
	}
}

// TestBindingStore_TrustTierMatchesTheNodesConstant pins the local string to
// internal/nodes.TierPairedDevice's value without importing it (R-14.69): the
// two must independently equal "paired-device", and 06 §5.22 names the value.
func TestBindingStore_TrustTierMatchesTheNodesConstant(t *testing.T) {
	if trustTierPairedDevice != "paired-device" {
		t.Fatalf("trustTierPairedDevice = %q, want \"paired-device\" (06 §5.22)", trustTierPairedDevice)
	}
}

// TestBindingStore_NonAllowlistedSenderOnBoundBotRefused is R-21.227's per-user
// allowlist: a bound bot is not an open door.
func TestBindingStore_NonAllowlistedSenderOnBoundBotRefused(t *testing.T) {
	ctx := context.Background()
	s := newTestBindingStore(&recordingRegistrar{}, newMemState())
	if _, err := s.Bind(ctx, testSubject, "owner"); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if allowed, _ := s.IsAllowed(ctx, testSubject, "stranger"); allowed {
		t.Fatal("a non-allowlisted sender was admitted on a bound bot")
	}
}

func TestBindingStore_SecondBindAddsToAllowlist(t *testing.T) {
	ctx := context.Background()
	s := newTestBindingStore(&recordingRegistrar{}, newMemState())
	for _, sender := range []string{"user-1", "user-2"} {
		if _, err := s.Bind(ctx, testSubject, sender); err != nil {
			t.Fatalf("Bind(%s): %v", sender, err)
		}
	}
	for _, sender := range []string{"user-1", "user-2"} {
		if allowed, _ := s.IsAllowed(ctx, testSubject, sender); !allowed {
			t.Fatalf("%s lost its place on the allowlist", sender)
		}
	}
	// Re-binding an existing sender must not duplicate it.
	if _, err := s.Bind(ctx, testSubject, "user-1"); err != nil {
		t.Fatalf("re-Bind: %v", err)
	}
	b, err := s.Bind(ctx, testSubject, "user-1")
	if err != nil {
		t.Fatalf("re-Bind: %v", err)
	}
	if len(b.AllowedFrom) != 2 {
		t.Fatalf("allowlist = %v, want exactly two entries", b.AllowedFrom)
	}
}

// TestBindingStore_SurvivesRestart is the durability clause of the ticket's
// failure oracle: a second store over the same state still admits the sender.
func TestBindingStore_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	if _, err := newTestBindingStore(&recordingRegistrar{}, state).Bind(ctx, testSubject, "user-1"); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	restarted := newTestBindingStore(&recordingRegistrar{}, state)
	if allowed, err := restarted.IsAllowed(ctx, testSubject, "user-1"); err != nil || !allowed {
		t.Fatalf("IsAllowed after restart = (%v, %v), want (true, nil)", allowed, err)
	}
	if allowed, _ := restarted.IsAllowed(ctx, testSubject, "stranger"); allowed {
		t.Fatal("the restarted store admitted a sender that was never bound")
	}
}

func TestBindingStore_RequiresSubjectAndSender(t *testing.T) {
	ctx := context.Background()
	s := newTestBindingStore(&recordingRegistrar{}, newMemState())
	for _, tc := range []struct{ subject, sender string }{{"", "u"}, {"s", ""}, {"", ""}} {
		if _, err := s.Bind(ctx, tc.subject, tc.sender); err == nil {
			t.Fatalf("Bind(%q,%q) succeeded", tc.subject, tc.sender)
		}
	}
}

// TestBindingStore_RegistrarFailureFailsTheBind is CR #10's fix: the earlier
// draft discarded this error and reported success.
func TestBindingStore_RegistrarFailureFailsTheBind(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	s := newTestBindingStore(refusingRegistrar{}, state)
	if _, err := s.Bind(ctx, testSubject, "user-1"); err == nil {
		t.Fatal("Bind succeeded although the paired-device record was refused")
	}
	if allowed, _ := s.IsAllowed(ctx, testSubject, "user-1"); allowed {
		t.Fatal("the sender was admitted after a refused registration")
	}
	if _, ok, _ := state.Load(ctx, testSubject); ok {
		t.Fatal("a row was written for a bind that failed")
	}
}

// TestBindingStore_NoRegistrarRefusesTheBind is the fail-closed default: an
// unwired host must not bind at all.
func TestBindingStore_NoRegistrarRefusesTheBind(t *testing.T) {
	s := newTestBindingStore(nil, newMemState())
	_, err := s.Bind(context.Background(), testSubject, "user-1")
	if err == nil {
		t.Fatal("Bind succeeded with no device registrar wired")
	}
	if !strings.Contains(err.Error(), ErrNoDeviceRegistrar.Error()) {
		t.Fatalf("got %v, want the no-registrar refusal", err)
	}
}

func TestBindingStore_RegistrarRunsOnceForANewSender(t *testing.T) {
	reg := &recordingRegistrar{}
	s := newTestBindingStore(reg, newMemState())
	if _, err := s.Bind(context.Background(), testSubject, "user-1"); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if reg.count() != 1 {
		t.Fatalf("registrar was called %d times, want 1", reg.count())
	}
}

// TestBindingStore_NoStateStoreRefusesEverything is the other fail-closed
// default: with no durable state there is no binding to read or write.
func TestBindingStore_NoStateStoreRefusesEverything(t *testing.T) {
	ctx := context.Background()
	s := newTestBindingStore(&recordingRegistrar{}, nil)
	if _, err := s.Bind(ctx, testSubject, "user-1"); err == nil {
		t.Fatal("Bind succeeded with no state store")
	}
	if _, err := s.IsAllowed(ctx, testSubject, "user-1"); err == nil {
		t.Fatal("IsAllowed succeeded with no state store")
	}
	if _, err := s.Bound(ctx, testSubject); err == nil {
		t.Fatal("Bound succeeded with no state store")
	}
}

// TestBindingStore_UnreadableStoreAnswersFalseAndSaysWhy: the dispatch gate
// gets the fail-closed answer AND the reason, so a refusal can be diagnosed
// without telling the sender anything.
func TestBindingStore_UnreadableStoreAnswersFalseAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	state.loadErr = errStoreDown
	s := newTestBindingStore(&recordingRegistrar{}, state)
	allowed, err := s.IsAllowed(ctx, testSubject, "user-1")
	if allowed {
		t.Fatal("IsAllowed = true against an unreadable store")
	}
	if err == nil {
		t.Fatal("IsAllowed swallowed the store failure")
	}
	if _, err := s.Bound(ctx, testSubject); err == nil {
		t.Fatal("Bound swallowed the store failure")
	}
	if _, err := s.Bind(ctx, testSubject, "user-1"); err == nil {
		t.Fatal("Bind succeeded against an unreadable store")
	}
}

// TestBindingStore_WriteFailureFailsTheBind: a bind whose row never landed is
// not a bind.
func TestBindingStore_WriteFailureFailsTheBind(t *testing.T) {
	state := newMemState()
	state.saveErr = errStoreDown
	s := newTestBindingStore(&recordingRegistrar{}, state)
	if _, err := s.Bind(context.Background(), testSubject, "user-1"); err == nil {
		t.Fatal("Bind succeeded although the row was never written")
	}
}

func TestUnconfiguredDeviceRegistrar_ReturnsTypedError(t *testing.T) {
	_, err := unconfiguredDeviceRegistrar{}.RegisterPairedDevice(
		context.Background(), testSubject, "user-1", t0())
	if err == nil {
		t.Fatal("the unconfigured registrar reported success")
	}
	if !strings.Contains(err.Error(), ErrNoDeviceRegistrar.Error()) {
		t.Fatalf("got %v, want ErrNoDeviceRegistrar", err)
	}
}

func TestNewStores_EveryStoreIsUsable(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	stores := NewStores(fixedTestClock{t0()}, &recordingRegistrar{}, state, testPairKey(t))
	code, err := stores.Pairing.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("Pairing.IssueCode: %v", err)
	}
	if outcome, verr := stores.Pairing.VerifyAndConsume(ctx, testSubject, code); verr != nil || !outcome.Bound {
		t.Fatalf("VerifyAndConsume = (%+v, %v)", outcome, verr)
	}
	if _, err := stores.Binding.Bind(ctx, testSubject, "user-1"); err != nil {
		t.Fatalf("Binding.Bind: %v", err)
	}
	if err := stores.Callback.Store(CallbackNonce{Nonce: "n1", ExpiresAt: t0().Add(time.Minute)}); err != nil {
		t.Fatalf("Callback.Store: %v", err)
	}
	fresh, err := stores.Updates.Accept(ctx, testSubject, 1)
	if err != nil || !fresh {
		t.Fatalf("Updates.Accept = (%v, %v)", fresh, err)
	}
	if stores.Clock.Now() != t0() {
		t.Fatalf("Clock.Now = %v", stores.Clock.Now())
	}
}
