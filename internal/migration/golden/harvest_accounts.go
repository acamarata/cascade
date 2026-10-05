// Purpose: harvest the accounts domain: run the real accounts importer into
// a scratch provider registry and read every record back with ListProviders.
// Inputs: one verified accounts input and the injected clock.
// Outputs: one fixture holding every redacted registry.ProviderRecord plus
// the importer's redacted changes, journal and reauth prompts.
// Constraints: the registry is a fresh SQLite file inside scratch, migrated
// with the registry's own schema; nothing outside scratch is opened.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"context"
	"database/sql"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver for the scratch registry

	v1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// harvestAccounts returns the accounts fixture for one input, using a
// scratch v1 home and registry inside scratch.
func harvestAccounts(ctx context.Context, clock runtime.Clock, scratch string, file inputFile) (fixture, error) {
	home := filepath.Join(scratch, "home")
	if err := stage(home, ".cascade/accounts/accounts.json", file.Data); err != nil {
		return fixture{}, err
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(scratch, "providers.db"))
	if err != nil {
		return fixture{}, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: open the scratch registry")
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	if err := registry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clock, "", ""); err != nil {
		return fixture{}, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: migrate the scratch registry")
	}
	reg := registry.NewRegistry(db, clock)
	res, err := runImporter(ctx, importerFor(v1.DomainAccounts, importerDeps{registry: reg}), v1.DomainAccounts, file.Rel, home)
	if err != nil {
		return fixture{}, err
	}
	records, err := reg.ListProviders(ctx)
	if err != nil {
		return fixture{}, cascade.Wrap(cascade.KindUnavailable, err, "golden harvest: read back the scratch registry")
	}
	r := newRedactor(v1.DomainAccounts, file.Rel)
	providers := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		providers = append(providers, r.provider(rec))
	}
	record := r.dryRun(res)
	record["providers"] = providers
	return newFixture(v1.DomainAccounts, "accounts", file, record)
}

// harvestAccountsInputs harvests each accounts input in its own scratch.
func harvestAccountsInputs(ctx context.Context, clock runtime.Clock, files []inputFile) ([]fixture, error) {
	out := make([]fixture, 0, len(files))
	for _, file := range files {
		fx, err := withScratch(func(scratch string) (fixture, error) {
			return harvestAccounts(ctx, clock, scratch, file)
		})
		if err != nil {
			return nil, err
		}
		out = append(out, fx)
	}
	return out, nil
}
