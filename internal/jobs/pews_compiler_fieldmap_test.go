package jobs

import (
	"errors"
	"testing"
)

// TestPEWSFieldMap_AllRowsCovered exercises the seventeen named §1a
// mapping functions directly, one case per row, so no row's target is
// left untested.
func TestPEWSFieldMap_AllRowsCovered(t *testing.T) {
	c := validContract()

	if got := mapIDToTicketID(c); got != c.ID {
		t.Errorf("mapIDToTicketID = %q, want %q", got, c.ID)
	}
	if got := mapTitleToJobName(c); got != c.Title {
		t.Errorf("mapTitleToJobName = %q, want %q", got, c.Title)
	}
	if got := mapShortDescToMetadata(c); got != c.ShortDesc {
		t.Errorf("mapShortDescToMetadata = %q, want %q", got, c.ShortDesc)
	}
	if got := mapFullDescToMetadata(c); got != c.FullDesc {
		t.Errorf("mapFullDescToMetadata = %q, want %q", got, c.FullDesc)
	}
	if got := mapBranchToWorktreeBase(c); got != c.Branch {
		t.Errorf("mapBranchToWorktreeBase = %q, want %q", got, c.Branch)
	}
	if got, err := mapWeightToCostCeiling(c.Weight); err != nil || got != 3 {
		t.Errorf("mapWeightToCostCeiling(M) = (%v, %v), want (3, nil)", got, err)
	}
	if _, err := mapWeightToCostCeiling("bogus"); err == nil {
		t.Error("mapWeightToCostCeiling(bogus): want error, got nil")
	}
	if got, err := mapModelClassToTaskClass(c); err != nil || got == "" {
		t.Errorf("mapModelClassToTaskClass = (%v, %v), want (non-empty, nil)", got, err)
	}
	if got := mapDependsOnToDAGEdges(c); len(got) != 1 || got[0] != "P1-TEST-T0" {
		t.Errorf("mapDependsOnToDAGEdges = %v, want [P1-TEST-T0]", got)
	}
	if got := mapTasksToMetadata(c); len(got) != 1 {
		t.Errorf("mapTasksToMetadata = %v, want 1 item", got)
	}
	if got := mapChecksToVerificationJobs(c); len(got) != 1 || got[0].Command != "go build ./..." {
		t.Errorf("mapChecksToVerificationJobs = %v, want one job for %q", got, c.Checks[0])
	}
	if got := mapAcceptanceCriteriaToEvidenceRequirements(c); len(got) != 1 {
		t.Errorf("mapAcceptanceCriteriaToEvidenceRequirements = %v, want 1 item", got)
	}
	if got := mapSpecRefsToMetadata(c); len(got) != 1 {
		t.Errorf("mapSpecRefsToMetadata = %v, want 1 item", got)
	}
	if got := mapSportUpdatesToIntegrateJob(c); len(got) != 1 {
		t.Errorf("mapSportUpdatesToIntegrateJob = %v, want 1 item", got)
	}
	if got := mapDocsUpdatesToIntegrateJob(c); len(got) != 1 {
		t.Errorf("mapDocsUpdatesToIntegrateJob = %v, want 1 item", got)
	}
}

// TestMapFilesScopeToFootprint_UnionOrderingAndDedup asserts row 12's
// ADD, CHANGE, DELETE ordering and de-duplication.
func TestMapFilesScopeToFootprint_UnionOrderingAndDedup(t *testing.T) {
	c := PEWSContract{
		FilesScopeAdd:    []string{"a.go", "shared.go"},
		FilesScopeChange: []string{"b.go", "shared.go"},
		FilesScopeDelete: []string{"c.go"},
	}
	got := mapFilesScopeToFootprint(c)
	want := []string{"a.go", "shared.go", "b.go", "c.go"}
	if len(got) != len(want) {
		t.Fatalf("mapFilesScopeToFootprint = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mapFilesScopeToFootprint[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestValidateContractFields_Valid asserts a fully populated contract
// passes.
func TestValidateContractFields_Valid(t *testing.T) {
	if err := ValidateContractFields(validContract()); err != nil {
		t.Fatalf("ValidateContractFields(valid) = %v, want nil", err)
	}
}

// TestValidateContractFields_MissingOrUnparseableEachField asserts a
// missing/unparseable case for every one of the seventeen fields
// returns the correctly typed refusal.
func TestValidateContractFields_MissingOrUnparseableEachField(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*PEWSContract)
		wantErr func(error) bool
	}{
		{"empty id", func(c *PEWSContract) { c.ID = "" }, func(e error) bool { return errors.Is(e, ErrEmptyTicketID) }},
		{"unknown model_class", func(c *PEWSContract) { c.ModelClass = "bogus" }, isType[*ErrUnknownModelClass]},
		{"empty model_class", func(c *PEWSContract) { c.ModelClass = "" }, isType[*ErrUnknownModelClass]},
		{"unknown cr_level", func(c *PEWSContract) { c.CRLevel = "CR-A" }, isType[*ErrUnknownReviewLevel]},
		{"empty cr_level", func(c *PEWSContract) { c.CRLevel = "" }, isType[*ErrUnknownReviewLevel]},
		{"unknown qa_level", func(c *PEWSContract) { c.QALevel = "QA-D" }, isType[*ErrUnknownReviewLevel]},
		{"missing title", func(c *PEWSContract) { c.Title = "" }, isType[*ErrMissingContractField]},
		{"missing short_desc", func(c *PEWSContract) { c.ShortDesc = "" }, isType[*ErrMissingContractField]},
		{"missing full_desc", func(c *PEWSContract) { c.FullDesc = "" }, isType[*ErrMissingContractField]},
		{"missing branch", func(c *PEWSContract) { c.Branch = "" }, isType[*ErrMissingContractField]},
		{"missing weight", func(c *PEWSContract) { c.Weight = "" }, isType[*ErrMissingContractField]},
		{"unparseable weight", func(c *PEWSContract) { c.Weight = "XXL" }, isType[*ErrUnparseableContractField]},
		{"missing depends_on", func(c *PEWSContract) { c.DependsOn = nil }, isType[*ErrMissingContractField]},
		{"missing tasks", func(c *PEWSContract) { c.Tasks = nil }, isType[*ErrMissingContractField]},
		{"missing checks", func(c *PEWSContract) { c.Checks = nil }, isType[*ErrMissingContractField]},
		{"missing acceptance_criteria", func(c *PEWSContract) { c.AcceptanceCriteria = nil }, isType[*ErrMissingContractField]},
		{"missing spec_refs", func(c *PEWSContract) { c.SpecRefs = nil }, isType[*ErrMissingContractField]},
		{"missing sport_updates", func(c *PEWSContract) { c.SportUpdates = nil }, isType[*ErrMissingContractField]},
		{"missing docs_updates", func(c *PEWSContract) { c.DocsUpdates = nil }, isType[*ErrMissingContractField]},
		{"missing files_scope", func(c *PEWSContract) {
			c.FilesScopeAdd, c.FilesScopeChange, c.FilesScopeDelete = nil, nil, nil
		}, isType[*ErrMissingContractField]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validContract()
			tc.mutate(&c)
			err := ValidateContractFields(c)
			if err == nil {
				t.Fatal("ValidateContractFields = nil, want a typed refusal")
			}
			if !tc.wantErr(err) {
				t.Errorf("ValidateContractFields returned wrong error type: %v (%T)", err, err)
			}
		})
	}
}

// isType returns a predicate asserting err's concrete type is T.
func isType[T error](err error) bool {
	_, ok := err.(T)
	return ok
}
