package jobs

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

type assignmentPolicy struct{ require, hold bool }

func (p assignmentPolicy) RequiresDistinctFamily() bool     { return p.require }
func (p assignmentPolicy) HoldsWithoutDistinctFamily() bool { return p.hold }

func assignmentPolicies() []struct {
	name     string
	policy   ReviewPolicySource
	degraded bool
	err      error
} {
	return []struct {
		name     string
		policy   ReviewPolicySource
		degraded bool
		err      error
	}{
		{"low", assignmentPolicy{}, false, nil},
		{"normal", assignmentPolicy{require: true}, true, nil},
		{"high", assignmentPolicy{true, true}, false, ErrNoDistinctFamily},
		{"critical", assignmentPolicy{true, true}, false, ErrNoDistinctFamily},
		{"missing policy", nil, false, ErrNoDistinctFamily},
	}
}

func checkAssignment(t *testing.T, degraded bool, err error, wantDegraded bool, wantErr error) {
	t.Helper()
	if degraded != wantDegraded || err != wantErr {
		t.Fatalf("got degraded=%v, err=%v; want degraded=%v, err=%v", degraded, err, wantDegraded, wantErr)
	}
	if err == ErrNoDistinctFamily {
		if !cascade.HasKind(err, cascade.KindElevationRequired) || err.Error() != "elevation-required: jobs: no distinct reviewer family" {
			t.Fatalf("unexpected family sentinel: %v", err)
		}
	}
}

func TestRequireDistinctFamilyLadder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	author := FamilyRef{Family: "vendor-a", Resolved: true}
	for _, policy := range assignmentPolicies() {
		t.Run(policy.name, func(t *testing.T) {
			for _, candidates := range [][]FamilyRef{
				nil, {author}, {author, author},
				{{Family: "vendor-b"}}, {{Resolved: true}},
			} {
				degraded, err := RequireDistinctFamily(policy.policy, author, candidates)
				checkAssignment(t, degraded, err, policy.degraded, policy.err)
			}
			degraded, err := RequireDistinctFamily(policy.policy, author, []FamilyRef{author, {Family: "vendor-b", Resolved: true}})
			if policy.policy != nil {
				checkAssignment(t, degraded, err, false, nil)
			} else {
				checkAssignment(t, degraded, err, false, ErrNoDistinctFamily)
			}
			degraded, err = RequireDistinctFamily(policy.policy, FamilyRef{Family: "vendor-a"}, []FamilyRef{{Family: "vendor-b", Resolved: true}})
			checkAssignment(t, degraded, err, policy.degraded, policy.err)
		})
	}
}

func TestVerifyObservedFamily(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	author := FamilyRef{Family: "vendor-a", Resolved: true}
	for _, policy := range assignmentPolicies() {
		t.Run(policy.name, func(t *testing.T) {
			for _, observed := range []FamilyRef{author, {}, {Family: "vendor-b"}, {Resolved: true}} {
				degraded, err := VerifyObservedFamily(policy.policy, author, observed)
				checkAssignment(t, degraded, err, policy.degraded, policy.err)
			}
			different := FamilyRef{Family: "vendor-b", Resolved: true}
			degraded, err := VerifyObservedFamily(policy.policy, author, different)
			if policy.policy != nil {
				checkAssignment(t, degraded, err, false, nil)
			} else {
				checkAssignment(t, degraded, err, false, ErrNoDistinctFamily)
			}
			degraded, err = VerifyObservedFamily(policy.policy, FamilyRef{Family: "vendor-a"}, different)
			checkAssignment(t, degraded, err, policy.degraded, policy.err)
		})
	}
}

func TestHumanApprovalRequiredIsElevation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if ErrHumanApprovalRequired == nil || ErrHumanApprovalRequired == ErrNoDistinctFamily {
		t.Fatal("approval sentinel must have its own identity")
	}
	if !cascade.HasKind(ErrHumanApprovalRequired, cascade.KindElevationRequired) {
		t.Fatalf("wrong kind: %v", ErrHumanApprovalRequired)
	}
	if got := ErrHumanApprovalRequired.Error(); got != "elevation-required: jobs: human approval required" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestJobsImportsNoReview(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no Go files found in the jobs package")
	}
	fset := token.NewFileSet()
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "github.com/acamarata/cascade/internal/review" {
				t.Errorf("%s imports internal/review", name)
			}
		}
	}
}
