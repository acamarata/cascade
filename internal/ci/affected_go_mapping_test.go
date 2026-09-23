package ci

import "testing"

// TestChangedPathForcesFull_ModuleFiles proves go.mod/go.sum/go.work(.sum)
// and vendor/ paths are recognised by their literal filename/prefix,
// independent of any go list subprocess (D1).
func TestChangedPathForcesFull_ModuleFiles(t *testing.T) {
	forces := []string{
		"go.mod", "go.sum", "go.work", "go.work.sum",
		"vendor", "vendor/modules.txt", "vendor/example.com/pkg/x.go",
	}
	for _, p := range forces {
		if !changedPathForcesFull(p) {
			t.Errorf("changedPathForcesFull(%q) = false, want true", p)
		}
	}
	notForced := []string{"pkg/a/a.go", "README.md", "internal/ci/vendorish.go"}
	for _, p := range notForced {
		if changedPathForcesFull(p) {
			t.Errorf("changedPathForcesFull(%q) = true, want false", p)
		}
	}
}

// TestUnderTestdata proves the walk-up predicate matches a directory
// literally named "testdata" at any depth, and nothing else.
func TestUnderTestdata(t *testing.T) {
	under := []string{"testdata", "pkg/a/testdata", "pkg/a/testdata/sub/deep"}
	for _, d := range under {
		if !underTestdata(d) {
			t.Errorf("underTestdata(%q) = false, want true", d)
		}
	}
	notUnder := []string{".", "pkg/a", "pkg/testdataish"}
	for _, d := range notUnder {
		if underTestdata(d) {
			t.Errorf("underTestdata(%q) = true, want false", d)
		}
	}
}
