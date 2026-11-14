//go:build !windows

// Purpose: a home left by a build that predates the controller designation
// (jobs rows, no binding, no designation) starts, bootstraps and serves
// without touching what the operator already had.
// Constraints: the fixture is the committed text dump, loaded into
// <dataDir>/cascade.db through modernc.org/sqlite at test time; jobs_* rows
// are compared per table over the fixture's columns (the daemon adds the
// outbox tables and other domains' tables, so those may grow).
// SPORT: cmd/cascade/daemon (test).

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/nodes"
)

const (
	preDesignationDump = "testdata/pre-designation-home/cascade.sql"
	preConfigText      = "# operator config from before the designation\n"
	preKeyBytes        = "pre-designation-key-material"
)

// loadPreDesignationHome builds the f688c0b-layout data dir from the dump.
func loadPreDesignationHome(t *testing.T, dataDir string) {
	t.Helper()
	dump, err := os.ReadFile(preDesignationDump)
	if err != nil {
		t.Fatalf("read %s: %v", preDesignationDump, err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dataDir, err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "cascade.db"))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(string(dump)); err != nil {
		t.Fatalf("load %s: %v", preDesignationDump, err)
	}
}

// jobsRows is every jobs_* table's rows over its own columns, ordered, as
// one text block per table. tables, when non-nil, limits and orders them.
func jobsRows(t *testing.T, dbPath string, tables []string) (map[string]string, []string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	if tables == nil {
		tables = jobsTableNames(t, db)
	}
	out := map[string]string{}
	for _, table := range tables {
		out[table] = tableRows(t, db, table)
	}
	return out, tables
}

func jobsTableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'jobs\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		t.Fatalf("list jobs tables: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	return names
}

func tableRows(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	cols := tableColumns(t, db, table)
	rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", strings.Join(cols, ", "), table, strings.Join(cols, ", ")))
	if err != nil {
		t.Fatalf("select %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var sb strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		fmt.Fprintf(&sb, "%v\n", vals)
	}
	return sb.String()
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer func() { _ = rows.Close() }()
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info %s: %v", table, err)
		}
		cols = append(cols, name)
	}
	return cols
}

func TestUpgradeFromPreDesignationHome(t *testing.T) {
	deps := freshHomeDeps(t)
	dataDir := deps.Paths.DataDir()
	loadPreDesignationHome(t, dataDir)
	configPath := deps.Paths.ConfigPath()
	keyPath := filepath.Join(deps.SecretsDir, "pre-designation.key")
	if err := os.WriteFile(configPath, []byte(preConfigText), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(preKeyBytes), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	dbPath := filepath.Join(dataDir, "cascade.db")
	before, tables := jobsRows(t, dbPath, nil)
	if len(tables) < 7 || before["jobs_job"] == "" {
		t.Fatalf("fixture jobs tables = %v, jobs_job rows %q; want the seven-table jobs set with rows", tables, before["jobs_job"])
	}

	w, cancel, done := startTestDaemon(t, deps, observeWiring(t))
	assertUpgradedHome(t, w, dataDir)
	cancel()
	awaitDaemonReturn(t, done)

	assertBytes(t, configPath, preConfigText)
	assertBytes(t, keyPath, preKeyBytes)
	after, _ := jobsRows(t, dbPath, tables)
	for _, table := range tables {
		if after[table] != before[table] {
			t.Errorf("%s rows changed by the first start:\nbefore:\n%s\nafter:\n%s", table, before[table], after[table])
		}
	}
}

// assertUpgradedHome: the home bootstrapped as the controller, the
// scheduler runs and the controller-only guard admits job.advance.
func assertUpgradedHome(t *testing.T, w *daemonWiring, dataDir string) {
	t.Helper()
	d, found, err := nodes.NewFileDesignationBackend(dataDir).Load()
	if err != nil || !found || d.Epoch != 1 || d.Reason != "bootstrap" {
		t.Fatalf("designation = %+v found=%v err=%v; want epoch 1, reason bootstrap", d, found, err)
	}
	if s := subsystem(t, w, "jobs.scheduler"); s.State != daemon.SubsystemRunning {
		t.Fatalf("jobs.scheduler = %v %q after the upgrade, want Running", s.State, s.Detail)
	}
	guard := newRoleGuard(context.Background(), w.Deps.Fleet.JobsDB())
	if err := guard.RequireController("job.advance"); err != nil {
		t.Fatalf("job.advance guard after the upgrade: %v", err)
	}
}

func assertBytes(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("%s = %q (err %v); want it byte-equal to %q", path, got, err, want)
	}
}
