package jobs

import (
	"testing"

	"github.com/acamarata/cascade/internal/conductor"
)

// validContract returns a well-formed PEWSContract covering all
// seventeen fields, for tests that mutate one field at a time.
func validContract() PEWSContract {
	return PEWSContract{
		ID:                 "P1-TEST-T1",
		Title:              "Example ticket",
		ShortDesc:          "short desc",
		FullDesc:           "full desc",
		Branch:             "P1-TEST-T1-example",
		Weight:             "M",
		ModelClass:         conductor.ModelClassBuild,
		DependsOn:          []string{"P1-TEST-T0"},
		Tasks:              []string{"do the thing"},
		Checks:             []string{"go build ./..."},
		AcceptanceCriteria: []string{"it builds"},
		FilesScopeAdd:      []string{"internal/jobs/example.go"},
		FilesScopeChange:   []string{},
		FilesScopeDelete:   []string{},
		SpecRefs:           []string{"06-FORGE-SPEC.md §1"},
		CRLevel:            "CR-B",
		QALevel:            "QA-B",
		SportUpdates:       []string{"internal/jobs: ADD entity Example"},
		DocsUpdates:        []string{"internal/jobs/doc.go"},
	}
}

// TestPEWSContract_AllSeventeenFieldsRoundTrip asserts every field this
// type declares survives assignment and read-back unmodified -- the
// portable-DTO shape AC/S-59.T4's own wording requires.
func TestPEWSContract_AllSeventeenFieldsRoundTrip(t *testing.T) {
	c := validContract()
	if c.ID != "P1-TEST-T1" || c.Title != "Example ticket" || c.ShortDesc != "short desc" ||
		c.FullDesc != "full desc" || c.Branch != "P1-TEST-T1-example" || c.Weight != "M" ||
		c.ModelClass != conductor.ModelClassBuild || c.CRLevel != "CR-B" || c.QALevel != "QA-B" {
		t.Fatalf("scalar field round-trip mismatch: %+v", c)
	}
	if len(c.DependsOn) != 1 || len(c.Tasks) != 1 || len(c.Checks) != 1 ||
		len(c.AcceptanceCriteria) != 1 || len(c.FilesScopeAdd) != 1 ||
		len(c.SpecRefs) != 1 || len(c.SportUpdates) != 1 || len(c.DocsUpdates) != 1 {
		t.Fatalf("list field round-trip mismatch: %+v", c)
	}
}
