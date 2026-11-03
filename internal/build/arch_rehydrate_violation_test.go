package build

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestArchRehydrateCallersDetectsViolation: the allowlist admits the one
// FILE cmd/cascade/hooks_rehydrate.go, so a sibling file in cmd/cascade that
// reaches RehydrateOnly is a finding while the allowlisted file is not.
func TestArchRehydrateCallersDetectsViolation(t *testing.T) {
	call := "func %s(r *secrets.Rehydrator) { _, _ = r.RehydrateOnly(context.Background(), nil, nil) }\n"
	got := seededReferences(t, map[string]string{
		"cmd/cascade/hooks_rehydrate.go": fmt.Sprintf(fixtureHead, "main") + fmt.Sprintf(call, "allowed"),
		"cmd/cascade/hooks_sibling.go":   fmt.Sprintf(fixtureHead, "main") + fmt.Sprintf(call, "sibling"),
		"cmd/cascade/main.go":            "package main\n\nfunc main() {}\n",
	})
	want := []string{"cmd/cascade/hooks_sibling.go:11: selects RehydrateOnly"}
	if !slices.Equal(got, want) {
		t.Fatalf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
