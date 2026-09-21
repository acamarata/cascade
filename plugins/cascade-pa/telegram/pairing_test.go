package telegram

// Purpose (this file): the pairing flow's tests — bind, refuse, expire, lock
//   out, and the two failures the review found: a bind reported as success
//   when the device record was never written, and a confirmation that echoed
//   whatever string reached it.
//
// SPORT: plugins/cascade-pa/telegram pairing-tests/TEST (P1-E23-W5-S48-T1).

import (
	"context"
	"strings"
	"testing"

	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

// issue puts a fresh code on the rig's subject.
func issue(t *testing.T, rig *testRig) string {
	t.Helper()
	code, err := rig.stores.Pairing.IssueCode(context.Background(), fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	return code
}

func TestPairCoordinator_CorrectCodeBinds(t *testing.T) {
	rig := newDefaultRig(t)
	code := issue(t, rig)
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 999, "/pair "+code))
	if !rig.allows(t, "111") {
		t.Fatal("a correct code did not bind the sender")
	}
	if got := lastSent(t, rig.doer); !strings.HasPrefix(got, replyPaired) {
		t.Fatalf("reply = %q, want the %q confirmation", got, replyPaired)
	}
}

// TestPairCoordinator_ConfirmationNamesTheDigestNotTheToken is the D4 half a
// review found unasserted: the confirmation must carry the digest-derived
// subject, never a token.
func TestPairCoordinator_ConfirmationNamesTheDigestNotTheToken(t *testing.T) {
	state := newMemState()
	clock := fixedTestClock{t0()}
	stores := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	subject := SubjectFromToken(syntheticToken)
	doer := &fakeDoer{}
	client := NewBotClient(subject, doer, &tierGate{}, stores.Updates)
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, nil, clock)
	module := NewTelegramModule(subject, client, stores.Binding, pairer, nodeVerbPolicy())

	ctx := context.Background()
	code, err := stores.Pairing.IssueCode(ctx, fixedEntropy(), subject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	module.dispatch(ctx, textUpdate(1, 222, 999, "/pair "+code))
	got := lastSent(t, doer)
	if !tokenAbsent(got, syntheticToken) {
		t.Fatalf("the confirmation carried the bot token: %q", got)
	}
	if !strings.Contains(got, subject) {
		t.Fatalf("the confirmation %q does not name the subject digest %q", got, subject)
	}
}

func TestPairCoordinator_WrongCodeNeverBinds(t *testing.T) {
	rig := newDefaultRig(t)
	_ = issue(t, rig)
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 999, "/pair 00000000"))
	if rig.allows(t, "111") {
		t.Fatal("a wrong code bound the sender")
	}
	if got := lastSent(t, rig.doer); got != replyPairingFailed {
		t.Fatalf("reply = %q, want %q", got, replyPairingFailed)
	}
}

func TestPairCoordinator_ExpiredCodeNeverBinds(t *testing.T) {
	clock := &advancingClock{at: t0()}
	rig := newRig(t, clock, &okRegistrar{}, nodeVerbPolicy())
	code := issue(t, rig)
	clock.advance(cascadepa.PairCodeTTL + 1)
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 999, "/pair "+code))
	if rig.allows(t, "111") {
		t.Fatal("an expired code bound the sender")
	}
	if got := lastSent(t, rig.doer); got != replyPairingFailed {
		t.Fatalf("reply = %q, want %q", got, replyPairingFailed)
	}
}

// TestPairCodeLockoutBurn is R-16.37's five-attempt rule: the fifth wrong
// candidate burns the outstanding code and emits the typed event, and the
// correct code no longer works afterwards.
func TestPairCodeLockoutBurn(t *testing.T) {
	rig := newDefaultRig(t)
	code := issue(t, rig)
	ctx := context.Background()
	for i := int64(1); i <= cascadepa.PairCodeMaxAttempts; i++ {
		rig.module.dispatch(ctx, textUpdate(i, 111, 999, "/pair ZZZZZZZZ"))
	}
	if rig.sink.count() != 1 {
		t.Fatalf("emitted %d lockout events, want exactly 1", rig.sink.count())
	}
	pending, err := rig.stores.Pairing.Pending(ctx, testSubject)
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if pending {
		t.Fatal("the outstanding code survived the lockout")
	}
	rig.module.dispatch(ctx, textUpdate(99, 111, 999, "/pair "+code))
	if rig.allows(t, "111") {
		t.Fatal("the burned code still bound the sender")
	}
}

// TestPairCoordinator_LockoutCounterSurvivesRestart: a lockout a restart
// resets is not a lockout.
func TestPairCoordinator_LockoutCounterSurvivesRestart(t *testing.T) {
	state := newMemState()
	clock := fixedTestClock{t0()}
	ctx := context.Background()
	first := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	code, err := first.Pairing.IssueCode(ctx, fixedEntropy(), testSubject)
	if err != nil {
		t.Fatalf("IssueCode: %v", err)
	}
	for i := 0; i < cascadepa.PairCodeMaxAttempts-1; i++ {
		if _, verr := first.Pairing.VerifyAndConsume(ctx, testSubject, "ZZZZZZZZ"); verr != nil {
			t.Fatalf("VerifyAndConsume: %v", verr)
		}
	}
	// Restart: a second store over the same durable state.
	second := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	outcome, err := second.Pairing.VerifyAndConsume(ctx, testSubject, "YYYYYYYY")
	if err != nil {
		t.Fatalf("VerifyAndConsume after restart: %v", err)
	}
	if !outcome.LockedOut {
		t.Fatal("the attempt counter reset across a restart; the lockout is defeatable by restarting")
	}
	if _, err := second.Pairing.VerifyAndConsume(ctx, testSubject, code); err != nil {
		t.Fatalf("VerifyAndConsume: %v", err)
	}
	if allowed, _ := second.Binding.IsAllowed(ctx, testSubject, "111"); allowed {
		t.Fatal("the burned code bound a sender after the restart")
	}
}

