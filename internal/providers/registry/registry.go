// Package registry implements the providers registry storage domain
// (P1-E10-W3-S20-T2): the durable provider_records/provider_lanes CRUD
// surface, the key-pool round-robin dispatcher, and the pkg/provider.
// ProviderRegistryReader adapter (Reader, lanes.go). It is the durable
// replacement S-20.T1 intake's MemoryRegistry stands in for; wiring intake
// onto this Registry is out of this ticket's files_scope (see the journal).
//
// Purpose (this file): Registry, the write-capable CRUD surface over the
// provider_records table (migration.go). Every method takes
// context.Context; every timestamp comes from the injected Clock, never
// bare time.Now; every row Validate()s before it reaches SQL.
// SPORT: provider.registry/ADD (P1-E10-W3-S20-T2).
package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Clock abstracts the wall clock (Art.7.3: no bare time.Now). Declared
// locally, structurally identical to internal/runtime.Clock and internal/
// providers/intake.Clock, so any of their concrete types (and internal/
// testkit's) already satisfy it with zero adapter code.
type Clock interface {
	Now() time.Time
}

// Registry is the providers registry's provider_records/provider_lanes
// CRUD surface. The zero value is not usable; construct with NewRegistry.
type Registry struct {
	db    *sql.DB
	clock Clock
}

// NewRegistry returns a Registry persisting through db (already migrated
// via ApplyMigrationSchema) and stamping every write from clk.
func NewRegistry(db *sql.DB, clk Clock) *Registry {
	return &Registry{db: db, clock: clk}
}

// Validate checks rec's closed-vocabulary fields and required identifiers.
// Called by UpsertProvider before any DB write -- this is the domain
// validation layer schema.go's AuthRef doc comment refers to.
func (rec ProviderRecord) Validate() error {
	if rec.Name == "" {
		return cascade.New(cascade.KindInvalidInput, "registry: provider name is required")
	}
	if !rec.Driver.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "registry: invalid driver_kind %q", rec.Driver)
	}
	if !rec.Auth.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "registry: invalid auth_type %q", rec.Auth)
	}
	if rec.AuthRef == "" {
		return cascade.New(cascade.KindInvalidInput, "registry: auth_ref is required")
	}
	if isCredentialShaped(rec.AuthRef.String()) {
		// Domain-validation enforcement (not merely convention): AuthRef
		// must be a vault-key NAME. VaultKeyRef is a distinct type but is
		// still string-backed, so nothing at the type level stops a
		// caller from assigning a raw secret to it by mistake; this check
		// refuses the obviously-wrong shape before any DB write. The
		// error message below names only the field, never rec.AuthRef's
		// own value -- echoing it back would be exactly the leak class
		// S-20.T1 found in an error message.
		return cascade.New(cascade.KindInvalidInput,
			"registry: auth_ref must be a vault-key name, not a credential-shaped value")
	}
	if !rec.AccountKind.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "registry: invalid account_kind %q", rec.AccountKind)
	}
	if !rec.Tier.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "registry: invalid tier %q", rec.Tier)
	}
	if !rec.HealthStatus.Valid() {
		return cascade.Newf(cascade.KindInvalidInput, "registry: invalid health_status %q", rec.HealthStatus)
	}
	return nil
}

// providerSelectColumns is the shared SELECT column list for every
// provider_records read, kept in exactly the order scanProviderRow expects.
const providerSelectColumns = `SELECT name, driver_kind, base_url, auth_type, auth_ref, known_models,
	account_kind, tier, capabilities, capabilities_probed_at, cost, health_status,
	health_checked_at, demotion_count, created_at, updated_at`

// scanProviderRow scans one providerSelectColumns row into a ProviderRecord.
func scanProviderRow(row rowScanner) (ProviderRecord, error) {
	var (
		rec                                     ProviderRecord
		driver, auth, accountKind, tier, health string
		knownModels, caps                       string
		cost                                    sql.NullString
		capsProbedAt, healthCheckedAt           sql.NullInt64
		createdAt, updatedAt                    int64
	)
	err := row.Scan(&rec.Name, &driver, &rec.BaseURL, &auth, &rec.AuthRef, &knownModels,
		&accountKind, &tier, &caps, &capsProbedAt, &cost, &health,
		&healthCheckedAt, &rec.DemotionCount, &createdAt, &updatedAt)
	if err != nil {
		return ProviderRecord{}, err
	}

	rec.Driver = DriverKind(driver)
	rec.Auth = AuthType(auth)
	rec.AccountKind = AccountKind(accountKind)
	rec.Tier = Tier(tier)
	rec.HealthStatus = HealthStatus(health)
	rec.CreatedAt = millisToTime(createdAt)
	rec.UpdatedAt = millisToTime(updatedAt)
	rec.CapabilitiesProbedAt = nullMillisToTime(capsProbedAt)
	rec.HealthCheckedAt = nullMillisToTime(healthCheckedAt)

	if err := json.Unmarshal([]byte(knownModels), &rec.KnownModels); err != nil {
		return ProviderRecord{}, cascade.Wrap(cascade.KindIntegrity, err, "registry: decode known_models")
	}
	if err := json.Unmarshal([]byte(caps), &rec.Capabilities); err != nil {
		return ProviderRecord{}, cascade.Wrap(cascade.KindIntegrity, err, "registry: decode capabilities")
	}
	if cost.Valid {
		parsed, perr := ParseCostRecord([]byte(cost.String))
		if perr != nil {
			return ProviderRecord{}, perr
		}
		rec.Cost = parsed
	}
	return rec, nil
}

