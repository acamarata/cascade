package runtime

// Purpose: TestCIWatch* proves [ci.watch] registers via Load
// (P1-E25-W5-S51-T4, task 1): defaults, a valid explicit value, every
// error path config_ci_watch.go names, and the write-side round-trip and
// idempotent add/remove semantics config_ci_watch_write.go implements --
// reusing config_ci_test.go's loadCITestConfig/writeConfigFile helper
// shape.

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func loadCIWatchTestConfig(t *testing.T, toml string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := writeConfigFile(t, dir, toml)
	return Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
}

func TestCIWatchDefaultsToEmpty(t *testing.T) {
	cfg, err := loadCIWatchTestConfig(t, "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CIWatch.Entries) != 0 {
		t.Errorf("Entries = %v, want empty", cfg.CIWatch.Entries)
	}
}

func TestCIWatchExplicitEntries(t *testing.T) {
	cfg, err := loadCIWatchTestConfig(t, `ci.watch = [{repo = "acamarata/cascade", branch = "main", workflow = "build-*"}, {repo = "acamarata/curtain"}]`+"\n")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CIWatch.Entries) != 2 {
		t.Fatalf("Entries = %+v, want 2", cfg.CIWatch.Entries)
	}
	got := cfg.CIWatch.Entries[0]
	want := CIWatchEntry{Repo: "acamarata/cascade", Branch: "main", Workflow: "build-*"}
	if got != want {
		t.Errorf("Entries[0] = %+v, want %+v", got, want)
	}
	got2 := cfg.CIWatch.Entries[1]
	want2 := CIWatchEntry{Repo: "acamarata/curtain"}
	if got2 != want2 {
		t.Errorf("Entries[1] = %+v, want %+v (branch/workflow default to empty = match all)", got2, want2)
	}
}

func TestCIWatchInvalidRepoPattern(t *testing.T) {
	_, err := loadCIWatchTestConfig(t, `ci.watch = [{repo = "not-a-repo-pattern"}]`+"\n")
	if err == nil {
		t.Fatal("Load: want an error for a repo pattern with no \"/\"")
	}
	if !strings.Contains(err.Error(), "ci.watch") {
		t.Errorf("error %q does not name ci.watch", err.Error())
	}
}

func TestCIWatchInvalidGlob(t *testing.T) {
	_, err := loadCIWatchTestConfig(t, `ci.watch = [{repo = "a/b", branch = "["}]`+"\n")
	if err == nil {
		t.Fatal("Load: want an error for an unparseable branch glob")
	}
}

func TestCIWatchMissingRepo(t *testing.T) {
	_, err := loadCIWatchTestConfig(t, `ci.watch = [{branch = "main"}]`+"\n")
	if err == nil {
		t.Fatal("Load: want an error for a ci.watch entry with no repo")
	}
}

func TestCIWatchUnrecognisedKey(t *testing.T) {
	_, err := loadCIWatchTestConfig(t, `ci.watch = [{repo = "a/b", tag = "x"}]`+"\n")
	if err == nil {
		t.Fatal("Load: want an error for an unrecognised ci.watch entry key")
	}
}

func TestCIWatchNotAnArray(t *testing.T) {
	_, err := loadCIWatchTestConfig(t, `ci.watch = "not-an-array"`+"\n")
	if err == nil {
		t.Fatal("Load: want an error when ci.watch is not an array")
	}
}

func TestWriteCIWatchEntries_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "")
	entries := []CIWatchEntry{
		{Repo: "acamarata/cascade", Branch: "main", Workflow: "build-*"},
		{Repo: "acamarata/curtain"},
	}
	if err := WriteCIWatchEntries(path, entries); err != nil {
		t.Fatalf("WriteCIWatchEntries: %v", err)
	}
	cfg, err := Load(context.Background(), LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil)})
	if err != nil {
		t.Fatalf("Load after write: %v", err)
	}
	if len(cfg.CIWatch.Entries) != 2 || cfg.CIWatch.Entries[0] != entries[0] || cfg.CIWatch.Entries[1] != entries[1] {
		t.Errorf("round-tripped entries = %+v, want %+v", cfg.CIWatch.Entries, entries)
	}
}

func TestWriteCIWatchEntries_RefusesAnInvalidEntryBeforeTouchingDisk(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	err = WriteCIWatchEntries(path, []CIWatchEntry{{Repo: "not-valid"}})
	if err == nil {
		t.Fatal("WriteCIWatchEntries: want an error for an invalid entry")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading fixture: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("disk changed on a refused write:\nbefore: %q\nafter:  %q", before, after)
	}
}

