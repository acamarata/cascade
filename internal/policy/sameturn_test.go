// Purpose: the same-turn window, asserted as a window rather than as a
// feature. Four properties carry the whole design and each has its own
// case: an authorization binds to ONE action, is SINGLE USE, NEVER
// PERSISTS, and can NEVER stand in for an attestation (R-21.231).
//
// SPORT: internal/policy SameTurnLedger/ADDED, NoSameTurnAuth/ADDED
// (P1-E09-W2-S17-T4).
package policy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// newLedger builds a ledger over a frozen clock.
func newLedger(t *testing.T) (*SameTurnLedger, *testkit.FrozenClock) {
	t.Helper()
	clock := testkit.NewFrozenClock(baseTime)
	ledger, err := NewSameTurnLedger(clock)
	if err != nil {
		t.Fatalf("NewSameTurnLedger: %v", err)
	}
	return ledger, clock
}

// TestSameTurnAuthorizeThenCheckThenConsume covers the happy path and the
// single-use rule in one sequence: the authorization answers once and the
// second question for the same action finds nothing.
func TestSameTurnAuthorizeThenCheckThenConsume(t *testing.T) {
	ctx := context.Background()
	ledger, _ := newLedger(t)
	subject := testSubject()

	nonce, err := ledger.Authorize(ctx, subject, "rm -rf /srv")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if len(nonce) != nonceBytes*2 {
		t.Fatalf("nonce %q is not %d hex bytes", nonce, nonceBytes)
	}
	if ledger.Live() != 1 {
		t.Fatalf("Live = %d, want 1", ledger.Live())
	}
	ok, err := ledger.Authorized(ctx, subject, "rm -rf /srv")
	if err != nil || !ok {
		t.Fatalf("Authorized = %v, %v; want true", ok, err)
	}
	ok, err = ledger.Authorized(ctx, subject, "rm -rf /srv")
	if err != nil || ok {
		t.Fatalf("the authorization answered twice (%v, %v): it is not single use", ok, err)
	}
	if ledger.Live() != 0 {
		t.Fatalf("Live = %d after consumption, want 0", ledger.Live())
	}
}

// TestSameTurnBindsToOneActionAndOneSubject is the not-inheritable rule.
// Authorizing one action authorizes THAT action for THAT subject and
// nothing else, in the same turn or any other.
func TestSameTurnBindsToOneActionAndOneSubject(t *testing.T) {
	ctx := context.Background()
	ledger, _ := newLedger(t)
	subject := testSubject()
	if _, err := ledger.Authorize(ctx, subject, "rm -rf /srv"); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	cases := []struct {
		name    string
		subject Subject
		action  string
	}{
		{"a later action in the same turn", subject, "rm -rf /etc"},
		{"the same operation spelled differently", subject, `sh -c 'rm -rf /srv'`},
		{"the same operation with different spacing", subject, "rm  -rf /srv"},
		{"another subject", Subject{Kind: SubjectAgent, ID: "lane-b"}, "rm -rf /srv"},
		{"another subject kind", Subject{Kind: SubjectUser, ID: "lane-a"}, "rm -rf /srv"},
		{"a subject that names nobody", Subject{}, "rm -rf /srv"},
		{"no action at all", subject, ""},
	}
	for _, tc := range cases {
		ok, err := ledger.Authorized(ctx, tc.subject, tc.action)
		if err != nil {
			t.Errorf("%s: Authorized: %v", tc.name, err)
			continue
		}
		if ok {
			t.Errorf("%s: the authorization was inherited by %q", tc.name, tc.action)
		}
	}
	// And the action it WAS issued for is still there.
	if ok, err := ledger.Authorized(ctx, subject, "rm -rf /srv"); err != nil || !ok {
		t.Fatalf("the original authorization was lost: %v, %v", ok, err)
	}
}

// TestSameTurnExpiresAndDoesNotPersist covers both expiry paths: the
// clock-driven backstop window, and EndTurn, which is what a turn ending
// actually calls.
func TestSameTurnExpiresAndDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	ledger, clock := newLedger(t)
	subject := testSubject()

	if _, err := ledger.Authorize(ctx, subject, "rm -rf /srv"); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	clock.Advance(sameTurnWindow)
	if ledger.Live() != 0 {
		t.Errorf("Live = %d at the window boundary, want 0", ledger.Live())
	}
	if ok, err := ledger.Authorized(ctx, subject, "rm -rf /srv"); err != nil || ok {
		t.Errorf("an expired authorization answered %v, %v", ok, err)
	}

	if _, err := ledger.Authorize(ctx, subject, "rm -rf /etc"); err != nil {
		t.Fatalf("Authorize after expiry: %v", err)
	}
	ledger.EndTurn()
	if ok, err := ledger.Authorized(ctx, subject, "rm -rf /etc"); err != nil || ok {
		t.Errorf("an authorization survived the end of its turn: %v, %v", ok, err)
	}
	// Consume is idempotent and needs no prior authorization.
	ledger.Consume(subject, "never-authorized")
	ledger.Consume(subject, "never-authorized")
}

