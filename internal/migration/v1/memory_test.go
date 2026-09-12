package v1

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestMemoryImporter_GoldenPlainCollections(t *testing.T) {
	root := stageMemoryGoldens(t)
	store := memory.NewFileStore(filepath.Join(t.TempDir(), "memory"), testClock{})
	result, err := NewMemoryImporter(store).Import(context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeltaCount() != 3 || !result.Applied || len(result.Journal) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	assertImportedMemory(t, store, "decisions", "# Technical Decisions\n")
	assertImportedMemory(t, store, "lessons", "# Lessons Learned\n")
	assertImportedMemory(t, store, "patterns", "# Codebase Patterns\n")
}

func TestMemoryImporter_TombstoneCarryThrough(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/memory/decisions.md.tombstone", nil)
	dest := filepath.Join(t.TempDir(), "memory")
	result, err := NewMemoryImporter(memory.NewFileStore(dest, testClock{})).Import(
		context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changes[0].Operation != OperationTombstone {
		t.Fatalf("operation = %q", result.Changes[0].Operation)
	}
	if _, err := os.Stat(filepath.Join(dest, "project", "decisions.md.tombstone")); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryImporter_FailClosedBeforeWrite(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, ".cascade/memory/decisions.md", []byte("# Decisions\n"))
	writeSource(t, root, ".cascade/memory/unmapped.md", []byte("# Unknown\n"))
	dest := filepath.Join(t.TempDir(), "memory")
	_, err := NewMemoryImporter(memory.NewFileStore(dest, testClock{})).Import(
		context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindInvalidInput)
	assertSentinel(t, err, ErrUnknownInput)
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination changed after refusal: %v", statErr)
	}
}

func TestMemoryImporter_RefusesUnknownShapes(t *testing.T) {
	cases := []struct {
		name string
		file string
		data []byte
		kind cascade.Kind
	}{
		{"bad extension", "decisions.txt", []byte("# Decisions\n"), cascade.KindInvalidInput},
		{"bad utf8", "decisions.md", []byte{0xff}, cascade.KindIntegrity},
		{"nul", "decisions.md", []byte("# D\x00\n"), cascade.KindIntegrity},
		{"no heading", "decisions.md", []byte("text\n"), cascade.KindIntegrity},
		{"unknown tombstone", "other.md.tombstone", nil, cascade.KindInvalidInput},
		{"nonempty unknown tombstone", "other.md.tombstone", []byte("# X\n"), cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeSource(t, root, ".cascade/memory/"+tc.file, tc.data)
			_, err := readMemorySource(root)
			assertKind(t, err, tc.kind)
		})
	}
}

func TestMemoryImporter_RefusesNonRegularSource(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".cascade", "memory")
	if err := os.MkdirAll(filepath.Join(dir, "nested.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := readMemorySource(root)
	assertKind(t, err, cascade.KindInvalidInput)

	root = t.TempDir()
	dir = filepath.Join(root, ".cascade", "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "decisions.md")); err != nil {
		t.Fatal(err)
	}
	_, err = readMemorySource(root)
	assertKind(t, err, cascade.KindInvalidInput)
}

func TestMemoryFrontmatter_ClosedSchema(t *testing.T) {
	valid := []byte("---\nname: retained\ndescription: exact description\ntype: user\nformat: 1\n---\nbody\n")
	kind, description, name, err := parseMemoryFrontmatter(valid)
	if err != nil || kind != memory.KindUser || description != "exact description" || name != "retained" {
		t.Fatalf("parsed (%q, %q, %q, %v)", kind, description, name, err)
	}
	cases := []struct {
		name string
		data string
		kind cascade.Kind
	}{
		{"missing fence", "name: x\n", cascade.KindIntegrity},
		{"missing closing", "---\nname: x\n", cascade.KindIntegrity},
		{"missing required", "---\nname: x\ntype: user\n---\n", cascade.KindIntegrity},
		{"unknown key", "---\nname: x\ndescription: d\ntype: user\nextra: x\n---\n", cascade.KindInvalidInput},
		{"duplicate", "---\nname: x\nname: y\ndescription: d\ntype: user\n---\n", cascade.KindIntegrity},
		{"invalid field", "---\nname\ndescription: d\ntype: user\n---\n", cascade.KindIntegrity},
		{"bad format", "---\nname: x\ndescription: d\ntype: user\nformat: x\n---\n", cascade.KindIntegrity},
		{"future", "---\nname: x\ndescription: d\ntype: user\nformat: 2\n---\n", cascade.KindUnsupported},
		{"unknown type", "---\nname: x\ndescription: d\ntype: mystery\n---\n", cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := parseMemoryFrontmatter([]byte(tc.data))
			assertKind(t, err, tc.kind)
		})
	}
}

