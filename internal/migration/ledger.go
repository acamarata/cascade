// Package migration (ledger.go): LedgerStore, the durable per-domain
// migration ledger `cascade migrate v1` reads to skip an already-done
// domain and writes to record each domain's outcome.
//
// Inputs: a provider.Store (writes land in the `audit` domain, per
// schema.go) and a Clock.
// Outputs: a LedgerRow per domain, or a typed refusal.
// Constraints: WriteDomain is an unconditional Put, not a conditional
// create — unlike internal/policy/ledger.go's single-use approval nonce,
// a migration ledger row must be overwritable so a domain that errored on
// one run can move to StatusDone on the next.
//
// SPORT: internal.migration.LedgerStore/ADD (P1-E26-W10-S53-T4).
package migration

import (
	"context"
	"time"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Clock abstracts time.Now so LedgerStore never reads the wall clock
// directly (Art.7.3). Declared locally, duck-typed, matching
// internal/policy/grant.go's and internal/storage/domains.go's own local
// Clock interfaces — any of their concrete implementations, including
// internal/runtime.NewSystemClock(), already satisfies this.
type Clock interface {
	// Now returns the current instant.
	Now() time.Time
}

// LedgerStore is the durable migration ledger over the `audit` domain kv
// namespace (schema.go).
type LedgerStore struct {
	store provider.Store
	clock Clock
}

// NewLedgerStore builds a LedgerStore over store, stamping every write
// with clock. Both are required: a ledger with no store could not
// remember an outcome, and one with no clock could not say when.
func NewLedgerStore(store provider.Store, clock Clock) (*LedgerStore, error) {
	if store == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "migration: ledger requires a store")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "migration: ledger requires a clock")
	}
	return &LedgerStore{store: store, clock: clock}, nil
}

// ReadDomain returns domain's ledger row and true, or a zero row and
// false if no row has been written yet. A store failure other than "not
// found" is returned unchanged, so a caller can never mistake an
// unreachable store for "never migrated".
func (l *LedgerStore) ReadDomain(ctx context.Context, domain migrationv1.Domain) (LedgerRow, bool, error) {
	if l == nil {
		return LedgerRow{}, false, cascade.New(cascade.KindInvalidInput, "migration: nil ledger")
	}
	data, err := l.store.Get(ctx, LedgerNamespace, ledgerKey(domain))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return LedgerRow{}, false, nil
		}
		return LedgerRow{}, false, err
	}
	row, err := decodeLedgerRow(data)
	if err != nil {
		return LedgerRow{}, false, err
	}
	return row, true, nil
}

// WriteDomain records domain's outcome, stamping ImportedAtUnixNano from
// the injected clock regardless of what row.ImportedAtUnixNano already
// holds — the timestamp is this store's fact, never the caller's, exactly
// as internal/policy/ledger.go's Consume stamps ConsumedAtUnixNano itself.
func (l *LedgerStore) WriteDomain(ctx context.Context, row LedgerRow) error {
	if l == nil {
		return cascade.New(cascade.KindInvalidInput, "migration: nil ledger")
	}
	row.ImportedAtUnixNano = l.clock.Now().UTC().UnixNano()
	if err := row.Validate(); err != nil {
		return err
	}
	encoded, err := encodeLedgerRow(row)
	if err != nil {
		return err
	}
	return l.store.Put(ctx, LedgerNamespace, ledgerKey(row.Domain), encoded)
}