// TestSameTurnRefusesDuplicateAuthorize asserts a second AUTHORIZE for a
// live action is a conflict, so it cannot quietly extend the first
// window. Once the first has expired, a fresh one is allowed.
func TestSameTurnRefusesDuplicateAuthorize(t *testing.T) {
	ctx := context.Background()
	ledger, clock := newLedger(t)
	subject := testSubject()

	if _, err := ledger.Authorize(ctx, subject, "rm -rf /srv"); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	_, err := ledger.Authorize(ctx, subject, "rm -rf /srv")
	if !errors.Is(err, ErrSameTurnDuplicate) {
		t.Fatalf("the duplicate returned %v, want %s", err, CodeSameTurnDuplicate)
	}
	if !strings.Contains(err.Error(), CodeSameTurnDuplicate) {
		t.Errorf("the refusal %q does not name its code", err)
	}
	clock.Advance(sameTurnWindow)
	if _, err := ledger.Authorize(ctx, subject, "rm -rf /srv"); err != nil {
		t.Fatalf("a fresh authorization after expiry was refused: %v", err)
	}
}

// TestSameTurnRefusesElevationClass is R-21.231. The verbs come from
// internal/rpc's canonical §5.14 table by way of IsElevated; there is no
// second list here, and every one of them must be unauthorizable in-turn.
func TestSameTurnRefusesElevationClass(t *testing.T) {
	ctx := context.Background()
	ledger, _ := newLedger(t)
	subject := testSubject()

	elevated := []string{
		"vault.get", "vault.rotate", "approval.grant", "standing_grant.create",
		"backup.restore", "backup.key_export", "perms.grant", "node.enroll",
		"node.remove", "policy.set", "sensitivity.set", "uninstall.purge_data",
	}
	for _, verb := range elevated {
		nonce, err := ledger.Authorize(ctx, subject, verb)
		if !errors.Is(err, ErrSameTurnElevation) {
			t.Errorf("Authorize(%q) = %q, %v; want %s", verb, nonce, err, CodeSameTurnElevation)
		}
		if !errors.Is(err, cascade.ErrElevationRequired) {
			t.Errorf("Authorize(%q) refusal does not carry KindElevationRequired: %v", verb, err)
		}
		if ok, checkErr := ledger.Authorized(ctx, subject, verb); ok || checkErr != nil {
			t.Errorf("a refused elevation verb %q is still authorized (%v, %v)",
				verb, ok, checkErr)
		}
	}
	if ledger.Live() != 0 {
		t.Fatalf("a refused authorization was recorded: Live = %d", ledger.Live())
	}
	// A non-elevated action is unaffected.
	if _, err := ledger.Authorize(ctx, subject, "rm -rf /srv"); err != nil {
		t.Fatalf("a non-elevated action was refused: %v", err)
	}
}

// TestSameTurnAuthorizeRefusesUnusableInput covers the remaining error
// paths: a subject that names nobody, an empty action, a canceled
// context, and a ledger built with no clock.
func TestSameTurnAuthorizeRefusesUnusableInput(t *testing.T) {
	ledger, _ := newLedger(t)
	ctx := context.Background()
	if _, err := ledger.Authorize(ctx, Subject{}, "rm -rf /srv"); err == nil {
		t.Error("a subject that names nobody authorized an action")
	}
	if _, err := ledger.Authorize(ctx, testSubject(), ""); err == nil {
		t.Error("an empty action was authorized")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ledger.Authorize(canceled, testSubject(), "rm -rf /srv"); err == nil {
		t.Error("an authorization was recorded after cancellation")
	}
	if _, err := ledger.Authorized(canceled, testSubject(), "rm -rf /srv"); err == nil {
		t.Error("the ledger answered after cancellation")
	}
	if _, err := NewSameTurnLedger(nil); err == nil {
		t.Error("a ledger was built with no clock")
	}
}

// TestSameTurnIsRaceFree drives Authorize and Authorized concurrently and
// asserts the invariant that matters: exactly one caller can spend a given
// authorization, however many ask at once.
func TestSameTurnIsRaceFree(t *testing.T) {
	ctx := context.Background()
	ledger, _ := newLedger(t)
	subject := testSubject()
	const readers = 16

	for round := 0; round < 8; round++ {
		if _, err := ledger.Authorize(ctx, subject, "rm -rf /srv"); err != nil {
			t.Fatalf("Authorize: %v", err)
		}
		var mu sync.Mutex
		wins := 0
		var wg sync.WaitGroup
		for i := 0; i < readers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ok, err := ledger.Authorized(ctx, subject, "rm -rf /srv")
				if err != nil {
					return
				}
				if ok {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if wins != 1 {
			t.Fatalf("round %d: %d callers spent one authorization, want 1", round, wins)
		}
	}
}

// TestNoSameTurnAuthIsACompleteBehaviour asserts the default an Engine is
// built with never authorizes anything (Art.1).
func TestNoSameTurnAuthIsACompleteBehaviour(t *testing.T) {
	ctx := context.Background()
	a := NoSameTurnAuth()
	for _, action := range []string{"rm -rf /srv", "", "vault.get"} {
		ok, err := a.Authorized(ctx, testSubject(), action)
		if ok || err != nil {
			t.Errorf("Authorized(%q) = %v, %v; want false, nil", action, ok, err)
		}
	}
}
