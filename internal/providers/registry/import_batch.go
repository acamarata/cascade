// Package registry uses this file for transactional v1 provider imports.
// Purpose: create absent provider records as one existing-wins transaction.
// Inputs: fully validated ProviderRecords and a dry-run flag.
// Outputs: created and existing-wins names, with no partial transaction.
// Constraints: records are sorted, duplicates refuse, existing rows are never
// updated, and any insert failure rolls the entire batch back.
// SPORT: provider.registry/CHANGED (P1-E26-W10-S53-T1).
package registry

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ProviderImportResult reports the transactional existing-wins decision.
type ProviderImportResult struct {
	Created  []string
	Existing []string
}

// ImportProviders creates every absent record in one transaction. Existing
// names win unchanged. dryRun performs the same validation and reads, then
// rolls the transaction back without issuing inserts.
func (r *Registry) ImportProviders(ctx context.Context, records []ProviderRecord, dryRun bool) (ProviderImportResult, error) {
	ordered, err := validateImportRecords(records)
	if err != nil {
		return ProviderImportResult{}, err
	}
	if r == nil || r.db == nil || r.clock == nil {
		return ProviderImportResult{}, cascade.New(cascade.KindInvalidInput, "registry: import requires an initialized registry")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderImportResult{}, cascade.Wrap(cascade.KindUnavailable, err, "registry: begin provider import")
	}
	defer func() { _ = tx.Rollback() }()
	result, err := r.importProvidersTx(ctx, tx, ordered, dryRun)
	if err != nil {
		return ProviderImportResult{}, err
	}
	if dryRun {
		return result, nil
	}
	if err := tx.Commit(); err != nil {
		return ProviderImportResult{}, cascade.Wrap(cascade.KindUnavailable, err, "registry: commit provider import")
	}
	return result, nil
}

func validateImportRecords(records []ProviderRecord) ([]ProviderRecord, error) {
	ordered := append([]ProviderRecord(nil), records...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	for i := range ordered {
		if err := ordered[i].Validate(); err != nil {
			return nil, err
		}
		if i > 0 && ordered[i-1].Name == ordered[i].Name {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"registry: provider import repeats name %q", ordered[i].Name)
		}
	}
	return ordered, nil
}

func (r *Registry) importProvidersTx(ctx context.Context, tx *sql.Tx, records []ProviderRecord, dryRun bool) (ProviderImportResult, error) {
	result := ProviderImportResult{Created: []string{}, Existing: []string{}}
	for _, rec := range records {
		exists, err := providerExistsTx(ctx, tx, rec.Name)
		if err != nil {
			return ProviderImportResult{}, err
		}
		if exists {
			result.Existing = append(result.Existing, rec.Name)
			continue
		}
		result.Created = append(result.Created, rec.Name)
		if !dryRun {
			if err := r.insertImportedProvider(ctx, tx, rec); err != nil {
				return ProviderImportResult{}, err
			}
		}
	}
	return result, nil
}

func providerExistsTx(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM `+tableProviderRecords+` WHERE name = ?`, name).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return false, cascade.Wrapf(cascade.KindUnavailable, err, "registry: inspect imported provider %q", name)
}

func (r *Registry) insertImportedProvider(ctx context.Context, tx *sql.Tx, rec ProviderRecord) error {
	now := r.clock.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.UpdatedAt = now
	knownModels, caps, cost, err := encodeProviderJSON(rec)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO `+tableProviderRecords+`
		(name, driver_kind, base_url, auth_type, auth_ref, known_models, account_kind, tier,
		 capabilities, capabilities_probed_at, cost, health_status, health_checked_at,
		 demotion_count, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.Name, string(rec.Driver), rec.BaseURL, string(rec.Auth), string(rec.AuthRef), string(knownModels),
		string(rec.AccountKind), string(rec.Tier), string(caps), millisPtr(rec.CapabilitiesProbedAt), cost,
		string(rec.HealthStatus), millisPtr(rec.HealthCheckedAt), rec.DemotionCount,
		rec.CreatedAt.UnixMilli(), rec.UpdatedAt.UnixMilli())
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "registry: import provider %q", rec.Name)
	}
	return nil
}