// TestPairCoordinator_RegistrarFailureIsAPairingFailure is CR #10: the earlier
// draft discarded the registrar's error, so pairing "succeeded" while the
// device registry recorded nothing.
func TestPairCoordinator_RegistrarFailureIsAPairingFailure(t *testing.T) {
	rig := newRig(t, fixedTestClock{t0()}, failingRegistrar{}, nodeVerbPolicy())
	code := issue(t, rig)
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 999, "/pair "+code))
	if got := lastSent(t, rig.doer); got != replyPairingFailed {
		t.Fatalf("reply = %q, want %q — a bind whose device record failed is not a pairing", got, replyPairingFailed)
	}
	if rig.allows(t, "111") {
		t.Fatal("the sender was bound even though the paired-device record was never written")
	}
}

// TestPairCoordinator_RegistrarRunsOnEveryBind pins the positive direction, so
// the test above cannot pass by the registrar never being consulted.
func TestPairCoordinator_RegistrarRunsOnEveryBind(t *testing.T) {
	reg := &okRegistrar{}
	rig := newRig(t, fixedTestClock{t0()}, reg, nodeVerbPolicy())
	code := issue(t, rig)
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 999, "/pair "+code))
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if len(reg.seen) != 1 || reg.seen[0] != testSubject+"/111" {
		t.Fatalf("registrar saw %v, want one %s/111 registration", reg.seen, testSubject)
	}
}

// TestPairCoordinator_AlreadyPairedSenderIsTold: an allowlisted sender that
// sends /pair again gets a distinct, honest answer rather than the stranger's.
func TestPairCoordinator_AlreadyPairedSenderIsTold(t *testing.T) {
	rig := newDefaultRig(t)
	rig.bind(t, "111")
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 999, "/pair ABCDEFGH"))
	if got := lastSent(t, rig.doer); got != replyAlreadyPaired {
		t.Fatalf("reply = %q, want %q", got, replyAlreadyPaired)
	}
}

func TestIsPairCommand(t *testing.T) {
	for _, tc := range []struct {
		in   string
		code string
		ok   bool
	}{
		{"/pair ABCDEFGH", "ABCDEFGH", true},
		{"  /PAIR abcdefgh  ", "abcdefgh", true},
		{"/pair", "", false},
		{"/pairing ABCDEFGH", "", false},
		{"hello", "", false},
		{"", "", false},
	} {
		code, ok := isPairCommand(tc.in)
		if ok != tc.ok || code != tc.code {
			t.Fatalf("isPairCommand(%q) = (%q, %v), want (%q, %v)", tc.in, code, ok, tc.code, tc.ok)
		}
	}
}

func TestDiscardLockoutSinkIsCallable(_ *testing.T) {
	discardLockoutSink{}.EmitLockout(context.Background(), LockoutEvent{Subject: testSubject})
}

// TestPairCoordinator_StoreErrorReportsPairingFailed: an unreachable store must
// answer "pairing failed", never bind and never panic.
func TestPairCoordinator_StoreErrorReportsPairingFailed(t *testing.T) {
	rig := newDefaultRig(t)
	rig.state.loadErr = errTestStoreDown
	rig.module.dispatch(context.Background(), textUpdate(1, 111, 999, "/pair ABCDEFGH"))
	if got := lastSent(t, rig.doer); got != replyNotPaired {
		t.Fatalf("reply = %q, want %q (the Bound() read fails first)", got, replyNotPaired)
	}
}

// TestPairCoordinator_VerifierErrorReportsPairingFailed drives the coordinator
// directly with an unreadable store, so the verifier's own error branch (not
// the dispatch gate's earlier Bound() read) is the one under test.
func TestPairCoordinator_VerifierErrorReportsPairingFailed(t *testing.T) {
	state := newMemState()
	state.loadErr = errTestStoreDown
	clock := fixedTestClock{t0()}
	stores := cascadepa.NewStores(clock, &okRegistrar{}, state, testPairKey(t))
	doer := &fakeDoer{}
	client := NewBotClient(testSubject, doer, &tierGate{}, stores.Updates)
	pairer := newPairCoordinator(stores.Pairing, stores.Binding, nil, clock)
	pairer.handlePairCommand(context.Background(), client, testSubject, 999, "111", "ABCDEFGH")
	if got := lastSent(t, doer); got != replyPairingFailed {
		t.Fatalf("reply = %q, want %q", got, replyPairingFailed)
	}
	allowed, _ := stores.Binding.IsAllowed(context.Background(), testSubject, "111")
	if allowed {
		t.Fatal("a sender was bound while the store was unreadable")
	}
}
