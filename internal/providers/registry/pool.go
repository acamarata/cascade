// Purpose: key-pool dispatch -- ListPool (deterministic member listing) and
//
//	AdvancePoolIndex (the atomic round-robin selector). Rotation is
//	REGISTRY-side per the contract: drivers are pool-stateless, and this
//	file is the one place pool_index advances.
//
// Inputs: an open *sql.DB already migrated via ApplyMigrationSchema.
// Outputs: []LaneRecord or a selected provider_name, or a pkg/cascade
//
//	taxonomy error.
//
// Constraints: AdvancePoolIndex NEVER promotes a member whose State is not
//
//	LaneStateAvailable -- an exhausted, constrained, auth-required, or
//	unknown member is skipped. A pool with no available member returns
//	ErrPoolExhausted (a typed, distinguishable refusal), never a nil pick
//	or an empty string. The whole read-modify-write runs inside one *sql.Tx
//	so concurrent callers never observe (or double-advance) the same
//	least-recently-used member.
//
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2).

package registry

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// rowScanner is satisfied by both *sql.Row and *sql.Rows; shared by
// registry.go's and lanes.go's scan helpers.
type rowScanner interface {
	Scan(dest ...any) error
}

// millisToTime converts a required (NOT NULL) unix-millis column back to
// time.Time, in UTC so a round-trip is byte-for-byte comparable regardless
// of the process's local timezone.
func millisToTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

// nullMillisToTime converts an optional unix-millis column: NULL becomes
// the zero time.Time, matching the field's own "unset" meaning.
func nullMillisToTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return millisToTime(v.Int64)
}

// millisPtr returns a *int64 unix-millis value for a non-zero t, or nil
// (SQL NULL) for the zero time.Time.
func millisPtr(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}

// credentialShapedPrefixes is the closed deny-list isCredentialShaped
// checks a proposed AuthRef against: recognizable raw-credential prefixes
// from the vendors this registry's DriverKind set covers. Not exhaustive
// by design -- this is defense in depth against an obvious mistake, not a
// secret scanner; the vault is the only real custodian of credential
// values.
var credentialShapedPrefixes = []string{
	"sk-", "sk-ant-", "AKIA", "ghp_", "gho_", "xox", "ya29.", "eyJ", "Bearer ",
}

// isCredentialShaped reports whether s looks like a raw credential value
// rather than a vault-key name: it starts with a recognizable vendor-key
// prefix, or it contains whitespace (a vault-key name never does). Used
// by ProviderRecord.Validate (registry.go) as domain-validation-layer
// enforcement that AuthRef holds a name, never a value.
func isCredentialShaped(s string) bool {
	for _, prefix := range credentialShapedPrefixes {
		if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
			return true
		}
	}
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			return true
		}
	}
	return false
}

// ListPool returns every lane whose PoolMembership equals pool, sorted by
// provider_name for deterministic dispatch order.
func (r *Registry) ListPool(ctx context.Context, pool string) ([]LaneRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		laneSelectColumns+` FROM `+tableProviderLanes+` WHERE pool_membership = ? ORDER BY provider_name`, pool)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "registry: list pool %q", pool)
	}
	defer func() { _ = rows.Close() }()
	out := make([]LaneRecord, 0)
	for rows.Next() {
		rec, serr := scanLaneRow(rows)
		if serr != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, serr, "registry: scan pool lane row")
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "registry: iterate pool")
	}
	return out, nil
}

// AdvancePoolIndex atomically selects the least-recently-used AVAILABLE
// member of pool (lowest pool_index, ties broken by provider_name for
// determinism), bumps its pool_index above every other member so the next
// call picks a different one, and returns its provider_name. A pool with
// zero eligible (LaneStateAvailable) members returns ErrPoolExhausted.
func (r *Registry) AdvancePoolIndex(ctx context.Context, pool string) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "registry: advance pool index: begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	var laneName, providerName string
	row := tx.QueryRowContext(ctx, `
		SELECT lane_name, provider_name FROM `+tableProviderLanes+`
		WHERE pool_membership = ? AND state = ?
		ORDER BY pool_index ASC, provider_name ASC LIMIT 1`,
		pool, string(LaneStateAvailable))
	if err := row.Scan(&laneName, &providerName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", cascade.Wrapf(cascade.KindQuotaExhausted, ErrPoolExhausted, "pool %q", pool)
		}
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "registry: advance pool index: select for %q", pool)
	}

	var maxIndex int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(pool_index), 0) FROM `+tableProviderLanes+` WHERE pool_membership = ?`, pool).
		Scan(&maxIndex); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "registry: advance pool index: read max for %q", pool)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+tableProviderLanes+` SET pool_index = ? WHERE lane_name = ?`, maxIndex+1, laneName); err != nil {
		return "", cascade.Wrapf(cascade.KindUnavailable, err, "registry: advance pool index: update %q", laneName)
	}
	if err := tx.Commit(); err != nil {
		return "", cascade.Wrap(cascade.KindUnavailable, err, "registry: advance pool index: commit")
	}
	return providerName, nil
}
