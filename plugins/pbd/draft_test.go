// Package pbd (draft_test.go): cross-package proof that N/S-29.T2's
// draft-phase isolation reaches through this plugin's own authoring
// surface: a draft phase accepts a ticket edit an active phase would
// refuse (the weight/cr_level floor, internal/pews's ruleCRWeight), and a
// draft phase's tickets stay absent from a default (excluded) read even
// after that edit succeeds. Engine-level proofs of the isolation boundary
// itself live in internal/pews/draft_test.go; this file only proves the
// same boundary holds when driven through plugins/pbd's own engine calls
// (pews.Create, pews.NewStore), not a second, self-authored path.
// SPORT: plugins/pbd draft-isolation (ADD) — P1-E14-W3-S29-T2.
package pbd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// weightFloorViolatingTicketYAML mirrors cleanLintTicketYAML but sets
// weight: M with cr_level: CR-A — valid enum members individually, but a
// violation of ruleCRWeight's weight -> cr_level floor (M requires
// CR-B). An active phase's Lint refuses this; R-21.276 says a draft phase
// must not.
func weightFloorViolatingTicketYAML(id string) string {
	y := cleanLintTicketYAML(id)
	y = strings.Replace(y, "weight: S\n", "weight: M\n", 1)
	y = strings.Replace(y, "cr_level: CR-A+CR-B\n", "cr_level: CR-A\n", 1)
	return y
}

func writeDraftPhaseRecord(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "phase.yaml"), []byte("draft: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// draftAuthoringTicketID is the id every subtest below authors.
const draftAuthoringTicketID = "P1-E14-W3-S29-T1"

func TestDraftAuthoringIsolation(t *testing.T) {
	ticket, err := pews.DecodeTicket([]byte(weightFloorViolatingTicketYAML(draftAuthoringTicketID)))
	if err != nil {
		t.Fatalf("DecodeTicket (enum-valid, floor-violating): %v", err)
	}

	t.Run("a draft phase accepts the weight/cr_level floor violation an active phase refuses",
		func(t *testing.T) { testDraftAcceptsFloorViolation(t, ticket) })
	t.Run("the same draft ticket stays absent from a default (excluded) read after the edit",
		func(t *testing.T) { testDraftTicketExcludedAfterEdit(t, ticket) })
	t.Run("RunValidate (the pbd validate command's own engine call) also excludes the draft phase",
		func(t *testing.T) { testDraftRunValidateExcludes(t, ticket) })
}

func testDraftAcceptsFloorViolation(t *testing.T, ticket pews.Ticket) {
	draftRoot := t.TempDir()
	writeDraftPhaseRecord(t, draftRoot)
	if err := pews.Create(draftRoot, "P1", ticket); err != nil {
		t.Fatalf("Create into draft phase: %v", err)
	}

	activeRoot := t.TempDir()
	if err := pews.Create(activeRoot, "P1", ticket); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("Create into active phase: err = %v, want KindInvalidInput (floor violation)", err)
	}
}

func testDraftTicketExcludedAfterEdit(t *testing.T, ticket pews.Ticket) {
	draftRoot := t.TempDir()
	writeDraftPhaseRecord(t, draftRoot)
	if err := pews.Create(draftRoot, "P1", ticket); err != nil {
		t.Fatalf("Create into draft phase: %v", err)
	}

	excluded, err := pews.NewStore(draftRoot, "P1").Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(excluded.Tickets) != 0 {
		t.Errorf("excluded Tickets = %+v, want empty", excluded.Tickets)
	}
	if !excluded.Draft {
		t.Error("excluded.Draft = false, want true")
	}

	included, err := pews.NewStore(draftRoot, "P1").LoadWithOptions(pews.LoadOptions{IncludeDrafts: true})
	if err != nil {
		t.Fatalf("LoadWithOptions: %v", err)
	}
	if len(included.Tickets) != 1 || included.Tickets[0].ID != draftAuthoringTicketID {
		t.Errorf("included Tickets = %+v, want exactly %q", included.Tickets, draftAuthoringTicketID)
	}
}

func testDraftRunValidateExcludes(t *testing.T, ticket pews.Ticket) {
	draftRoot := t.TempDir()
	writeDraftPhaseRecord(t, draftRoot)
	if err := pews.Create(draftRoot, "P1", ticket); err != nil {
		t.Fatalf("Create into draft phase: %v", err)
	}
	report, err := RunValidate(draftRoot, "P1")
	if err != nil {
		t.Fatalf("RunValidate: %v", err)
	}
	if report.ActiveCount != 0 || !report.Draft {
		t.Errorf("report = %+v, want ActiveCount 0 and Draft true", report)
	}
}
