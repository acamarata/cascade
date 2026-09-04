package memory

// Purpose: the export canaries. Export is the single most dangerous
//   operation this package offers — it takes the system's model of a
//   person and writes it to a file that leaves the machine — so these
//   tests plant, in the same store and the same tree, exactly the things
//   an export must never pick up, and assert on the ACTUAL exported bytes
//   rather than on the struct that produced them.
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/testkit"
)

// soulCanaries are the values planted around an export. Each one is a
// distinct class of thing that must not leave with the export: another
// record's body, a credential-shaped value, and an absolute machine path.
var soulCanaries = []struct {
	name  string
	value string
}{
	{"another record's body", "CANARY-OTHER-RECORD-9f2c"},
	{"a credential-shaped value", "sk-live-CANARYSECRET0123456789abcdef"},
	{"an absolute machine path", "/Users/canary-operator/.cascade/memory"},
}

// TestSoulExportCarriesTheSoulAndNothingElse is the export canary.
//
// Export is the single most dangerous operation this package offers: it
// takes the system's model of a person and writes it to a file that leaves
// the machine. This test plants, in the same store and the same directory
// tree, exactly the things an export must never pick up — another memory
// record, a credential-shaped string, and an absolute path naming the
// operator's home directory — and asserts on the ACTUAL exported bytes.
func TestSoulExportCarriesTheSoulAndNothingElse(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)

	// A neighbouring memory record in the same tree, holding one canary.
	records := NewFileStore(f.base, f.clock)
	neighbour := validEntry()
	neighbour.Body = soulCanaries[0].value + "\n" + soulCanaries[1].value
	neighbour.Description = soulCanaries[2].value
	if err := records.Write(ctx, neighbour); err != nil {
		t.Fatalf("write neighbouring record: %v", err)
	}
	// A second canary file sitting inside the soul directory itself.
	writeFileAs(t, filepath.Join(f.base, soulDir, "notes.txt"), soulCanaries[1].value)

	f.mustEdit(t, "I am Ada. I care about correctness.")
	f.mustEdit(t, "I am Ada. I care about correctness and about being asked first.")

	export, err := f.store.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("marshal export: %v", err)
	}
	for _, c := range soulCanaries {
		if strings.Contains(string(data), c.value) {
			t.Fatalf("export leaked %s: %s", c.name, data)
		}
	}
	if !strings.Contains(string(data), "being asked first") {
		t.Fatalf("export omitted the soul it was asked for: %s", data)
	}
}

// TestSoulExportHasExactlyTheDocumentedFields pins the envelope's shape.
// A field added to SoulExport leaves the machine with every future export,
// so the field set is asserted rather than assumed.
func TestSoulExportHasExactlyTheDocumentedFields(t *testing.T) {
	f := newSoulFixture(t)
	f.mustEdit(t, "I am Ada.")
	export, err := f.store.Export(context.Background())
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	assertKeys(t, "export", top, []string{"schema_version", "exported_at", "soul", "audit_entries"})

	var soul map[string]json.RawMessage
	if err := json.Unmarshal(top["soul"], &soul); err != nil {
		t.Fatalf("unmarshal soul: %v", err)
	}
	assertKeys(t, "soul", soul, []string{"body", "schema"})

	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(top["audit_entries"], &entries); err != nil {
		t.Fatalf("unmarshal audit_entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("audit_entries has %d entries, want 1", len(entries))
	}
	assertKeys(t, "audit entry", entries[0], []string{"version", "route", "edited_at", "delta_hash"})
	if export.SchemaVersion != SoulSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", export.SchemaVersion, SoulSchemaVersion)
	}
}

// assertKeys fails unless obj's key set is exactly want.
func assertKeys(t *testing.T, what string, obj map[string]json.RawMessage, want []string) {
	t.Helper()
	if len(obj) != len(want) {
		t.Fatalf("%s has %d fields, want %d: %v", what, len(obj), len(want), keysOf(obj))
	}
	for _, k := range want {
		if _, ok := obj[k]; !ok {
			t.Fatalf("%s is missing field %q: %v", what, k, keysOf(obj))
		}
	}
}

func keysOf(obj map[string]json.RawMessage) []string {
	out := make([]string, 0, len(obj))
	for k := range obj {
		out = append(out, k)
	}
	return out
}

// TestSoulAuditEntriesCarryNoSoulText is the audit canary: a record noting
// that the SOUL changed must not contain the SOUL. The whole log ships in
// every export, so a body that leaked into an entry would leak twice.
func TestSoulAuditEntriesCarryNoSoulText(t *testing.T) {
	ctx := context.Background()
	f := newSoulFixture(t)
	const canary = "CANARY-SOUL-BODY-4d1e my deepest secret"

	f.mustEdit(t, "before "+canary)
	if err := f.store.EditViaChat(ctx, SoulDocument{Body: "after " + canary + " again"}); err != nil {
		t.Fatalf("edit via chat: %v", err)
	}
	export, err := f.store.Export(ctx)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	data, err := json.Marshal(export.AuditEntries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	for _, needle := range []string{canary, "deepest", "before", "after"} {
		if strings.Contains(string(data), needle) {
			t.Fatalf("audit log leaked %q: %s", needle, data)
		}
	}
	// The whole ledger file on disk is under the same rule: it is what the
	// export is built from.
	ledger, err := os.ReadFile(f.ledgerPath())
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if strings.Contains(string(ledger), canary) {
		t.Fatalf("ledger file leaked the soul body: %s", ledger)
	}
}

// ExampleSoulStore_Export shows what an export contains: the envelope
// version, the instant it was produced, the document, and one entry per
// recorded change in version order. The audit entries name a version, a
// route, an instant and a digest — never the text of any version of the
// document, which is why an export can be read for its history without
// re-reading everything the user has ever said about themselves.
func ExampleSoulStore_Export() {
	dir, err := os.MkdirTemp("", "cascade-soul-example-*")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	ctx := context.Background()
	clock := testkit.NewFrozenClock(fixedNow)
	var store SoulStore = NewFileSoulStore(dir, clock, nil)

	if _, err := store.Edit(ctx, SoulDocument{Body: "I am Ada.\n"}); err != nil {
		panic(err)
	}
	clock.Advance(time.Hour)
	if err := store.EditViaChat(ctx, SoulDocument{Body: "I am Ada. I like precision.\n"}); err != nil {
		panic(err)
	}

	export, err := store.Export(ctx)
	if err != nil {
		panic(err)
	}
	fmt.Println("schema version:", export.SchemaVersion)
	fmt.Println("exported at:   ", export.ExportedAt.Format(time.RFC3339))
	fmt.Printf("soul:           %q (%s)\n", export.Soul.Body, export.Soul.Schema)
	for _, e := range export.AuditEntries {
		fmt.Printf("change %d via %-14s at %s\n", e.Version, e.Route, e.EditedAt.Format(time.RFC3339))
	}
	// Output:
	// schema version: 1
	// exported at:    2026-01-02T04:04:05Z
	// soul:           "I am Ada. I like precision.\n" (cascade.soul/v1)
	// change 1 via cli            at 2026-01-02T03:04:05Z
	// change 2 via chat           at 2026-01-02T04:04:05Z
}
