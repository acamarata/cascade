//go:build postgres

// Purpose: unit tests for the Migrator injection seam (Option/WithMigrator)
//
//	that need no live server — that WithMigrator sets the config field and
//	that Open's nil-migrator default is preserved when no option is given.
//	The live proof that an injected Migrator actually runs is
//	integration_test.go's TestPostgresLiveMigration.
//
// SPORT: providers.postgres.Store/CHANGED (P1-E17-W4-S38-T4).
package postgres

import (
	"context"
	"database/sql"
	"testing"
)

func TestWithMigrator_SetsConfig(t *testing.T) {
	called := false
	m := Migrator(func(context.Context, *sql.DB) error { called = true; return nil })

	cfg := openConfig{}
	WithMigrator(m)(&cfg)
	if cfg.migrator == nil {
		t.Fatal("WithMigrator did not set cfg.migrator")
	}
	if err := cfg.migrator(context.Background(), nil); err != nil {
		t.Fatalf("cfg.migrator invocation: %v", err)
	}
	if !called {
		t.Fatal("cfg.migrator did not invoke the injected Migrator")
	}
}

func TestOpenConfig_DefaultsToNilMigrator(t *testing.T) {
	cfg := openConfig{}
	if cfg.migrator != nil {
		t.Fatal("openConfig zero value should have a nil migrator")
	}
}