// TestUpsertCIWatchEntry_AddIsIdempotent is AC4's config-layer proof: a
// second add for the same repo updates the existing entry rather than
// appending a duplicate.
func TestUpsertCIWatchEntry_AddIsIdempotent(t *testing.T) {
	entries, updated := UpsertCIWatchEntry(nil, CIWatchEntry{Repo: "a/b", Branch: "main"})
	if updated {
		t.Fatalf("first add reported updated=true, want a fresh append")
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1 after first add", entries)
	}

	entries, updated = UpsertCIWatchEntry(entries, CIWatchEntry{Repo: "a/b", Branch: "release-*", Workflow: "build-*"})
	if !updated {
		t.Fatalf("second add for the same repo reported updated=false, want true")
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want still exactly 1 after a same-repo second add (no duplicate)", entries)
	}
	want := CIWatchEntry{Repo: "a/b", Branch: "release-*", Workflow: "build-*"}
	if entries[0] != want {
		t.Errorf("entries[0] = %+v, want the updated delta %+v", entries[0], want)
	}
}

func TestUpsertCIWatchEntry_DoesNotMutateItsInput(t *testing.T) {
	original := []CIWatchEntry{{Repo: "a/b"}}
	_, _ = UpsertCIWatchEntry(original, CIWatchEntry{Repo: "a/b", Branch: "main"})
	if original[0].Branch != "" {
		t.Errorf("UpsertCIWatchEntry mutated its input slice in place: %+v", original)
	}
}

// TestRemoveCIWatchEntry_AbsentRepoIsANoOp is AC4's other half: removing
// an entry that is not present exits successfully with removed=false,
// never an error, and leaves the slice unchanged.
func TestRemoveCIWatchEntry_AbsentRepoIsANoOp(t *testing.T) {
	entries := []CIWatchEntry{{Repo: "a/b"}}
	out, removed := RemoveCIWatchEntry(entries, "c/d")
	if removed {
		t.Fatalf("removed=true for an absent repo, want false")
	}
	if len(out) != 1 || out[0].Repo != "a/b" {
		t.Errorf("entries changed on a no-op remove: %+v", out)
	}
}

func TestRemoveCIWatchEntry_RemovesTheMatch(t *testing.T) {
	entries := []CIWatchEntry{{Repo: "a/b"}, {Repo: "c/d"}}
	out, removed := RemoveCIWatchEntry(entries, "a/b")
	if !removed {
		t.Fatalf("removed=false for a present repo, want true")
	}
	if len(out) != 1 || out[0].Repo != "c/d" {
		t.Errorf("entries = %+v, want only c/d left", out)
	}
}

// TestWriteCIWatchEntries_RefusesTheArrayOfTablesForm is the write-side
// fail-closed guard: a hand-written `[[ci.watch]]` array-of-TABLES header
// form LOADS fine (both TOML shapes decode to the identical
// []interface{}, so no reader can tell them apart), but this
// line-oriented writer can only rewrite the single-key inline form. The
// refusal is typed KindInvalidInput, names the supported form, and leaves
// the file byte-identical -- nothing accepted on read is left silently
// un-round-trippable on write.
func TestWriteCIWatchEntries_RefusesTheArrayOfTablesForm(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[[ci.watch]]\nrepo = \"a/b\"\n")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the seeded config: %v", err)
	}

	// The load half of the asymmetry: this file is accepted by Load.
	if _, err := Load(context.Background(), LoadOptions{
		Path: path, Getenv: func(string) string { return "" }, Environ: fakeEnviron(nil),
	}); err != nil {
		t.Fatalf("Load refused the [[ci.watch]] form: %v (the premise of this test is that it loads)", err)
	}

	writeErr := WriteCIWatchEntries(path, []CIWatchEntry{{Repo: "c/d"}})
	if writeErr == nil {
		t.Fatalf("WriteCIWatchEntries accepted a [[ci.watch]] document; want a typed refusal")
	}
	if !cascade.HasKind(writeErr, cascade.KindInvalidInput) {
		t.Errorf("error kind = %v, want KindInvalidInput: %v", writeErr, writeErr)
	}
	if !strings.Contains(writeErr.Error(), `watch = [{repo = "owner/repo"`) {
		t.Errorf("the refusal does not name the supported inline form: %v", writeErr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-reading the config: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("disk changed on a refused write:\n  before: %q\n  after:  %q", before, after)
	}
}

// TestWriteCIWatchEntries_IgnoresOtherArrayOfTablesHeaders proves the
// guard is scoped to [[ci.watch]] and does not refuse a document that
// legitimately carries an unrelated array of tables.
func TestWriteCIWatchEntries_IgnoresOtherArrayOfTablesHeaders(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "[[fleet.accounts]]\nid = \"a1\"\n")
	if err := WriteCIWatchEntries(path, []CIWatchEntry{{Repo: "c/d"}}); err != nil {
		t.Fatalf("WriteCIWatchEntries refused an unrelated [[...]] header: %v", err)
	}
}
