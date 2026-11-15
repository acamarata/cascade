package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
)

// TestTopologyDoctorSourceReadsAFreshDatabaseAsEmpty: a cascade.db the
// daemon never migrated (a fresh v1 migration) has no topology tables; the
// doctor source reads it as no rows, the same as a missing cascade.db.
func TestTopologyDoctorSourceReadsAFreshDatabaseAsEmpty(t *testing.T) {
	paths := fakeDaemonPaths{root: t.TempDir()}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(paths.DataDir(), "cascade.db")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE unrelated (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	src := topologyDoctorSourceFor(paths, runtime.NewSystemClock())
	creds, err := src.ListCredentials(context.Background())
	if err != nil || len(creds) != 0 {
		t.Fatalf("credentials = %v, err %v; want none and no error", creds, err)
	}
	domains, err := src.ListQuotaDomains(context.Background())
	if err != nil || len(domains) != 0 {
		t.Fatalf("domains = %v, err %v; want none and no error", domains, err)
	}
}
