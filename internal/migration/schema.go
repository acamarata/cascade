// Package migration (schema.go): the v1-to-v2 migration ledger's on-record
// row shape and key layout — which v1 data domains this installation has
// already imported, so a second `cascade migrate v1` run can converge
// instead of re-importing.
//
// Inputs: none (pure declarations and small pure helpers).
// Outputs: LedgerRow, LedgerStatus, and the storage key layout ledger.go
// reads and writes.
// Constraints: no I/O, no clock, no randomness — everything here is a
// deterministic function of its arguments.
//
// CONTRACT DEVIATION (recorded, not papered over). This ticket's task 1
// says the ledger is "a `migration_ledger` table in the `audit` domain
// ... created by the B/S-02.T3 migration builder" — a SQL DDL step
// applied through internal/storage/migrate. The tree has moved past that
// design, as internal/audit/schema.go's own recorded deviation already
// states: "every cascade.db domain persists through pkg/provider.Store,
// whose SQLite driver keeps one physical kv table ... the production
// migration set is deliberately empty" (cmd/cascade/daemon_unix_store.go).
// internal/policy/ledger.go's single-use approval-nonce ledger already
// follows this exact precedent: a kv-keyed row inside the `audit`
// namespace, never its own SQL table. This file does the same — the
// ledger's "schema" is this Go struct plus its key layout over
// provider.Store, matching R-16.65's own text ("there is no `migration`
// storage domain; ... via the B/S-02 Store API"). See the journal for
// both sides quoted in full.
//
// SPORT: internal.migration.schema/ADD (P1-E26-W10-S53-T4).
package migration

import (
	"encoding/json"
	"strings"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
)

// LedgerNamespace is the pkg/provider.Store namespace every ledger row is
// written under: the ratified `audit` domain (R-16.65).
const LedgerNamespace = string(storage.DomainAudit)

// ledgerKeyPrefix scopes ledger rows inside the audit namespace so they
// can never collide with internal/audit's own "rec:"/"idx:"/"head" keys or
// internal/policy's "approval/nonce/" keys.
const ledgerKeyPrefix = "migration/v1/ledger/"

// LedgerStatus is the closed set of ledger row states.
type LedgerStatus string

const (
	// StatusDone records a domain that imported successfully; a re-run
	// against the same v1 directory skips it.
	StatusDone LedgerStatus = "done"
	// StatusError records a domain whose last attempt failed; a re-run
	// retries only this domain, never the ones already StatusDone.
	StatusError LedgerStatus = "error"
)

// LedgerRow is one v1 data domain's migration outcome.
type LedgerRow struct {
	// Domain is one of the four v1.Domain values this ticket orchestrates.
	Domain migrationv1.Domain `json:"domain"`
	// Status is StatusDone or StatusError. WriteDomain refuses any other
	// value (Validate below).
	Status LedgerStatus `json:"status"`
	// ImportedAtUnixNano is the instant this row was written, stamped by
	// LedgerStore.WriteDomain from its injected clock — never set by a
	// caller, exactly as internal/policy/ledger.go's ConsumedAtUnixNano
	// is stamped by Consume rather than trusted from the caller.
	ImportedAtUnixNano int64 `json:"imported_at_unix_nano"`
	// RecordCount is the number of destination mutations this domain's
	// import made (DryRunResult.DeltaCount()), zero for StatusError.
	RecordCount int `json:"record_count"`
	// Error is the last failure's message, empty for StatusDone.
	Error string `json:"error,omitempty"`
}

// Done reports whether this row records a completed import.
func (r LedgerRow) Done() bool { return r.Status == StatusDone }

// Validate refuses a row whose domain or status is not one of the closed
// sets this package knows about — a row that could not be validated must
// never reach the store, or a later reader could not tell a real outcome
// from a corrupted one.
func (r LedgerRow) Validate() error {
	switch r.Domain {
	case migrationv1.DomainMemory, migrationv1.DomainVault,
		migrationv1.DomainAccounts, migrationv1.DomainConfig:
	default:
		return cascade.Newf(cascade.KindInvalidInput,
			"migration: ledger row has unknown domain %q", r.Domain)
	}
	switch r.Status {
	case StatusDone, StatusError:
	default:
		return cascade.Newf(cascade.KindInvalidInput,
			"migration: ledger row has unknown status %q", r.Status)
	}
	return nil
}

// ledgerKey builds the storage key for domain's ledger row.
func ledgerKey(domain migrationv1.Domain) string {
	return ledgerKeyPrefix + strings.TrimSpace(string(domain))
}

// encodeLedgerRow marshals row for storage.
func encodeLedgerRow(row LedgerRow) ([]byte, error) {
	b, err := json.Marshal(row)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "migration: encode ledger row")
	}
	return b, nil
}

// decodeLedgerRow unmarshals row bytes read back from storage. A stored
// row that cannot be decoded is treated as tampering, not as "no row" —
// ReadDomain never widens a corrupted row into "never migrated".
func decodeLedgerRow(data []byte) (LedgerRow, error) {
	var row LedgerRow
	if err := json.Unmarshal(data, &row); err != nil {
		return LedgerRow{}, cascade.Wrap(cascade.KindIntegrity, err, "migration: decode ledger row")
	}
	return row, nil
}
