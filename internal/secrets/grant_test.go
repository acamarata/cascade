package secrets

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the standing grant's contract — the eight binding
//   properties R-14.243 rules, each asserted rather than described.
// Constraints: the clock is injected, so no expiry test ever sleeps.
// SPORT: internal/secrets grant tests (ADD) — P1-E10-W4-S87-T1.

// grantClock is the injected time source for these tests. detector_test.go
// already owns the package's fixedClock for a different shape.
type grantClock struct{ now time.Time }

func (c *grantClock) Now() time.Time { return c.now }

// newGrants builds a register over a temp-dir file store.
func newGrants(t *testing.T) (*Grants, *grantClock, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := NewFileGrantStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	clock := &grantClock{now: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	grants, err := NewGrants(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	return grants, clock, dir
}

// TestAGrantOpensOneKeyAndOneVerb is properties 2 and 3: scope.
func TestAGrantOpensOneKeyAndOneVerb(t *testing.T) {
	ctx := context.Background()
	grants, _, _ := newGrants(t)
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_A_KEY", VerbGet); !ok {
		t.Error("the granted key is not readable")
	}
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_B_KEY", VerbGet); ok {
		t.Error("a grant for one key opened another key")
	}
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_A_KEY", VerbRotate); ok {
		t.Error("a vault.get grant authorised vault.rotate")
	}
}

// TestAGrantExpiresWithoutAnyAction is property 4.
func TestAGrantExpiresWithoutAnyAction(t *testing.T) {
	ctx := context.Background()
	grants, clock, _ := newGrants(t)
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	clock.now = clock.now.Add(59 * time.Minute)
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_A_KEY", VerbGet); !ok {
		t.Fatal("the grant died before its expiry")
	}
	clock.now = clock.now.Add(2 * time.Minute)
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_A_KEY", VerbGet); ok {
		t.Error("an expired grant still authorises a read")
	}
}

// TestAnInfiniteGrantIsNotOfferable is the other half of property 4: the
// ceiling is enforced, not advisory.
func TestAnInfiniteGrantIsNotOfferable(t *testing.T) {
	ctx := context.Background()
	grants, clock, _ := newGrants(t)
	grant, err := grants.Issue(ctx, "PROVIDER_A_KEY", 3650*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.now.Add(MaxGrantTTL); !grant.ExpiresAt.Equal(want) {
		t.Errorf("expiry = %v, want it clamped to %v", grant.ExpiresAt, want)
	}
}

// TestRevocationTakesEffectOnTheNextRead is property 5, and the property
// that separates a grant from an exemption.
func TestRevocationTakesEffectOnTheNextRead(t *testing.T) {
	ctx := context.Background()
	grants, _, _ := newGrants(t)
	grant, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := grants.Revoke(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := grants.Lookup(ctx, "PROVIDER_A_KEY", VerbGet); ok {
		t.Error("a revoked grant still authorises a read")
	}
	if err := grants.Revoke(ctx, "nosuchgrant"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("revoking an unknown id: err = %v, want KindNotFound", err)
	}
}

// TestReissuingReplacesRatherThanStacks holds the hazard that would make a
// revoke ineffective: a second Issue leaving the first alive behind it.
func TestReissuingReplacesRatherThanStacks(t *testing.T) {
	ctx := context.Background()
	grants, _, _ := newGrants(t)
	for i := 0; i < 3; i++ {
		if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	all, err := grants.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, g := range all {
		if !g.Revoked {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("%d live grants for one key, want exactly one", live)
	}
}

// TestTheRegisterCarriesNoSecretValue is the standing rule.
func TestTheRegisterCarriesNoSecretValue(t *testing.T) {
	ctx := context.Background()
	grants, _, dir := newGrants(t)
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, grantsFileName))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, grantsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("register mode = %v, want 0600", perm)
	}
	// The register names the key; it must never be able to carry a value,
	// which is structural — Grant has no value field. Asserted on the bytes
	// so a future field addition trips here.
	for _, field := range []string{"value", "secret", "token"} {
		if containsField(string(raw), field) {
			t.Errorf("the register carries a %q field", field)
		}
	}
}

// containsField reports a JSON key of that name.
func containsField(doc, name string) bool {
	return len(doc) > 0 && len(name) > 0 &&
		indexOf(doc, `"`+name+`"`) >= 0
}

// indexOf is a small substring search, kept local.
func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// TestACorruptRegisterIsNotReadAsEmpty holds the fail-closed rule that
// matters most: reading a damaged register as "no grants" would silently
// revoke everything the operator issued.
func TestACorruptRegisterIsNotReadAsEmpty(t *testing.T) {
	ctx := context.Background()
	grants, _, dir := newGrants(t)
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, grantsFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := grants.Lookup(ctx, "PROVIDER_A_KEY", VerbGet); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("err = %v, want KindIntegrity rather than a silent empty register", err)
	}
}

// TestAMissingRegisterIsAnEmptyOne is the other side: a daemon that has
// never been granted anything must still start.
func TestAMissingRegisterIsAnEmptyOne(t *testing.T) {
	ctx := context.Background()
	grants, _, _ := newGrants(t)
	got, err := grants.List(ctx)
	if err != nil {
		t.Fatalf("listing an unwritten register: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %v, want an empty register", got)
	}
}
