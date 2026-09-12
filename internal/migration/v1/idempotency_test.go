package v1

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
)

func TestImporter_Idempotent(t *testing.T) {
	t.Run("memory", assertMemoryIdempotent)
	t.Run("vault", assertVaultIdempotent)
	t.Run("accounts", assertAccountsIdempotent)
	t.Run("config", assertConfigIdempotent)
}

func TestImporter_DryRun(t *testing.T) {
	t.Run("memory", assertMemoryDryRun)
	t.Run("vault", func(t *testing.T) { TestVaultImporter_DryRunDoesNotWrite(t) })
	t.Run("accounts", func(t *testing.T) { TestAccountsImporter_DryRunDoesNotWrite(t) })
	t.Run("config", func(t *testing.T) { TestConfigTranslator_DryRunDoesNotWrite(t) })
}

func assertMemoryIdempotent(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/memory/decisions.md", []byte("# Decisions\nbody\n"))
	store := memory.NewFileStore(filepath.Join(t.TempDir(), "memory"), testClock{})
	assertSecondRunEmpty(t, NewMemoryImporter(store), root)
}

func assertVaultIdempotent(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/vault.env", []byte("A_KEY=opaque\n"))
	assertSecondRunEmpty(t, NewVaultImporter(testBroker(t, newMapCustody())), root)
}

func assertAccountsIdempotent(t *testing.T) {
	root := stageFixture(t, "accounts/accounts.json", ".cascade/accounts/accounts.json")
	store, _ := testRegistry(t)
	assertSecondRunEmpty(t, NewAccountsImporter(store), root)
}

func assertConfigIdempotent(t *testing.T) {
	root := stageFixture(t, "config/daemon-known.toml", ".cascade/config.toml")
	assertSecondRunEmpty(t, NewConfigImporter(filepath.Join(t.TempDir(), "config.toml")), root)
}

func assertSecondRunEmpty(t *testing.T, importer Importer, root string) {
	t.Helper()
	first, err := importer.Import(context.Background(), Request{SourceRoot: root})
	if err != nil || first.EmptyDelta() {
		t.Fatalf("first run did not change destination: result=%+v err=%v", first, err)
	}
	second, err := importer.Import(context.Background(), Request{SourceRoot: root})
	if err != nil || !second.EmptyDelta() {
		t.Fatalf("second run did not converge: result=%+v err=%v", second, err)
	}
}

func assertMemoryDryRun(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/memory/decisions.md", []byte("# Decisions\nbody\n"))
	destination := filepath.Join(t.TempDir(), "memory")
	result, err := NewMemoryImporter(memory.NewFileStore(destination, testClock{})).Import(
		context.Background(), Request{SourceRoot: root, DryRun: true})
	if err != nil || result.Applied || result.DeltaCount() != 1 {
		t.Fatalf("unexpected memory dry-run: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("memory dry run wrote destination: %v", err)
	}
}

func TestDryRunResult_NormalizesAndCountsOnlyMutations(t *testing.T) {
	result := DryRunResult{Changes: []Change{
		{Operation: OperationCreate}, {Operation: OperationUpdate},
		{Operation: OperationTombstone}, {Operation: OperationSkip},
		{Operation: OperationUnchanged},
	}}
	if result.DeltaCount() != 3 || result.EmptyDelta() {
		t.Fatalf("delta semantics drifted: %+v", result)
	}
	empty := (DryRunResult{}).Normalize()
	if empty.Changes == nil || empty.Journal == nil || empty.Reauth == nil {
		t.Fatalf("normalize left nil JSON slices: %+v", empty)
	}
}
