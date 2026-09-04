// Package policy (grant_store.go): Purpose: StoreGrants' write path — the
//
//	B-layer-backed GrantStore over provider.Store, plus the key builder
//	both the write and the read path address rows through. Split from
//	grant.go as a sibling file per R-14.117 (Art.10.3's 300-line cap);
//	grant.go holds the contract this file implements, and grant_check.go
//	holds the read path.
//
// Inputs: a provider.Store (the `policy` domain namespace), a
//
//	CapabilityRegistry, and a Clock. All three are required.
//
// Outputs: persisted grant rows, or a typed refusal.
// Constraints: writes go through the B-layer abstraction only, never
//
//	direct SQLite. A grant naming an unregistered capability is refused at
//	write as well as denied at read. An already-expired grant is refused
//	rather than stored as a row that can never be honoured.
//
// SPORT: internal/policy StoreGrants/ADDED (P1-E09-W2-S17-T1).
package policy

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// StoreGrants is the B-layer-backed GrantStore: a real implementation
// (Art.1) over provider.Store, writing into the `policy` domain namespace.
type StoreGrants struct {
	store    provider.Store
	registry CapabilityRegistry
	clock    Clock
}

var _ GrantStore = (*StoreGrants)(nil)

// NewStoreGrants builds a GrantStore over store, resolving capability
// names through registry and expiry against clock. All three are required:
// a store with no registry could not reject an unknown capability, and one
// with no clock could not expire a grant, and a store that cannot do
// either has no business answering a permission question.
func NewStoreGrants(store provider.Store, registry CapabilityRegistry, clock Clock) (*StoreGrants, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: grant store requires a store")
	}
	if registry == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: grant store requires a capability registry")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "policy: grant store requires a clock")
	}
	return &StoreGrants{store: store, registry: registry, clock: clock}, nil
}

// namespace is the `policy` storage domain, read from storage's own
// constant so the two can never drift.
func (s *StoreGrants) namespace() string { return string(storage.DomainPolicy) }

// Grant implements GrantStore.
func (s *StoreGrants) Grant(ctx context.Context, g Grant) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if _, err := s.registry.Lookup(ctx, g.Capability); err != nil {
		return err
	}
	if !g.ExpiresAt.IsZero() && !s.clock.Now().Before(g.ExpiresAt) {
		return newGrantDenied("grant for %s on %q expired at %s before it was written",
			g.Subject, g.Capability, g.ExpiresAt.UTC().Format(time.RFC3339))
	}
	encoded, err := json.Marshal(g)
	if err != nil {
		return cascade.Wrapf(cascade.KindInternal, err,
			"policy: encoding grant for %s on %q", g.Subject, g.Capability)
	}
	return s.store.Put(ctx, s.namespace(), g.key(), encoded)
}

// Revoke implements GrantStore.
func (s *StoreGrants) Revoke(ctx context.Context, subject Subject, capability string) error {
	key, err := grantKey(subject, capability)
	if err != nil {
		return err
	}
	if _, err := s.store.Get(ctx, s.namespace(), key); err != nil {
		if errors.Is(err, cascade.ErrNotFound) {
			return newGrantDenied("%s holds no grant on %q to revoke",
				subject, sanitize(capability))
		}
		return err
	}
	return s.store.Delete(ctx, s.namespace(), key)
}

// grantKey validates the components and builds the storage key, so Revoke
// and Check cannot address a row a Grant could never have written.
func grantKey(subject Subject, capability string) (string, error) {
	if err := subject.Validate(); err != nil {
		return "", err
	}
	if err := validateCapabilityName(capability); err != nil {
		return "", err
	}
	return Grant{Subject: subject, Capability: capability}.key(), nil
}
