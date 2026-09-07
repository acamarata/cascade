// Purpose: draft.go's required unit tests — TestDraftPhaseIsolation proves
// the isolation boundary NEGATIVELY (a draft phase's tickets are absent
// from an active Load/Validate, never satisfy a dependency, and a
// malformed draft ticket never even reaches decode for an excluded read);
// TestDraftPhasePlatformParity proves the same behavior is pure
// filesystem/YAML logic with no platform-gated path to assert a refusal
// for (Art.5).
// SPORT: plugins/pbd/internal/pews draft-isolation (ADD) — P1-E14-W3-S29-T2.
package pews

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// writePhaseRecord writes root/phase.yaml with the given draft value.
func writePhaseRecord(t *testing.T, root string, draft bool) {
	t.Helper()
	content := "draft: false\n"
	if draft {
		content = "draft: true\n"
	}
	if err := os.WriteFile(filepath.Join(root, phaseRecordFile), []byte(content), 0o644); err != nil {
		t.Fatalf("write phase record: %v", err)
	}
}

// ticketYAMLWithWeight is minimalTicketYAML with an overridable weight,
// used to prove a draft ticket may carry a weight DecodeTicket would
// reject.
func ticketYAMLWithWeight(id, weight string) string {
	return `id: ` + id + `
title: t
short_desc: d
full_desc: d
branch: b
weight: ` + weight + `
model_class: build
depends_on: []
tasks: []
checks: []
acceptance_criteria: []
files_scope:
  add: []
  change: []
  delete: []
spec_refs: []
cr_level: CR-B
qa_level: QA-A
sport_updates: []
docs_updates: []
`
}

// mkTicketFileRaw writes raw ticket YAML content at the canonical path, so
// tests can supply content minimalTicketYAML/mkTicketFile cannot (an
// invalid weight).
func mkTicketFileRaw(t *testing.T, root, epic string, wave, sprint, ticket int, content string) {
	t.Helper()
	dir := filepath.Join(root, "epics", "E-"+epic, "waves",
		fmt.Sprintf("W-%d", wave), "sprints", fmt.Sprintf("S-%02d", sprint), "tickets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("T-%d.yaml", ticket))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write ticket: %v", err)
	}
}

// TestDraftPhaseIsolation is the ticket's own required proof: a draft
// phase's tickets must be ABSENT from an active read, must not satisfy an
// active dependency, and must not reach Validate's report — asserted
// negatively, not just "the happy path still works".
func TestDraftPhaseIsolation(t *testing.T) {
	t.Run("default Load excludes a draft phase and never decodes its tickets", testDraftExcludedByDefault)
	t.Run("Validate on the excluded tree reports ActiveCount 0, never the draft's own structural facts", testDraftExcludedValidateClean)
	t.Run("a draft ticket's id never satisfies another tree's dependency", testDraftIDNeverSatisfiesDependency)
	t.Run("IncludeDrafts loads the draft's tickets using the lenient decoder", testDraftIncludeDraftsLenientDecode)
	t.Run("an active (non-draft) phase is entirely unaffected", testDraftActivePhaseUnaffected)
	t.Run("a malformed phase.yaml is a decode error, not a silent default", testDraftMalformedPhaseRecord)
	t.Run("RequireBuildable refuses a draft and passes an active tree", testDraftRequireBuildable)
}

func testDraftExcludedByDefault(t *testing.T) {
	root := t.TempDir()
	writePhaseRecord(t, root, true)
	// An invalid weight would fail DecodeTicket outright; excluding the
	// phase must mean this file is never even opened.
	mkTicketFileRaw(t, root, "N", 3, 29, 1, ticketYAMLWithWeight("P1-E14-W3-S29-T1", "BOGUS"))

	tree, err := NewStore(root, "P1").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !tree.Draft {
		t.Error("tree.Draft = false, want true")
	}
	if len(tree.Tickets) != 0 {
		t.Errorf("Tickets = %+v, want empty (excluded)", tree.Tickets)
	}
}

func testDraftExcludedValidateClean(t *testing.T) {
	root := t.TempDir()
	writePhaseRecord(t, root, true)
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)
	mkTicketFile(t, root, "N", 3, 29, 3, "P1-E14-W3-S29-T3", nil) // a gap the draft's own tree would flag

	tree, err := NewStore(root, "P1").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	report, verr := Validate(tree)
	if verr != nil {
		t.Fatalf("Validate: %v", verr)
	}
	if !report.OK() {
		t.Errorf("violations = %+v, want none (draft excluded)", report.Violations)
	}
	if report.ActiveCount != 0 {
		t.Errorf("ActiveCount = %d, want 0", report.ActiveCount)
	}
	if !report.Draft {
		t.Error("Report.Draft = false, want true")
	}
}

func testDraftIDNeverSatisfiesDependency(t *testing.T) {
	// Two separate roots, since Store/Tree model one phase at a time: an
	// active tree naming a draft ticket's id in depends_on sees it as
	// dangling, exactly as it would see any id absent from ITS OWN tree —
	// a draft ticket is never independently resolvable.
	activeRoot := t.TempDir()
	mkTicketFile(t, activeRoot, "N", 3, 29, 1, "P1-E14-W3-S29-T1",
		[]string{"P1-E14-W3-S28-T9"}) // an id that would live in a draft phase elsewhere

	tree, err := NewStore(activeRoot, "P1").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	report, verr := Validate(tree)
	if verr == nil {
		t.Fatal("Validate: want a dangling-dependency error, got nil")
	}
	if !hasViolationKind(report.Violations, ViolationDanglingDep) {
		t.Errorf("violations = %+v, want ViolationDanglingDep", report.Violations)
	}
}

