// Purpose: AtomicHealthUpdate, a transactional read-modify-write helper
//   internal/providers/health (P1-E10-W3-S20-T3) uses so
//   DemoteProvider/RecoverProbe never lose an update under concurrent
//   callers on the same provider name.
//
// FILE-SCOPE DEVIATION (recorded, not papered over). The S-20.T3 ticket's
// files_scope names "internal/providers/registry/registry.go" as the sole
// change target in this package, but registry.go is already 290 of its
// 300-line cap -- the repo-wide file-size gate (internal/build) leaves no
// room for a ~55-line transactional method there. This package already
// splits by concern across five files (schema.go, registry.go, lanes.go,
// pool.go, migration.go); this file continues that existing convention
// rather than exceeding the cap. Same situation, same resolution, as
// migration.go's own recorded CONTRACT DEVIATION note.
//
// Inputs: an open, migrated *sql.DB (via the Registry the caller already
//   holds) and a transform function.
// Outputs: the updated ProviderRecord, or a pkg/cascade taxonomy error.
// Constraints: the whole read-modify-write runs inside one *sql.Tx,
//   exactly mirroring pool.go's AdvancePoolIndex -- concurrent callers on
//   the same name are serialized by the transaction, never lost. fn
//   returning a non-nil error aborts without writing.
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T3, extending T2's Registry).

package registry

import (
	"context"
	"database/sql"
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// AtomicHealthUpdate reads the current record named name, applies fn, and
// persists the result -- all inside one transaction, so concurrent callers
// on the same name never race a lost update. fn returning a non-nil error
// aborts the transaction and returns that error unchanged; a returned
// record that fails Validate() also aborts. name not found returns
// ErrProviderNotFound.
func (r *Registry) AtomicHealthUpdate(ctx context.Context, name string, fn func(ProviderRecord) (ProviderRecord, error)) (ProviderRecord, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "registry: atomic health update: begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, providerSelectColumns+` FROM `+tableProviderRecords+` WHERE name = ?`, name)
	rec, err := scanProviderRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderRecord{}, cascade.Wrapf(cascade.KindNotFound, ErrProviderNotFound, "provider %q", name)
	}
	if err != nil {
		return ProviderRecord{}, cascade.Wrapf(cascade.KindUnavailable, err, "registry: atomic health update: select %q", name)
	}

	updated, ferr := fn(rec)
	if ferr != nil {
		return ProviderRecord{}, ferr
	}
	updated.Name = rec.Name
	updated.CreatedAt = rec.CreatedAt
	if verr := updated.Validate(); verr != nil {
		return ProviderRecord{}, verr
	}
	updated.UpdatedAt = r.clock.Now()

	knownModels, caps, cost, eerr := encodeProviderJSON(updated)
	if eerr != nil {
		return ProviderRecord{}, eerr
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE `+tableProviderRecords+` SET
			driver_kind=?, base_url=?, auth_type=?, auth_ref=?, known_models=?, account_kind=?, tier=?,
			capabilities=?, capabilities_probed_at=?, cost=?, health_status=?, health_checked_at=?,
			demotion_count=?, updated_at=?
		WHERE name=?`,
		string(updated.Driver), updated.BaseURL, string(updated.Auth), string(updated.AuthRef), string(knownModels),
		string(updated.AccountKind), string(updated.Tier), string(caps), millisPtr(updated.CapabilitiesProbedAt), cost,
		string(updated.HealthStatus), millisPtr(updated.HealthCheckedAt), updated.DemotionCount,
		updated.UpdatedAt.UnixMilli(), updated.Name)
	if err != nil {
		return ProviderRecord{}, cascade.Wrapf(cascade.KindUnavailable, err, "registry: atomic health update: exec %q", name)
	}
	if err := tx.Commit(); err != nil {
		return ProviderRecord{}, cascade.Wrap(cascade.KindUnavailable, err, "registry: atomic health update: commit")
	}
	return updated, nil
}
