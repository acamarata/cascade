// Purpose: scoped standing grants — the ONLY way a headless process reads
//
//	a vault value without a human at the terminal (R-14.243).
//
// WHY THIS EXISTS. vault.get is an elevated verb: the ElevationGate proves
//
//	a human was present. A daemon has no human to prove. Until this landed,
//	the daemon's provider resolver was given a nil CredentialSource, so
//	every key-authenticated provider failed closed and `cascade run` could
//	not reach any model — the product's core function.
//
// WHAT WAS REJECTED. An "unattended-daemon exemption" in the elevation
//
//	gate. That is authorization by self-assertion: it opens EVERY vault
//	secret to anything that can reach the daemon, with no expiry, no
//	per-key scope and no trail, and it gives the repo two answers to "was a
//	human present". A grant is the opposite shape: ONE key, ONE verb, a
//	bounded life, enumerable, revocable, and audited on every use.
//
// Inputs: an injected Clock (never time.Now) and a Store.
// Outputs: Issue/List/Revoke/Lookup over Grant records.
// Constraints: a grant holds NO credential value — only the REFERENCE to
//
//	one. Expiry and revocation are evaluated on every Lookup, so a revoked
//	or expired grant is dead on the very next read, not at the next
//	restart.
//
// SPORT: internal/secrets grant/ADD — P1-E10-W4-S87-T1 (R-14.243).

package secrets

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// MaxGrantTTL is the ceiling on a grant's life. An infinite grant is not
// offerable: renewal is another human-present act, which is the property
// that keeps this from being the exemption it replaces.
const MaxGrantTTL = 30 * 24 * time.Hour

// DefaultGrantTTL is the life a grant takes when the operator names none.
const DefaultGrantTTL = 7 * 24 * time.Hour

// Grant authorises ONE verb on ONE vault key for a bounded time.
type Grant struct {
	// ID identifies the grant for listing and revocation. Never derived
	// from the secret.
	ID string `json:"id"`
	// KeyRef is the single vault name this grant opens. A grant for one
	// provider's key opens no other key, and no non-provider secret.
	KeyRef string `json:"key_ref"`
	// Verb is the single operation authorised. Only VerbGet is issuable:
	// a grant never authorises rotate, set or delete.
	Verb string `json:"verb"`
	// IssuedAt is when a human proved presence for this grant.
	IssuedAt time.Time `json:"issued_at"`
	// ExpiresAt is when it stops working, with no action required.
	ExpiresAt time.Time `json:"expires_at"`
	// Revoked marks a grant the operator withdrew. Kept rather than
	// deleted so `vault grants` can show that it existed and ended.
	Revoked bool `json:"revoked,omitempty"`
}

// Live reports whether g authorises verb on keyRef at now.
//
// Every condition is re-checked here rather than at issue time, which is
// what makes revocation take effect on the next read instead of the next
// daemon restart.
func (g Grant) Live(keyRef, verb string, now time.Time) bool {
	return !g.Revoked &&
		g.KeyRef == keyRef &&
		g.Verb == verb &&
		!now.Before(g.IssuedAt) &&
		now.Before(g.ExpiresAt)
}

// GrantStore persists grants. It is an interface so the daemon and the CLI
// can share one implementation without this package choosing a file layout
// for them.
type GrantStore interface {
	// LoadGrants returns every grant, including revoked and expired ones.
	LoadGrants(ctx context.Context) ([]Grant, error)
	// SaveGrants replaces the stored set.
	SaveGrants(ctx context.Context, grants []Grant) error
}

// Grants is the grant register. Its Clock is this package's existing
// injected time source (quarantine.go): grants never read the wall clock,
// so an expiry test never has to sleep.
type Grants struct {
	store GrantStore
	clock Clock
}

// NewGrants builds a register over store.
func NewGrants(store GrantStore, clock Clock) (*Grants, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: a grant register needs a store")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: a grant register needs a clock")
	}
	return &Grants{store: store, clock: clock}, nil
}

// Issue records a grant for keyRef, for ttl.
//
// Only VerbGet is issuable. ttl is clamped to MaxGrantTTL rather than
// refused, so a mistyped ceiling produces a shorter grant instead of no
// grant at all; a zero or negative ttl takes DefaultGrantTTL.
func (g *Grants) Issue(ctx context.Context, keyRef string, ttl time.Duration) (Grant, error) {
	if err := validateSecretName(keyRef); err != nil {
		return Grant{}, err
	}
	switch {
	case ttl <= 0:
		ttl = DefaultGrantTTL
	case ttl > MaxGrantTTL:
		ttl = MaxGrantTTL
	}
	now := g.clock.Now()
	grant := Grant{
		ID:        grantID(keyRef, now),
		KeyRef:    keyRef,
		Verb:      VerbGet,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
	}
	existing, err := g.store.LoadGrants(ctx)
	if err != nil {
		return Grant{}, err
	}
	// One live grant per key: re-issuing replaces rather than stacking, so
	// a revoke can never leave an older grant alive behind it.
	kept := make([]Grant, 0, len(existing)+1)
	for _, e := range existing {
		if e.KeyRef != keyRef || e.Revoked {
			kept = append(kept, e)
		}
	}
	kept = append(kept, grant)
	if err := g.store.SaveGrants(ctx, kept); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

// List returns every grant, newest first.
func (g *Grants) List(ctx context.Context) ([]Grant, error) {
	return g.store.LoadGrants(ctx)
}

// Revoke marks the grant with id revoked. It takes effect on the next
// Lookup, not at the next restart.
func (g *Grants) Revoke(ctx context.Context, id string) error {
	grants, err := g.store.LoadGrants(ctx)
	if err != nil {
		return err
	}
	found := false
	for i := range grants {
		if grants[i].ID == id {
			grants[i].Revoked = true
			found = true
		}
	}
	if !found {
		return cascade.Newf(cascade.KindNotFound, "secrets: no grant %q", id)
	}
	return g.store.SaveGrants(ctx, grants)
}

// Lookup reports whether a live grant authorises verb on keyRef now.
func (g *Grants) Lookup(ctx context.Context, keyRef, verb string) (Grant, bool, error) {
	grants, err := g.store.LoadGrants(ctx)
	if err != nil {
		return Grant{}, false, err
	}
	now := g.clock.Now()
	for _, candidate := range grants {
		if candidate.Live(keyRef, verb, now) {
			return candidate, true, nil
		}
	}
	return Grant{}, false, nil
}
