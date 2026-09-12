// Purpose: the five typed TableDef declarations for account, credential,
//
//	quota_domain, runtime_profile and lane, each named with the `config`
//	storage domain's TablePrefix (R-21.22 -- the domain list is closed,
//	so these tables join the EXISTING config domain rather than a new
//	one).
//
// Inputs: none. Outputs: []migrate.TableDef consumed by migration.go.
// Constraints: column order here must match every scanner in store.go.
// SPORT: fleet/topology/schema/ADD (P1-E40-W9-S77-T1).

package topology

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface{ Scan(dest ...any) error }

// execer is satisfied by both *sql.DB and *sql.Tx, so Store's methods work
// unchanged whether called directly or through withTx's transaction scope.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store is the fleet-topology CRUD surface over the five config-domain
// tables (store.go/reconcile.go). The zero value is not usable; construct
// with NewStore.
type Store struct {
	db  execer
	rdb *sql.DB // set only by NewStore; withTx needs it to BeginTx
}

// NewStore returns a Store persisting through db (already migrated via
// ApplyMigrationSchema).
func NewStore(db *sql.DB) *Store { return &Store{db: db, rdb: db} }

// withTx runs fn against a Store scoped to one transaction, committing on
// nil and rolling back otherwise.
func (s *Store) withTx(ctx context.Context, fn func(*Store) error) error {
	if s.rdb == nil {
		return cascade.New(cascade.KindInternal, "topology: withTx requires a Store built with NewStore")
	}
	tx, err := s.rdb.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "topology: begin transaction")
	}
	if err := fn(&Store{db: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "topology: commit transaction")
	}
	return nil
}

// snapshot reads every row of every table into a Snapshot.
func (s *Store) snapshot(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	var err error
	if snap.Accounts, err = s.ListAccounts(ctx); err != nil {
		return Snapshot{}, err
	}
	if snap.Credentials, err = s.ListCredentials(ctx); err != nil {
		return Snapshot{}, err
	}
	if snap.QuotaDomains, err = s.ListQuotaDomains(ctx); err != nil {
		return Snapshot{}, err
	}
	if snap.RuntimeProfiles, err = s.ListRuntimeProfiles(ctx); err != nil {
		return Snapshot{}, err
	}
	if snap.Lanes, err = s.ListLanes(ctx, true); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// listRows runs query and scans every result row with scan.
func listRows[T any](ctx context.Context, db execer, query string, scan func(rowScanner) (T, error)) ([]T, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: list query")
	}
	defer func() { _ = rows.Close() }()
	out := make([]T, 0)
	for rows.Next() {
		rec, serr := scan(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// getRow runs query with args and scans the single result row; returns
// notFound (unwrapped) when the query matches no row.
func getRow[T any](ctx context.Context, db execer, query string, args []any, scan func(rowScanner) (T, error), notFound error) (T, error) {
	rec, err := scan(db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		var zero T
		return zero, notFound
	}
	return rec, err
}

// wrapScan wraps a non-sql.ErrNoRows scan failure; sql.ErrNoRows passes
// through unwrapped so getRow's caller-supplied notFound can replace it.
func wrapScan(err error, entity string) error {
	if err == sql.ErrNoRows {
		return err
	}
	return cascade.Wrapf(cascade.KindUnavailable, err, "topology: scan %s", entity)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// unixOrNil converts t to a nullable millisecond column value.
func unixOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

// nullUnixToTime converts a nullable millisecond column back to time.Time.
func nullUnixToTime(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64)
}

// Table names, prefixed with the `config` domain's TablePrefix
// (internal/storage/domains.go's DomainConfig entry) -- the same
// "<domain>_<table>" convention internal/repo's context_repo_inventory
// and internal/runtime's config-domain tables already use.
const (
	tableAccount        = "config_account"
	tableCredential     = "config_credential"
	tableQuotaDomain    = "config_quota_domain"
	tableRuntimeProfile = "config_runtime_profile"
	tableLane           = "config_lane"
)

// accountTable is the config_account TableDef.
func accountTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableAccount,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "provider", Type: migrate.TypeText, NotNull: true},
			{Name: "billing_kind", Type: migrate.TypeText, NotNull: true},
			{Name: "billing_user_reported_monthly_micros", Type: migrate.TypeInteger, NotNull: true},
			{Name: "role", Type: migrate.TypeText, NotNull: true},
		},
	}
}

