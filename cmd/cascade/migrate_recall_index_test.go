package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	migrationv1 "github.com/acamarata/cascade/internal/migration/v1"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/lifecycle"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

func TestMigrateV1_RebuildsRecallIndex(t *testing.T) {
	t.Run("stored catalog and chunks", testMigrationStoredRecall)
	for _, tc := range []struct {
		name, flag string
		verify     lifecycle.VerifyReport
		wantCalls  int
		wantError  bool
	}{
		{name: "full", verify: lifecycle.VerifyReport{MarkerStatus: lifecycle.MarkerCurrent}, wantCalls: 1},
		{name: "verify failure", verify: lifecycle.VerifyReport{MarkerStatus: lifecycle.MarkerCurrent, Missing: []string{"missing"}}, wantCalls: 1, wantError: true},
		{name: "dry run", flag: "--dry-run", wantCalls: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			deps := migrateDeps{
				Factory: fakeMigrateFactory(&recordingImporter{result: migrationv1.DryRunResult{
					Domain: migrationv1.DomainMemory, Changes: []migrationv1.Change{{Operation: migrationv1.OperationCreate}},
				}}, nil),
				OpenLedger: noopOpenLedger(t),
				Getenv:     func(string) string { return "" },
				RebuildIndex: func(context.Context) (migrationv1.RebuildIndexResult, error) {
					calls++
					return migrationv1.RebuildIndexResult{
						Rebuild: lifecycle.RebuildResult{ChunksWritten: 1}, Verify: tc.verify,
					}, nil
				},
			}
			cmd := newMigrateCmd(deps)
			var out bytes.Buffer
			cmd.SetOut(&out)
			args := []string{"v1", "--from", t.TempDir(), "--yes"}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			cmd.SetArgs(args)
			err := cmd.Execute()
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
			if calls != tc.wantCalls {
				t.Fatalf("rebuild calls = %d, want %d", calls, tc.wantCalls)
			}
			assertMigrationRecallOutput(t, out.String(), err, tc.wantCalls, tc.wantError)
		})
	}
}

func assertMigrationRecallOutput(t *testing.T, out string, err error, wantCalls int, wantError bool) {
	t.Helper()
	if !strings.Contains(out, "change(s)") {
		t.Fatalf("missing import report: %q", out)
	}
	if wantCalls == 1 && !strings.Contains(out, "recall index") {
		t.Fatalf("missing rebuild report: %q", out)
	}
	if wantError && !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing verify details: %v", err)
	}
	if wantError {
		var failure *RecallRebuildError
		if !errors.As(err, &failure) || !cascade.HasKind(err, cascade.KindIntegrity) || len(failure.Report.Missing) != 1 {
			t.Fatalf("verify failure lost typed report: %v", err)
		}
		if strings.Index(out, "change(s)") >= strings.Index(out, "recall index") {
			t.Fatal("rebuild report precedes import report")
		}
	}
}

func testMigrationStoredRecall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", filepath.Join(home, "destination"))
	t.Setenv("CASCADE_CONFIG", "")
	source := t.TempDir()
	dir := filepath.Join(source, ".cascade", "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"decisions.md", "lessons.md"} {
		data, err := os.ReadFile(filepath.Join("../../internal/migration/testdata/v1-home/dot-cascade/memory", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deps := productionMigrateDeps()
	deps.Factory = func(ctx context.Context, domain migrationv1.Domain) (migrationv1.Importer, func() error, error) {
		if domain == migrationv1.DomainMemory {
			return productionMigrateFactory(ctx, domain)
		}
		return &recordingImporter{result: migrationv1.DryRunResult{Domain: domain}}, func() error { return nil }, nil
	}
	cmd := newMigrateCmd(deps)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"v1", "--from", source, "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := migratedRecallChunks(t, filepath.Join(lazyPaths{}.Root(), "memory"))
	assertMigrationCatalog(t, want)
	if !strings.Contains(out.String(), fmt.Sprintf("%d chunk(s) written; verify healthy=true", len(want))) {
		t.Fatalf("unexpected report: %s", out.String())
	}
	assertMigrationStoredChunks(t, want)
}

func assertMigrationStoredChunks(t *testing.T, want map[string]retrieval.Chunk) {
	t.Helper()
	store, err := sqlite.Open(t.Context(), filepath.Join(lazyPaths{}.DataDir(), "cascade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	for id, chunk := range want {
		raw, err := store.Get(t.Context(), retrieval.IndexNamespace, "fts:doc:"+id)
		if err != nil {
			t.Fatal(err)
		}
		var row struct {
			Content string `json:"content"`
			Path    string `json:"path"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatal(err)
		}
		if row.Content != string(chunk.Content) || row.Path != chunk.Path {
			t.Fatalf("stored chunk differs: %s", id)
		}
	}
}

func migratedRecallChunks(t *testing.T, root string) map[string]retrieval.Chunk {
	t.Helper()
	want := map[string]retrieval.Chunk{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		chunker, err := retrieval.ChunkerFor(path)
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		chunks, err := chunker.Chunk(path, data)
		if err != nil {
			return err
		}
		for _, chunk := range chunks {
			want[chunk.ID] = chunk
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("migration produced no chunkable files")
	}
	return want
}

func assertMigrationCatalog(t *testing.T, want map[string]retrieval.Chunk) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(lazyPaths{}.DataDir(), "retrieval", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc recall.CatalogDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Records) != len(want) {
		t.Fatalf("catalog has %d records, want %d", len(doc.Records), len(want))
	}
	for _, row := range doc.Records {
		if _, ok := want[row.ID]; !ok {
			t.Fatalf("unexpected catalog row: %s", row.ID)
		}
	}
}
