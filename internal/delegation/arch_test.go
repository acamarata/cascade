package delegation

import (
	"os/exec"
	"strings"
	"testing"
)

// TestDelegateImportsNoJobsOrCapacityInConductor verifies architectural import boundaries.
// It ensures internal/conductor does not import internal/jobs, internal/fleet/capacity,
// or internal/delegation, and that internal/delegation imports no providers/agents/* package.
func TestDelegateImportsNoJobsOrCapacityInConductor(t *testing.T) {
	conductorImports := listImports(t, "internal/conductor")
	assertNoForbidden(t, conductorImports, "internal/conductor", []string{
		"internal/jobs",
		"internal/fleet/capacity",
		"internal/delegation",
	})

	delegationImports := listImports(t, "internal/delegation")
	for _, imp := range delegationImports {
		if strings.HasPrefix(imp, "providers/agents/") {
			t.Errorf("internal/delegation must not import %s", imp)
		}
	}
}

// listImports returns the direct imports of the given package path using go list.
func listImports(t *testing.T, pkg string) []string {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", "{{.Imports}}", pkg).Output()
	if err != nil {
		return nil
	}
	line := strings.TrimSpace(string(out))
	if line == "" || line == "[]" {
		return nil
	}
	line = strings.TrimPrefix(line, "[")
	line = strings.TrimSuffix(line, "]")
	if line == "" {
		return nil
	}
	return strings.Fields(line)
}

// assertNoForbidden checks that none of the forbidden prefixes appear in imports.
func assertNoForbidden(t *testing.T, imports []string, pkg string, forbidden []string) {
	t.Helper()
	for _, imp := range imports {
		for _, f := range forbidden {
			if imp == f || strings.HasPrefix(imp, f+"/") {
				t.Errorf("%s must not import %s", pkg, imp)
			}
		}
	}
}
