//go:build integration

// Purpose: inspect persisted migration results under the isolated test home.
// Inputs: child environment and the materialized fixture.
// Outputs: assertions against ledger rows, decrypted vault values and reports.
// Constraints: file custody is explicitly injected; platform custody is never selected.
// SPORT: migration end-to-end acceptance.
package migration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/providers/sqlite"
)

func assertEpicAbsent(t *testing.T, paths ...string) {
	t.Helper()
	if len(paths) == 0 {
		t.Fatal("absence assertion has no paths")
	}
	for _, path := range paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected destination %s: %v", path, err)
		}
	}
}

func assertEpicDryDestinations(t *testing.T, env []string) {
	t.Helper()
	root := epicEnv(env, "CASCADE_HOME")
	assertEpicAbsent(t, filepath.Join(root, "memory"), filepath.Join(root, "config.toml"), filepath.Join(root, "data", "vault.age"))
	assertEpicAccounts(t, env, 0)
}

func assertEpicAccounts(t *testing.T, env []string, want int) {
	t.Helper()
	path := filepath.Join(epicEnv(env, "CASCADE_HOME"), "data", "providers.db")
	if _, err := os.Stat(path); err != nil {
		if want == 0 && os.IsNotExist(err) {
			return
		}
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM provider_records").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("stored accounts=%d want=%d", count, want)
	}
}

func TestEpicZDryDestinationsAbsentAccounts(t *testing.T) {
	assertEpicDryDestinations(t, sealedEnv(t))
}

func assertEpicLedger(t *testing.T, env []string, done bool) {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(epicEnv(env, "CASCADE_HOME"), "data", "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ledger, err := NewLedgerStore(store, runtime.NewSystemClock())
	if err != nil {
		t.Fatal(err)
	}
	for _, domain := range []migrationv1.Domain{migrationv1.DomainMemory, migrationv1.DomainAccounts, migrationv1.DomainConfig, migrationv1.DomainVault} {
		row, found, err := ledger.ReadDomain(t.Context(), domain)
		if err != nil || found != done || (done && !row.Done()) {
			t.Fatalf("ledger %s: %+v found=%t err=%v", domain, row, found, err)
		}
	}
	it, err := store.Scan(t.Context(), LedgerNamespace, ledgerKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	n := 0
	for it.Next(t.Context()) {
		n++
	}
	if err := it.Err(); err != nil {
		t.Fatal(err)
	}
	want := 0
	if done {
		want = 4
	}
	if n != want {
		t.Fatalf("ledger rows=%d want=%d", n, want)
	}
}

func assertEpicVault(t *testing.T, env []string, source string, count int) {
	t.Helper()
	if count == 0 {
		t.Fatal("empty expected vault")
	}
	dir := filepath.Join(epicEnv(env, "CASCADE_HOME"), "data")
	if _, err := os.Stat(filepath.Join(dir, "vault.age")); err != nil {
		t.Fatal(err)
	}
	custody, err := secrets.SelectCustody(secrets.Config{
		Service: "migration-fixture", Dir: dir, ForceFileVault: true,
		Runner: func(context.Context, string, ...string) ([]byte, error) {
			return nil, fmt.Errorf("platform custody is forbidden in migration assertions")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := secrets.ParseVaultEnv(fixtureRead(t, filepath.Join(source, ".cascade", "vault.env")))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, entry := range entries {
		want = append(want, entry.Name)
		got, err := custody.Get(t.Context(), entry.Name)
		if err != nil || string(got) != string(entry.Value) || !strings.HasPrefix(string(got), "NONSECRET-") {
			t.Fatalf("vault value mismatch for %s: %v", entry.Name, err)
		}
	}
	names, err := custody.List(t.Context())
	slices.Sort(want)
	if err != nil || len(names) != count || !slices.Equal(names, want) {
		t.Fatalf("stored vault names=%v want=%v err=%v", names, want, err)
	}
}

func assertEpicRecallReport(t *testing.T, out string) {
	t.Helper()
	match := regexp.MustCompile(`recall index: ([0-9]+) chunk\(s\) written; verify healthy=true`).FindStringSubmatch(out)
	if len(match) != 2 {
		t.Fatalf("missing healthy rebuild: %s", out)
	}
	n, err := strconv.Atoi(match[1])
	if err != nil || n < 1 {
		t.Fatalf("no rebuilt chunks: %s", out)
	}
}

func epicProjectList(t *testing.T) string {
	t.Helper()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.CopyFS(project, os.DirFS("testdata/fixture_project")); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(t.TempDir(), "projects.txt")
	fixtureWrite(t, list, fmt.Appendf(nil, "%s\n", project))
	return list
}
