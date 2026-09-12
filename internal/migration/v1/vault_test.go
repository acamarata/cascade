package v1

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestVaultImporter_NoTempFile(t *testing.T) {
	root := t.TempDir()
	source := []byte("# harvested shape\nOPAQUE_ONE=alpha\nOPAQUE_TWO=\"line one\nline two\"\nOPAQUE_ONE=final\n")
	writeSource(t, root, ".claude/vault.env", source)
	before := walkRelative(t, root)
	custody := newMapCustody()
	result, err := NewVaultImporter(testBroker(t, custody)).Import(
		context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	after := walkRelative(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("source tree changed or temp file appeared: before=%v after=%v", before, after)
	}
	if string(custody.values["OPAQUE_ONE"]) != "final" || string(custody.values["OPAQUE_TWO"]) != "line one\nline two" {
		t.Fatal("opaque or multiline value did not survive in memory")
	}
	if result.DeltaCount() != 2 || len(result.Journal) != 1 {
		t.Fatalf("unexpected import result: %+v", result)
	}
}

func TestVaultImporter_RedactedGoldenFailsClosed(t *testing.T) {
	root := stageFixture(t, "vault/vault.env", ".claude/vault.env")
	custody := newMapCustody()
	_, err := NewVaultImporter(testBroker(t, custody)).Import(
		context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindIntegrity)
	assertSentinel(t, err, ErrMalformedInput)
	if len(custody.values) != 0 {
		t.Fatal("redacted fixture reached custody")
	}
}

func TestVaultGoldenContainsOnlyRedactedValues(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", "vault", "vault.env"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := secrets.ParseVaultEnv(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !bytes.Equal(entry.Value, []byte("REDACTED")) {
			t.Fatalf("fixture key %q is not redacted", entry.Name)
		}
	}
}

func TestVaultImporter_FailedImportRollsBack(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/vault.env", []byte("A_NEW=one\nB_FAIL=two\nC_KEEP=changed\n"))
	custody := newMapCustody()
	custody.values["C_KEEP"] = []byte("original")
	custody.failSetName, custody.failOnce = "B_FAIL", true
	_, err := NewVaultImporter(testBroker(t, custody)).Import(
		context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindUnavailable)
	if len(custody.values) != 1 || string(custody.values["C_KEEP"]) != "original" {
		t.Fatalf("failed import left partial writes: %#v", custody.values)
	}
}

func TestVaultImporter_CollisionIsVisibleAndConverges(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/vault.env", []byte("EXISTING=replacement\n"))
	custody := newMapCustody()
	custody.values["EXISTING"] = []byte("original")
	importer := NewVaultImporter(testBroker(t, custody))
	first, err := importer.Import(context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if first.Changes[0].Operation != OperationUpdate || string(custody.values["EXISTING"]) != "replacement" {
		t.Fatalf("collision decision was not explicit: %+v", first)
	}
	second, err := importer.Import(context.Background(), Request{SourceRoot: root})
	if err != nil || !second.EmptyDelta() || second.Changes[0].Operation != OperationUnchanged {
		t.Fatalf("second import did not converge: result=%+v err=%v", second, err)
	}
}

func TestVaultImporter_DryRunDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".claude/vault.env", []byte("NEW_KEY=new\nOLD_KEY=same\n"))
	custody := newMapCustody()
	custody.values["OLD_KEY"] = []byte("same")
	result, err := NewVaultImporter(testBroker(t, custody)).Import(
		context.Background(), Request{SourceRoot: root, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || result.DeltaCount() != 1 || len(custody.values) != 1 {
		t.Fatalf("dry run mutated custody: result=%+v values=%v", result, custody.values)
	}
}

func TestVaultImporter_RefusesAmbiguousOrMalformedSource(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".claude/vault.env", []byte("A=one\n"))
	writeSource(t, root, ".cascade/vault.env", []byte("B=two\n"))
	_, _, err := readVaultSource(root)
	assertKind(t, err, cascade.KindConflict)

	root = t.TempDir()
	writeSource(t, root, ".claude/vault.env", []byte("NOT AN ASSIGNMENT\n"))
	custody := newMapCustody()
	_, err = NewVaultImporter(testBroker(t, custody)).Import(context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindInvalidInput)
	if len(custody.values) != 0 {
		t.Fatal("malformed source caused writes")
	}
}

func TestVaultImporter_SourceErrorsAreTyped(t *testing.T) {
	_, _, err := readVaultSource("")
	assertKind(t, err, cascade.KindInvalidInput)
	_, _, err = readVaultSource(t.TempDir())
	assertKind(t, err, cascade.KindNotFound)
	_, err = NewVaultImporter(nil).Import(context.Background(), Request{SourceRoot: t.TempDir()})
	assertKind(t, err, cascade.KindInvalidInput)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".claude", "vault.env"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = readVaultSource(root)
	assertKind(t, err, cascade.KindInvalidInput)
}

func TestVaultImporter_ErrorDoesNotEchoValue(t *testing.T) {
	root := t.TempDir()
	secretValue := "do-not-echo-this-value"
	writeSource(t, root, ".claude/vault.env", []byte("BROKEN=\""+secretValue+"\n"))
	_, err := NewVaultImporter(testBroker(t, newMapCustody())).Import(
		context.Background(), Request{SourceRoot: root})
	if err == nil || strings.Contains(err.Error(), secretValue) {
		t.Fatalf("failure absent or leaked value: %v", err)
	}
}

func walkRelative(t *testing.T, root string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		paths = append(paths, relative)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}