// Sentinel errors, each wrapping exactly one frozen pkg/cascade.Kind.
var (
	// ErrProviderNotFound is returned when a provider name is unknown.
	ErrProviderNotFound = cascade.New(cascade.KindNotFound, "registry: provider not found")
	// ErrPoolExhausted is returned by AdvancePoolIndex when a pool has no
	// available member to select.
	ErrPoolExhausted = cascade.New(cascade.KindQuotaExhausted, "registry: pool exhausted (no available member)")
)

// UpsertProvider inserts or updates rec by rec.Name. rec.Validate() runs
// before any DB write -- an invalid closed-vocabulary value never reaches
// SQL.
func (r *Registry) UpsertProvider(ctx context.Context, rec ProviderRecord) error {
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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			driver_kind=excluded.driver_kind, base_url=excluded.base_url, auth_type=excluded.auth_type,
			auth_ref=excluded.auth_ref, known_models=excluded.known_models, account_kind=excluded.account_kind,
			tier=excluded.tier, capabilities=excluded.capabilities,
			capabilities_probed_at=excluded.capabilities_probed_at, cost=excluded.cost,
			health_status=excluded.health_status, health_checked_at=excluded.health_checked_at,
			demotion_count=excluded.demotion_count, updated_at=excluded.updated_at`,
		rec.Name, string(rec.Driver), rec.BaseURL, string(rec.Auth), string(rec.AuthRef), string(knownModels),
		string(rec.AccountKind), string(rec.Tier), string(caps), millisPtr(rec.CapabilitiesProbedAt), cost,
		string(rec.HealthStatus), millisPtr(rec.HealthCheckedAt), rec.DemotionCount,
		rec.CreatedAt.UnixMilli(), rec.UpdatedAt.UnixMilli())
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry: upsert provider %q", rec.Name)
	}
	return nil
}

// encodeProviderJSON marshals rec's JSON-blob columns.
func encodeProviderJSON(rec ProviderRecord) (knownModels, caps, cost []byte, err error) {
	knownModels, err = json.Marshal(nonNilStrings(rec.KnownModels))
	if err != nil {
		return nil, nil, nil, cascade.Wrap(cascade.KindInternal, err, "registry: encode known_models")
	}
	caps, err = json.Marshal(rec.Capabilities)
	if err != nil {
		return nil, nil, nil, cascade.Wrap(cascade.KindInternal, err, "registry: encode capabilities")
	}
	cost, err = EncodeCostRecord(rec.Cost)
	if err != nil {
		return nil, nil, nil, err
	}
	return knownModels, caps, cost, nil
}

// GetProvider returns the record named name, or ErrProviderNotFound.
func (r *Registry) GetProvider(ctx context.Context, name string) (ProviderRecord, error) {
	row := r.db.QueryRowContext(ctx, providerSelectColumns+` FROM `+tableProviderRecords+` WHERE name = ?`, name)
	rec, err := scanProviderRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderRecord{}, cascade.Wrapf(cascade.KindNotFound, ErrProviderNotFound, "provider %q", name)
	}
	if err != nil {
		return ProviderRecord{}, cascade.Wrapf(cascade.KindUnavailable, err, "registry: get provider %q", name)
	}
	return rec, nil
}

// ListProviders returns every provider record, ordered by name.
func (r *Registry) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := r.db.QueryContext(ctx, providerSelectColumns+` FROM `+tableProviderRecords+` ORDER BY name`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "registry: list providers")
	}
	defer func() { _ = rows.Close() }()
	out := make([]ProviderRecord, 0)
	for rows.Next() {
		rec, serr := scanProviderRow(rows)
		if serr != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, serr, "registry: scan provider row")
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "registry: iterate providers")
	}
	return out, nil
}

// DeleteProvider removes the record named name and cascade-deletes every
// lane record for that provider, atomically.
func (r *Registry) DeleteProvider(ctx context.Context, name string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "registry: delete provider: begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM `+tableProviderLanes+` WHERE provider_name = ?`, name); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry: delete lanes for provider %q", name)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+tableProviderRecords+` WHERE name = ?`, name); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry: delete provider %q", name)
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "registry: delete provider: commit")
	}
	return nil
}

// GetByModel returns every provider whose KnownModels contains model
// (exact match). A provider with empty KnownModels is excluded -- no
// wildcard expansion in P1.
func (r *Registry) GetByModel(ctx context.Context, model string) ([]ProviderRecord, error) {
	all, err := r.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderRecord, 0)
	for _, rec := range all {
		if containsString(rec.KnownModels, model) {
			out = append(out, rec)
		}
	}
	return out, nil
}

// nonNilStrings returns s unchanged, or an empty (non-nil) slice for a nil
// s, so json.Marshal always writes "[]" rather than "null".
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