func TestMemoryImporter_PreservesDisplayNameInSourceBytes(t *testing.T) {
	root := stageFixture(t, "memory/feedback-redacted.md", ".cascade/memory/feedback-redacted.md")
	data, readErr := os.ReadFile(filepath.Join("testdata", "v1-goldens", "memory", "feedback-redacted.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	store := memory.NewFileStore(filepath.Join(t.TempDir(), "memory"), testClock{})
	_, err := NewMemoryImporter(store).Import(
		context.Background(), Request{SourceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.Read(context.Background(), memory.KindFeedback, "feedback-redacted")
	if err != nil || entry.Body != string(data) {
		t.Fatalf("display name/source bytes were not preserved: entry=%+v err=%v", entry, err)
	}
}

func TestMemoryImporter_ConflictLeavesExistingRecord(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "memory")
	store := memory.NewFileStore(dest, testClock{})
	existing := memory.MemoryEntry{Name: "decisions", Kind: memory.KindProject,
		Body: "keep", ScopeRef: "existing", Confidence: 1,
		Provenance: memory.Provenance{Origin: memory.OriginHarness}}
	if err := store.Write(context.Background(), existing); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeSource(t, root, ".cascade/memory/decisions.md", []byte("# Decisions\nreplace"))
	_, err := NewMemoryImporter(store).Import(context.Background(), Request{SourceRoot: root})
	assertKind(t, err, cascade.KindConflict)
	got, readErr := store.Read(context.Background(), memory.KindProject, "decisions")
	if readErr != nil || got.Body != "keep" {
		t.Fatalf("existing record changed: body=%q err=%v", got.Body, readErr)
	}
}

func stageMemoryGoldens(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"decisions.md", "lessons.md", "patterns.md"} {
		data, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", "memory", name))
		if err != nil {
			t.Fatal(err)
		}
		writeSource(t, root, ".cascade/memory/"+name, data)
	}
	return root
}

func assertImportedMemory(t *testing.T, store *memory.FileStore, name, prefix string) {
	t.Helper()
	entry, err := store.Read(context.Background(), memory.KindProject, name)
	if err != nil {
		t.Fatal(err)
	}
	if !stringsHasPrefix(entry.Body, prefix) || entry.Provenance.SessionID != "v1:.cascade/memory/"+name+".md" {
		t.Fatalf("imported entry lost body/provenance: %+v", entry)
	}
	if !entry.Provenance.CreatedAt.Equal(testInstant) || entry.Provenance.ContentHash != entry.BodyHash() {
		t.Fatalf("imported entry lost mtime/hash: %+v", entry.Provenance)
	}
}

func stringsHasPrefix(value, prefix string) bool {
	return len(value) >= len(prefix) && value[:len(prefix)] == prefix
}

func TestMemoryImporter_MissingSourceTyped(t *testing.T) {
	_, err := readMemorySource(t.TempDir())
	assertKind(t, err, cascade.KindNotFound)
	_, err = NewMemoryImporter(nil).Import(context.Background(), Request{SourceRoot: t.TempDir()})
	assertKind(t, err, cascade.KindInvalidInput)
	_, err = readMemorySource("")
	assertKind(t, err, cascade.KindInvalidInput)
	if !errors.Is(err, cascade.ErrInvalidInput) {
		t.Fatal("missing taxonomy sentinel")
	}
}
