// Purpose: unit coverage for productionMigrateFactory's memory and
//
//	unknown-domain branches, which shipped with no direct test of their
//	own (migrate_test.go exercises runMigrateV1DryRun against an injected
//	fake factory, never this real one). CASCADE_HOME is redirected to
//	t.TempDir() so lazyPaths never touches the operator's real home.
//
// SPORT: cmd.cascade.migrate/TEST (migrate v1 composition root).
package main

import (
	"context"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
)

func TestProductionMigrateFactory_MemoryDomain(t *testing.T) {
	t.Setenv("CASCADE_HOME", t.TempDir())
	importer, closeFn, err := productionMigrateFactory(context.Background(), migrationv1.DomainMemory)
	if err != nil {
		t.Fatalf("productionMigrateFactory(DomainMemory): %v", err)
	}
	if importer == nil {
		t.Error("productionMigrateFactory(DomainMemory): importer is nil")
	}
	if closeFn == nil {
		t.Fatal("productionMigrateFactory(DomainMemory): closeFn is nil")
	}
	if err := closeFn(); err != nil {
		t.Errorf("closeFn(): %v", err)
	}
}

func TestProductionMigrateFactory_UnknownDomainRefuses(t *testing.T) {
	t.Setenv("CASCADE_HOME", t.TempDir())
	_, _, err := productionMigrateFactory(context.Background(), migrationv1.Domain("not-a-real-domain"))
	if err == nil {
		t.Fatal("productionMigrateFactory(unknown domain) = nil error, want a refusal")
	}
}

func TestProductionMigrateFactory_ConfigDomain(t *testing.T) {
	t.Setenv("CASCADE_HOME", t.TempDir())
	importer, closeFn, err := productionMigrateFactory(context.Background(), migrationv1.DomainConfig)
	if err != nil {
		t.Fatalf("productionMigrateFactory(DomainConfig): %v", err)
	}
	if importer == nil {
		t.Error("productionMigrateFactory(DomainConfig): importer is nil")
	}
	if closeFn == nil {
		t.Fatal("productionMigrateFactory(DomainConfig): closeFn is nil")
	}
	if err := closeFn(); err != nil {
		t.Errorf("closeFn(): %v", err)
	}
}
