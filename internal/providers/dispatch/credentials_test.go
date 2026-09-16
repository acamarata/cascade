// Purpose: the GrantedCredentials contract — construction refusals and the
//
//	empty-string-on-every-error-path property Resolve exists for.
//
// Constraints: the grant clock is injected, so expiry is never asserted by
//
//	sleeping. Custody is an in-memory test double local to this file.
//
// SPORT: internal/providers/dispatch credentials tests (ADD) — P1-E10-W4-S87-T1.
package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// memCustody is a minimal in-memory secrets.Custody double, local to this
// package because internal/secrets' own memCustody is unexported there.
type memCustody struct {
	entries map[string][]byte
}

func newMemCustody() *memCustody { return &memCustody{entries: map[string][]byte{}} }

func (m *memCustody) Name() string    { return "memory" }
func (m *memCustody) Available() bool { return true }

func (m *memCustody) Set(_ context.Context, name string, value []byte) error {
	m.entries[name] = value
	return nil
}

func (m *memCustody) Get(_ context.Context, name string) ([]byte, error) {
	v, ok := m.entries[name]
	if !ok {
		return nil, cascade.Newf(cascade.KindNotFound, "secrets: %q is not stored", name)
	}
	return v, nil
}

func (m *memCustody) Delete(_ context.Context, name string) error {
	delete(m.entries, name)
	return nil
}

func (m *memCustody) List(_ context.Context) ([]string, error) {
	names := make([]string, 0, len(m.entries))
	for n := range m.entries {
		names = append(names, n)
	}
	return names, nil
}

// credClock is the injected time source, so expiry never needs a sleep.
type credClock struct{ now time.Time }

func (c *credClock) Now() time.Time { return c.now }

// newGrantedCredentials wires a broker (no gate: only GetGranted is used,
// never an elevated verb) and a file-backed grant register over t.TempDir().
func newGrantedCredentials(t *testing.T) (*GrantedCredentials, *secrets.Grants, *credClock, *memCustody) {
	t.Helper()
	custody := newMemCustody()
	broker, err := secrets.NewBroker(custody, nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := secrets.NewFileGrantStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	clock := &credClock{now: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)}
	grants, err := secrets.NewGrants(store, clock)
	if err != nil {
		t.Fatal(err)
	}
	creds, err := NewGrantedCredentials(broker, grants)
	if err != nil {
		t.Fatal(err)
	}
	return creds, grants, clock, custody
}

// TestNewGrantedCredentialsRefusesNilBroker is contract 1 (broker half).
func TestNewGrantedCredentialsRefusesNilBroker(t *testing.T) {
	store, err := secrets.NewFileGrantStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	grants, err := secrets.NewGrants(store, &credClock{now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewGrantedCredentials(nil, grants)
	if err == nil {
		t.Fatal("a nil broker was accepted")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("kind = %v, want KindInvalidInput", err)
	}
}

// TestNewGrantedCredentialsRefusesNilGrants is contract 1 (grant register half).
func TestNewGrantedCredentialsRefusesNilGrants(t *testing.T) {
	broker, err := secrets.NewBroker(newMemCustody(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewGrantedCredentials(broker, nil)
	if err == nil {
		t.Fatal("a nil grant register was accepted")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("kind = %v, want KindInvalidInput", err)
	}
}

// TestResolveReturnsTheStoredValueUnderALiveGrant is contract 2.
func TestResolveReturnsTheStoredValueUnderALiveGrant(t *testing.T) {
	ctx := context.Background()
	creds, grants, _, custody := newGrantedCredentials(t)
	if err := custody.Set(ctx, "PROVIDER_A_KEY", []byte("sk-super-secret-value")); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := creds.Resolve(ctx, "PROVIDER_A_KEY")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sk-super-secret-value" {
		t.Errorf("Resolve = %q, want the stored value", got)
	}
}

// TestResolveRefusesAKeyWithNoGrant is contract 3, no-grant half.
func TestResolveRefusesAKeyWithNoGrant(t *testing.T) {
	ctx := context.Background()
	creds, _, _, custody := newGrantedCredentials(t)
	if err := custody.Set(ctx, "PROVIDER_A_KEY", []byte("sk-super-secret-value")); err != nil {
		t.Fatal(err)
	}
	got, err := creds.Resolve(ctx, "PROVIDER_A_KEY")
	if err == nil {
		t.Fatal("resolving an ungranted key succeeded")
	}
	if got != "" {
		t.Errorf("Resolve on the ungranted-key error path = %q, want empty string", got)
	}
	if strings.Contains(err.Error(), "sk-super-secret-value") {
		t.Errorf("error leaked the secret value: %v", err)
	}
}

// TestResolveRefusesAfterRevocation is contract 4, revocation half.
func TestResolveRefusesAfterRevocation(t *testing.T) {
	ctx := context.Background()
	creds, grants, _, custody := newGrantedCredentials(t)
	if err := custody.Set(ctx, "PROVIDER_A_KEY", []byte("sk-super-secret-value")); err != nil {
		t.Fatal(err)
	}
	grant, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Resolve(ctx, "PROVIDER_A_KEY"); err != nil {
		t.Fatalf("resolve failed before revocation: %v", err)
	}
	if err := grants.Revoke(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	got, err := creds.Resolve(ctx, "PROVIDER_A_KEY")
	if err == nil {
		t.Fatal("resolving a revoked-grant key succeeded")
	}
	if got != "" {
		t.Errorf("Resolve on the revoked-grant error path = %q, want empty string", got)
	}
	if strings.Contains(err.Error(), "sk-super-secret-value") {
		t.Errorf("error leaked the secret value: %v", err)
	}
}

// TestResolveRefusesAfterExpiry is contract 4, expiry half. The clock is
// advanced past ExpiresAt; nothing here sleeps.
func TestResolveRefusesAfterExpiry(t *testing.T) {
	ctx := context.Background()
	creds, grants, clock, custody := newGrantedCredentials(t)
	if err := custody.Set(ctx, "PROVIDER_A_KEY", []byte("sk-super-secret-value")); err != nil {
		t.Fatal(err)
	}
	if _, err := grants.Issue(ctx, "PROVIDER_A_KEY", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := creds.Resolve(ctx, "PROVIDER_A_KEY"); err != nil {
		t.Fatalf("resolve failed before expiry: %v", err)
	}
	clock.now = clock.now.Add(61 * time.Minute)
	got, err := creds.Resolve(ctx, "PROVIDER_A_KEY")
	if err == nil {
		t.Fatal("resolving an expired-grant key succeeded")
	}
	if got != "" {
		t.Errorf("Resolve on the expired-grant error path = %q, want empty string", got)
	}
	if strings.Contains(err.Error(), "sk-super-secret-value") {
		t.Errorf("error leaked the secret value: %v", err)
	}
}