func testDraftIncludeDraftsLenientDecode(t *testing.T) {
	root := t.TempDir()
	writePhaseRecord(t, root, true)
	mkTicketFileRaw(t, root, "N", 3, 29, 1, ticketYAMLWithWeight("P1-E14-W3-S29-T1", "BOGUS"))

	tree, err := NewStore(root, "P1").LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("LoadWithOptions: %v", err)
	}
	if !tree.Draft {
		t.Error("tree.Draft = false, want true")
	}
	if len(tree.Tickets) != 1 {
		t.Fatalf("Tickets = %+v, want 1", tree.Tickets)
	}
	if tree.Tickets[0].Ticket.Weight != "BOGUS" {
		t.Errorf("Weight = %q, want the invalid value preserved verbatim", tree.Tickets[0].Ticket.Weight)
	}
}

func testDraftActivePhaseUnaffected(t *testing.T) {
	root := t.TempDir()
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)

	tree, err := NewStore(root, "P1").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if tree.Draft {
		t.Error("tree.Draft = true, want false")
	}
	if len(tree.Tickets) != 1 {
		t.Fatalf("Tickets = %+v, want 1", tree.Tickets)
	}
}

func testDraftMalformedPhaseRecord(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, phaseRecordFile), []byte("draft: [1,2]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(root, "P1").Load(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

func testDraftRequireBuildable(t *testing.T) {
	if err := RequireBuildable(&Tree{Phase: "P1", Draft: true}); !cascade.HasKind(err, cascade.KindConflict) {
		t.Errorf("draft: err = %v, want KindConflict", err)
	}
	if err := RequireBuildable(&Tree{Phase: "P1", Draft: false}); err != nil {
		t.Errorf("active: err = %v, want nil", err)
	}
	if err := RequireBuildable(nil); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("nil: err = %v, want KindInvalidInput", err)
	}
}

// TestDraftPhasePlatformParity runs the same draft/active Load branch on
// whatever platform CI schedules this on. Nothing in draft.go is
// platform-gated (no build tag, no OS-specific path) — path/filepath.Join
// and gopkg.in/yaml.v3 behave identically on macOS, Linux, and Windows —
// so there is no unsupported behavior for this suite to assert a refusal
// for; Art.5 is satisfied by running unconditionally on every CI job's
// runner rather than by a platform skip.
func TestDraftPhasePlatformParity(t *testing.T) {
	root := t.TempDir()
	writePhaseRecord(t, root, true)
	mkTicketFile(t, root, "N", 3, 29, 1, "P1-E14-W3-S29-T1", nil)

	excluded, err := NewStore(root, "P1").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(excluded.Tickets) != 0 {
		t.Errorf("excluded Tickets = %+v, want empty", excluded.Tickets)
	}

	included, err := NewStore(root, "P1").LoadWithOptions(LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("LoadWithOptions: %v", err)
	}
	if len(included.Tickets) != 1 {
		t.Errorf("included Tickets = %+v, want 1", included.Tickets)
	}
}

// hasViolationKind reports whether v contains a Violation of kind k.
func hasViolationKind(v []Violation, k ViolationKind) bool {
	for _, x := range v {
		if x.Kind == k {
			return true
		}
	}
	return false
}

// TestDecodePhase covers DecodePhase directly: default, explicit true, and
// a malformed document.
func TestDecodePhase(t *testing.T) {
	p, err := DecodePhase([]byte("draft: true\n"))
	if err != nil || !p.Draft {
		t.Errorf("DecodePhase(true) = %+v, %v", p, err)
	}
	p, err = DecodePhase([]byte(""))
	if err != nil || p.Draft {
		t.Errorf("DecodePhase(empty) = %+v, %v, want zero value", p, err)
	}
	if _, err := DecodePhase([]byte("draft: [not-a-bool]\n")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err = %v, want KindInvalidInput", err)
	}
}

// TestDecodeDraftTicket proves the lenient decoder skips weight/model
// validity but keeps every other DecodeTicket check (cr_level/qa_level,
// unknown fields, malformed YAML).
func TestDecodeDraftTicket(t *testing.T) {
	if _, err := DecodeDraftTicket([]byte(ticketYAMLWithWeight("P1-E14-W3-S29-T1", "BOGUS"))); err != nil {
		t.Errorf("invalid weight: err = %v, want nil", err)
	}
	bad := ticketYAMLWithWeight("P1-E14-W3-S29-T1", "S")
	// Corrupt cr_level: DecodeDraftTicket must still refuse this.
	bad = strings.Replace(bad, "cr_level: CR-B\n", "cr_level: NOT-A-LEVEL\n", 1)
	if _, err := DecodeDraftTicket([]byte(bad)); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("bad cr_level: err = %v, want KindInvalidInput", err)
	}
	if _, err := DecodeDraftTicket([]byte("not: [valid")); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("malformed YAML: err = %v, want KindInvalidInput", err)
	}
}

// TestFilterDraftLintIssues proves the filter drops exactly the
// CR-weight-floor kind and nothing else.
func TestFilterDraftLintIssues(t *testing.T) {
	in := []LintIssue{
		{Kind: LintKindCRWeightMismatch, TicketID: "a"},
		{Kind: LintKindFieldRequired, TicketID: "b"},
	}
	out := filterDraftLintIssues(in)
	if len(out) != 1 || out[0].TicketID != "b" {
		t.Errorf("filterDraftLintIssues = %+v, want only %q", out, "b")
	}
}
