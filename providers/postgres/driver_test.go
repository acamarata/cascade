//go:build postgres

// Purpose: the storetest-under-docker entry point .github/workflows/ci.yml
//
//	invokes against a real postgres:17 service container. In W1 the
//	Driver is a total stub (store.go: every method returns
//	cascade.ErrUnsupported without touching the wire), so every
//	storetest.RunStoreTests sub-test here is EXPECTED to fail — that is
//	exactly why the CI job carries `continue-on-error: true` and the
//	Q/S-38.T4 reference (providers/postgres/testdata/README.md records
//	the allowed-fail rationale in full). This file exists so the lane
//	has something real to execute under -tags=postgres today, and so
//	Q/S-38.T4 only has to swap store.go's body — this test file, and
//	the CI job that calls it, stay as-is (minus continue-on-error) once
//	the real driver passes.
//
// Constraints: Art.7.1 — t.TempDir() would apply to any on-disk state,
//
//	but this stub holds none; the factory below opens no real
//	connection (Open never dials), so there is nothing to isolate
//	per-subtest beyond the fresh zero-value struct.
//
// SPORT: providers.postgres.Store/ADDED (P1-E02-W1-S03-T5).
package postgres_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/postgres"
)

// TestPostgresStore_Conformance runs the shared provider.Store conformance
// suite (internal/storage/storetest.RunStoreTests) against the stub
// Driver. W1: every sub-test fails because the stub refuses with
// cascade.ErrUnsupported before doing anything — allowed-fail per this
// ticket's CI job (continue-on-error: true), closed out by Q/S-38.T4 when
// the real driver replaces store.go's body.
func TestPostgresStore_Conformance(t *testing.T) {
	storetest.RunStoreTests(t, func(t *testing.T) provider.Store {
		t.Helper()
		d, err := postgres.Open(context.Background(), "")
		if err != nil {
			t.Fatalf("postgres.Open: %v", err)
		}
		return d
	})
}