// quotaDomainTable is the config_quota_domain TableDef.
func quotaDomainTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableQuotaDomain,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "account_ref", Type: migrate.TypeText, NotNull: true},
			{Name: "kind", Type: migrate.TypeText, NotNull: true},
			{Name: "billing_tier", Type: migrate.TypeText, NotNull: true},
			{Name: "quarantined", Type: migrate.TypeInteger, NotNull: true},
		},
		ForeignKeys: []migrate.ForeignKeyDef{
			{Column: "account_ref", RefTable: tableAccount, RefColumn: "id"},
		},
	}
}

// runtimeProfileTable is the config_runtime_profile TableDef.
func runtimeProfileTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableRuntimeProfile,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "runtime", Type: migrate.TypeText, NotNull: true},
			{Name: "config_home", Type: migrate.TypeText, NotNull: true},
			{Name: "persistent", Type: migrate.TypeInteger, NotNull: true},
			{Name: "endpoint_host", Type: migrate.TypeText, NotNull: true},
			{Name: "endpoint_port", Type: migrate.TypeInteger, NotNull: true},
			{Name: "env_var", Type: migrate.TypeText, NotNull: true},
		},
	}
}

// credentialTable is the config_credential TableDef. secret_ref holds a
// vault-key NAME only (R-16.44, R-21.22) -- never a credential value.
func credentialTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableCredential,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "account_ref", Type: migrate.TypeText, NotNull: true},
			{Name: "quota_domain_ref", Type: migrate.TypeText, NotNull: true},
			{Name: "secret_ref", Type: migrate.TypeText, NotNull: true},
			{Name: "runtime_profile_ref", Type: migrate.TypeText, NotNull: true},
			{Name: "health", Type: migrate.TypeText, NotNull: true},
			{Name: "quarantine_reason", Type: migrate.TypeText, NotNull: true},
			{Name: "quarantined_until", Type: migrate.TypeInteger},
		},
		ForeignKeys: []migrate.ForeignKeyDef{
			{Column: "account_ref", RefTable: tableAccount, RefColumn: "id"},
			{Column: "quota_domain_ref", RefTable: tableQuotaDomain, RefColumn: "id"},
			{Column: "runtime_profile_ref", RefTable: tableRuntimeProfile, RefColumn: "id"},
		},
	}
}

// laneTable is the config_lane TableDef. model_identity and
// offering_snapshot are flattened/JSON-encoded columns (R-21.72, R-21.101);
// offering_snapshot_version is denormalized alongside the JSON blob for
// query convenience.
func laneTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableLane,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "runtime_profile_ref", Type: migrate.TypeText, NotNull: true},
			{Name: "quota_domain_ref", Type: migrate.TypeText, NotNull: true},
			{Name: "credential_ref", Type: migrate.TypeText},
			{Name: "model_id", Type: migrate.TypeText, NotNull: true},
			{Name: "effort", Type: migrate.TypeText, NotNull: true},
			{Name: "roles", Type: migrate.TypeText, NotNull: true},
			{Name: "lane_class", Type: migrate.TypeText, NotNull: true},
			{Name: "interaction_class", Type: migrate.TypeText, NotNull: true},
			{Name: "base_shadow_price", Type: migrate.TypeReal, NotNull: true},
			{Name: "health", Type: migrate.TypeText, NotNull: true},
			{Name: "model_identity_canonical_id", Type: migrate.TypeText, NotNull: true},
			{Name: "model_identity_family", Type: migrate.TypeText, NotNull: true},
			{Name: "offering_snapshot", Type: migrate.TypeText, NotNull: true},
			{Name: "offering_snapshot_version", Type: migrate.TypeInteger, NotNull: true},
			{Name: "discovered_at", Type: migrate.TypeInteger, NotNull: true},
			{Name: "retired_at", Type: migrate.TypeInteger},
		},
		ForeignKeys: []migrate.ForeignKeyDef{
			{Column: "runtime_profile_ref", RefTable: tableRuntimeProfile, RefColumn: "id"},
			{Column: "quota_domain_ref", RefTable: tableQuotaDomain, RefColumn: "id"},
			{Column: "credential_ref", RefTable: tableCredential, RefColumn: "id"},
		},
	}
}
