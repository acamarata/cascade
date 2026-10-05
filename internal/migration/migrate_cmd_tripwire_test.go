package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/output"
)

const tripwireFixture = "internal/memory/testdata/v1-goldens/migration/fixture.json"

func tripwireModule(t *testing.T, current bool) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	writeTripwireFile(t, root, "go.mod", "module github.com/acamarata/cascade\n")
	var lines []string
	for _, base := range []string{"memory", "secrets", "providers/registry", "runtime"} {
		rel := "internal/" + base + "/testdata/v1-goldens/migration/fixture.json"
		writeTripwireFile(t, root, rel, "{}\n")
		lines = append(lines, fmt.Sprintf("%x  %s", sha256.Sum256([]byte("{}\n")), rel))
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i][66:] < lines[j][66:] })
	writeTripwireFile(t, root, "internal/migration/testdata/golden-checksums.sha256", strings.Join(lines, "\n")+"\n")
	if !current {
		writeTripwireFile(t, root, tripwireFixture, "changed\n")
	}
	return root
}

func writeTripwireFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateRunsTripwireInsideRepo(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(fmt.Sprintf("dry=%v", dry), func(t *testing.T) {
			root := tripwireModule(t, false)
			var stderr bytes.Buffer
			ff := newFakeFactory()
			calls := 0
			factory := func(ctx context.Context, d migrationv1.Domain) (migrationv1.Importer, func() error, error) {
				calls++
				if strings.Contains(stderr.String(), tripwireFixture) != dry {
					t.Fatal("warning at wrong importer boundary")
				}
				return ff.factory(ctx, d)
			}
			opts := MigrateV1Options{SourceRoot: root, Yes: true, DryRun: dry, WorkDir: root, Warn: output.New(nil, &stderr, false, false, false, true)}
			ledger := newTestLedgerAt(t, t.TempDir())
			_, err := MigrateV1(context.Background(), ledger, factory, opts)
			if err != nil || calls != len(DomainOrder) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if !strings.Contains(stderr.String(), tripwireFixture) || strings.Count(stderr.String(), "\n") != 1 {
				t.Fatalf("warning: %q", stderr.String())
			}
			opts.Warn = nil
			_, baseline := MigrateV1(context.Background(), newTestLedgerAt(t, t.TempDir()), newFakeFactory().factory, opts)
			if baseline != err {
				t.Fatalf("exit changed: %v vs %v", err, baseline)
			}
			for _, d := range DomainOrder {
				row, found, readErr := ledger.ReadDomain(context.Background(), d)
				if readErr != nil || found == dry || (!dry && row.Status != StatusDone) {
					t.Fatalf("ledger %s: %+v %v %v", d, row, found, readErr)
				}
			}
		})
	}
}

func assertNoTripwireWarning(t *testing.T, root string) {
	t.Helper()
	var stderr bytes.Buffer
	ff := newFakeFactory()
	_, err := MigrateV1(context.Background(), newTestLedgerAt(t, t.TempDir()), ff.factory, MigrateV1Options{
		SourceRoot: t.TempDir(), DryRun: true, WorkDir: root, Warn: output.New(nil, &stderr, false, false, false, true),
	})
	if err != nil || len(ff.opened) != len(DomainOrder) {
		t.Fatalf("migration not exercised: %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected warning: %q", stderr.String())
	}
}

func TestMigrateSkipsTripwireOutsideRepo(t *testing.T) { assertNoTripwireWarning(t, t.TempDir()) }

func TestMigrateSkipsTripwireOtherModule(t *testing.T) {
	root := tripwireModule(t, false)
	writeTripwireFile(t, root, "go.mod", "module example.test/other\n")
	assertNoTripwireWarning(t, root)
}

func TestMigrateTripwireCurrentFixtures(t *testing.T) {
	assertNoTripwireWarning(t, tripwireModule(t, true))
}

func TestMigrateTripwireEmptyWorkDir(t *testing.T) { assertNoTripwireWarning(t, "") }
