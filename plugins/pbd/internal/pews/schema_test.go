package pews

import (
	"reflect"
	"regexp"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// seedPath is the package-local fuzz corpus entry required by 06 §5 rule 7
// and R-21.266. Go's testing tool auto-loads every file under
// testdata/fuzz/<FuzzName>/ as a native corpus entry (the "go test fuzz v1"
// format), so the file at seedPath holds the corpus-encoded form of
// seedTicketDoc below, not readable YAML; seedTicketDoc is the single
// source both the tests here and FuzzTicketYAML's f.Add seed use.
const seedPath = "testdata/fuzz/FuzzTicketYAML/seed_ticket.yaml"

// seedTicketDoc is the synthetic seed ticket, verbatim. It matches the
// decoded payload the corpus-encoded seedPath file carries; keep the two in
// sync if either changes.
const seedTicketDoc = `id: P1-E00-W0-S00-T0
title: Synthetic fuzz seed ticket
short_desc: A synthetic ticket used only to seed FuzzTicketYAML.
full_desc: |
  This is a synthetic ticket used only to seed the fuzzer for the PEWS
  ticket-schema v2 decoder. It is not a real ticket and describes no real
  work.
branch: P1-E00-W0-S00-T0-Synthetic-Fuzz-Seed-Ticket
weight: M
model_class: build
depends_on:
- P1-E00-W0-S00-T-DOES-NOT-EXIST
tasks:
- First synthetic task waypoint
- Second synthetic task waypoint, spanning a folded continuation
  line to exercise multi-line plain scalars
checks:
- go test ./example/...
acceptance_criteria:
- Every listed check is green
files_scope:
  add:
  - example/example.go
  change:
  - example/other.go
  delete: []
spec_refs:
- 06-FORGE-SPEC.md §1 (synthetic reference)
cr_level: CR-A+CR-B
qa_level: QA-A
sport_updates:
- 'placeholder: example/synthetic (ADD)'
docs_updates:
- example/README.md
subtickets:
- P1-E00-W0-S00-T0a
journals: true
owner_prereq: none
gate_only: false
external_contract: false
`

// wantNormativeOrder is 06-FORGE-SPEC.md §1's 17 fields, in normative
// order. It is written independently of schema.go's own normativeFields
// var so the test checks the struct against the spec, not against a second
// copy of itself.
var wantNormativeOrder = []string{
	"id", "title", "short_desc", "full_desc", "branch", "weight",
	"model_class", "depends_on", "tasks", "checks", "acceptance_criteria",
	"files_scope", "spec_refs", "cr_level", "qa_level", "sport_updates",
	"docs_updates",
}

// wantExtraOrder is 06 §1's six declared extra flags, in the order it
// lists them.
var wantExtraOrder = []string{
	"subtickets", "journals", "owner_prereq", "gate_only", "external_contract",
	"amendment_note",
}

// yamlTagName strips a struct tag's trailing ",omitempty" (etc.) leaving
// the bare field name.
func yamlTagName(tag reflect.StructTag) string {
	v := tag.Get("yaml")
	for i, c := range v {
		if c == ',' {
			return v[:i]
		}
	}
	return v
}

// TestTicketSchema asserts Ticket's field declaration order matches the 17
// normative fields followed by exactly the 6 declared extra flags, and no
// others.
func TestTicketSchema(t *testing.T) {
	rt := reflect.TypeOf(Ticket{})
	want := append(append([]string{}, wantNormativeOrder...), wantExtraOrder...)
	if rt.NumField() != len(want) {
		t.Fatalf("Ticket has %d fields, want exactly %d (17 normative + 6 extra)", rt.NumField(), len(want))
	}
	for i, name := range want {
		got := yamlTagName(rt.Field(i).Tag)
		if got != name {
			t.Errorf("field %d: yaml tag %q, want %q", i, got, name)
		}
	}
}

// fullTicket builds a Ticket with every field, including every extra flag,
// populated with a distinct value.
func fullTicket() Ticket {
	journals, gateOnly, external := true, false, true
	prereq := "second machine"
	amendment := "AMENDED per R-00.0 (T0, synthetic). Example amendment note."
	return Ticket{
		ID: "P1-E00-W0-S00-T1", Title: "Example", ShortDesc: "One sentence.",
		FullDesc: "Longer\ndescription.\n", Branch: "P1-E00-W0-S00-T1-Example",
		Weight: WeightM, ModelClass: ModelClassBuild,
		DependsOn:          []string{"P1-E00-W0-S00-T0"},
		Tasks:              []string{"first task", "second task"},
		Checks:             []string{"go build ./..."},
		AcceptanceCriteria: []string{"every check is green"},
		FilesScope: FilesScope{
			Add: []string{"a.go"}, Change: []string{"b.go"}, Delete: []string{"c.go"},
		},
		SpecRefs:     []string{"06-FORGE-SPEC.md §1"},
		CRLevel:      CRLevelA + "+" + CRLevelB,
		QALevel:      QALevelA,
		SportUpdates: []string{"placeholder: example (ADD)"},
		DocsUpdates:  []string{".github/wiki/Example.md"},
		Subtickets:   []string{"P1-E00-W0-S00-T1a"},
		Journals:     &journals, OwnerPrereq: &prereq, GateOnly: &gateOnly, ExternalContract: &external,
		AmendmentNote: &amendment,
	}
}

// TestTicketYAMLFieldOrder asserts EncodeTicket emits top-level keys in 06
// §1's normative order followed by the extra flags in their declared order.
func TestTicketYAMLFieldOrder(t *testing.T) {
	out, err := EncodeTicket(fullTicket())
	if err != nil {
		t.Fatalf("EncodeTicket: %v", err)
	}
	keyRE := regexp.MustCompile(`(?m)^([a-z_]+):`)
	matches := keyRE.FindAllStringSubmatch(string(out), -1)
	var got []string
	for _, m := range matches {
		got = append(got, m[1])
	}
	want := append(append([]string{}, wantNormativeOrder...), wantExtraOrder...)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("key order = %v, want %v", got, want)
	}
}

