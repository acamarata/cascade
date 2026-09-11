// Purpose: AddProvider (P1-E10-W3-S21-T2 follow-up, the provider add/list
//   split fix): a strict-insert primitive on Registry that REFUSES a
//   duplicate name rather than upserting over it, complementing
//   UpsertProvider's intentional idempotent-update semantics (the CLI's
//   `cascade provider add` re-add path). Callers that need "create, never
//   silently overwrite" (a future scripted-provisioning surface, and this
//   ticket's own durability/refusal test suite) use this instead of
//   UpsertProvider.
// Inputs: an open, migrated *sql.DB (via the embedding Registry) and one
//   ProviderRecord.
// Outputs: nil on a successful first insert; a typed cascade error
//   otherwise -- KindConflict for an existing name, whatever Validate
//   returns for a malformed record, KindUnavailable for any other SQL
//   failure.
// Constraints: rec.Validate() runs before any DB write, matching
//   UpsertProvider (Art.1: fail closed on unparseable/unknown input). The
//   INSERT carries no ON CONFLICT clause, so a duplicate name never
//   reaches a partial write -- SQLite's own UNIQUE(name) constraint (the
//   primary key, migration.go) is the single source of truth for
//   "already exists," detected via the driver's own result code
//   (mcsqlite.Error.Code, mirroring internal/storage/sqlhelpers.go's and
//   internal/conversation/store.go's classifyProbeError/
//   translateAppendError pattern) rather than a string match on the error
//   text.
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2 follow-up).

package registry

import (
	"context"
	"errors"

	mcsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrProviderExists is returned by AddProvider when rec.Name already names
// a stored record.
var ErrProviderExists = cascade.New(cascade.KindConflict, "registry: provider already exists")

// AddProvider inserts rec as a NEW provider record. Unlike UpsertProvider,
// a duplicate rec.Name is refused -- it is never overwritten, updated, or
// partially applied. The insert is a single statement, so a refusal (a
// constraint violation or any other SQL failure) leaves the table exactly
// as it was before the call: there is no window in which a half-written
// row is visible to a concurrent reader.
func (r *Registry) AddProvider(ctx context.Context, rec ProviderRecord) error {
	if err := rec.Validate(); err != nil {
		return err
	}
	now := r.clock.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now

	knownModels, caps, cost, err := encodeProviderJSON(rec)
	if err != nil {
		return err
	}

	_, err = r.db.ExecContext(ctx, `
		INSERT INTO `+tableProviderRecords+`
			(name, driver_kind, base_url, auth_type, auth_ref, known_models, account_kind, tier,
			 capabilities, capabilities_probed_at, cost, health_status, health_checked_at,
			 demotion_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Name, string(rec.Driver), rec.BaseURL, string(rec.Auth), string(rec.AuthRef), string(knownModels),
		string(rec.AccountKind), string(rec.Tier), string(caps), millisPtr(rec.CapabilitiesProbedAt), cost,
		string(rec.HealthStatus), millisPtr(rec.HealthCheckedAt), rec.DemotionCount,
		rec.CreatedAt.UnixMilli(), rec.UpdatedAt.UnixMilli())
	if err != nil {
		if isConstraintViolation(err) {
			return cascade.Wrapf(cascade.KindConflict, ErrProviderExists, "registry: provider %q", rec.Name)
		}
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry: add provider %q", rec.Name)
	}
	return nil
}

// isConstraintViolation reports whether err is a real SQLite constraint
// failure (UNIQUE, NOT NULL, CHECK, ...), detected via the driver's own
// result code rather than a string match on the error text -- the same
// discipline internal/conversation/store.go's translateAppendError and
// internal/storage/sqlhelpers.go's classifyProbeError already use for this
// exact driver.
func isConstraintViolation(err error) bool {
	var sqliteErr *mcsqlite.Error
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}