// TestTicketYAMLRoundTrip decodes the seed ticket, re-encodes it, decodes
// the result again, and asserts the two decodes agree field for field,
// including ordered-list order and omitted-flag semantics. It also checks
// that an explicit false is distinguishable from an omitted flag.
func TestTicketYAMLRoundTrip(t *testing.T) {
	first, err := DecodeTicket([]byte(seedTicketDoc))
	if err != nil {
		t.Fatalf("DecodeTicket(seed): %v", err)
	}
	encoded, err := EncodeTicket(first)
	if err != nil {
		t.Fatalf("EncodeTicket: %v", err)
	}
	second, err := DecodeTicket(encoded)
	if err != nil {
		t.Fatalf("DecodeTicket(re-encoded): %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("round trip mismatch:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	if len(first.Tasks) < 2 || first.Tasks[0] != "First synthetic task waypoint" {
		t.Fatalf("Tasks order not preserved: %v", first.Tasks)
	}

	t.Run("OmittedFlagsStayNil", testOmittedFlagsStayNil)
	t.Run("ExplicitFalseIsNotOmitted", testExplicitFalseIsNotOmitted)
}

// minimalTicketDoc is a ticket document with only the 17 normative fields;
// every extra flag is omitted.
const minimalTicketDoc = `id: x
title: x
short_desc: x
full_desc: x
branch: x
weight: S
model_class: mech
depends_on: []
tasks: [x]
checks: [x]
acceptance_criteria: [x]
files_scope: {add: [], change: [], delete: []}
spec_refs: [x]
cr_level: CR-A
qa_level: QA-A
sport_updates: [x]
docs_updates: [x]
`

// testOmittedFlagsStayNil asserts every omitted extra flag decodes to nil
// and that re-encoding never introduces the omitted keys.
func testOmittedFlagsStayNil(t *testing.T) {
	tk, err := DecodeTicket([]byte(minimalTicketDoc))
	if err != nil {
		t.Fatalf("DecodeTicket(minimal): %v", err)
	}
	if tk.Journals != nil || tk.OwnerPrereq != nil || tk.GateOnly != nil || tk.ExternalContract != nil || tk.Subtickets != nil || tk.AmendmentNote != nil {
		t.Fatalf("expected all extra flags omitted (nil), got %+v", tk)
	}
	out, err := EncodeTicket(tk)
	if err != nil {
		t.Fatalf("EncodeTicket(minimal): %v", err)
	}
	for _, key := range wantExtraOrder {
		if regexp.MustCompile(`(?m)^` + key + `:`).Match(out) {
			t.Errorf("encoded output unexpectedly contains omitted key %q", key)
		}
	}
}

// testExplicitFalseIsNotOmitted asserts an explicit "journals: false"
// decodes to a non-nil pointer, distinguishing it from omission.
func testExplicitFalseIsNotOmitted(t *testing.T) {
	tk, err := DecodeTicket([]byte(minimalTicketDoc + "journals: false\n"))
	if err != nil {
		t.Fatalf("DecodeTicket(withFalse): %v", err)
	}
	if tk.Journals == nil || *tk.Journals != false {
		t.Fatalf("expected explicit journals:false to decode as non-nil false, got %+v", tk.Journals)
	}
}

// TestTicketYAMLErrors asserts every malformed-input case returns a
// cascade.KindInvalidInput error and never panics.
func TestTicketYAMLErrors(t *testing.T) {
	base := seedTicketDoc
	cases := map[string]string{
		"empty document":       "",
		"non-mapping root":     "- 1\n- 2\n",
		"unknown top field":    base + "nonexistent_field: x\n",
		"unknown files_scope":  replaceLine(base, "  add:\n", "  rename:\n"),
		"invalid weight":       replaceLine(base, "weight: M\n", "weight: ZZ\n"),
		"invalid model_class":  replaceLine(base, "model_class: build\n", "model_class: bogus\n"),
		"invalid cr_level":     replaceLine(base, "cr_level: CR-A+CR-B\n", "cr_level: CR-B+CR-A\n"),
		"invalid qa_level":     replaceLine(base, "qa_level: QA-A\n", "qa_level: QA-Z\n"),
		"missing required key": removeLine(base, "qa_level: QA-A\n"),
		"tab-broken syntax":    "id: x\n\ttitle: x\n",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeTicket([]byte(doc))
			if err == nil {
				t.Fatalf("DecodeTicket(%q): expected an error, got nil", name)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
				t.Fatalf("DecodeTicket(%q): kind = %v (ok=%v), want KindInvalidInput", name, kind, ok)
			}
		})
	}
}

func replaceLine(doc, from, to string) string {
	return regexp.MustCompile(regexp.QuoteMeta(from)).ReplaceAllString(doc, to)
}

func removeLine(doc, line string) string {
	return regexp.MustCompile(regexp.QuoteMeta(line)).ReplaceAllString(doc, "")
}

// FuzzTicketYAML fuzzes DecodeTicket. Its seed corpus comes only from
// testdata/fuzz/FuzzTicketYAML/ (R-21.266): the package-local seed ticket
// (present on disk as the native corpus-encoded file at seedPath, and added
// here from seedTicketDoc so the seed survives even if the corpus file is
// ever regenerated) plus a small set of adversarial literals added here.
// The decoder must never panic, whatever bytes it is handed.
func FuzzTicketYAML(f *testing.F) {
	f.Add([]byte(seedTicketDoc))
	f.Add([]byte(""))
	f.Add([]byte("not: [valid"))
	f.Add([]byte("- a\n- b\n"))
	f.Add([]byte("id: x\n\ttitle: x\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if _, derr := DecodeTicket(data); derr != nil {
			if _, ok := cascade.KindOf(derr); !ok {
				t.Fatalf("DecodeTicket returned a non-taxonomy error: %v", derr)
			}
		}
	})
}
