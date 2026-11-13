package build

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/inboxenv"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestModelCeilingMatchesEgressModelClasses pins admission to the egress table.
func TestModelCeilingMatchesEgressModelClasses(t *testing.T) {
	for _, class := range []egress.EgressClass{egress.EgressClassMCP, egress.EgressClassHook} {
		cfg, ok := egress.DefaultRegistry().Lookup(class)
		if !ok || !cfg.Enabled {
			t.Fatalf("%s is missing or disabled", class)
		}
		for _, tier := range []provider.SensitivityTier{
			provider.SensitivityPublic, provider.SensitivityInternal,
			provider.SensitivityRestricted, provider.SensitivityLocalOnly, provider.SensitivityTier(255),
		} {
			admitted := egress.SensitivityPass(class, cfg, tier) == nil
			if admitted && (tier == provider.SensitivityRestricted || tier == provider.SensitivityLocalOnly) {
				t.Fatalf("escalate: %s admits %s", class, tier)
			}
			if got := inboxenv.ModelAdmits(tier); got != admitted {
				t.Errorf("%s/%s: ModelAdmits = %v, egress = %v", class, tier, got, admitted)
			}
		}
	}
}

// TestArchInboxNeverReachesApprovalOrGate checks production imports without
// loading dependencies, and proves the scan rejects seeded forbidden imports.
func TestArchInboxNeverReachesApprovalOrGate(t *testing.T) {
	violations, err := inboxGateImports(archModuleRoot(t))
	if err != nil || len(violations) != 0 {
		t.Fatalf("inbox authority boundary: %v: %v", violations, err)
	}
	for _, dir := range []string{"internal/notify", "plugins/claude", "pkg/inboxenv"} {
		for _, target := range []string{"internal/policy", "internal/jobs", "internal/fleet/hookpacks", "internal/notify"} {
			if target == "internal/notify" && dir != "pkg/inboxenv" {
				continue
			}
			t.Run(dir+"/"+target, func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, dir, "inbox_seed.go")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				body := "package seed\nimport _ \"github.com/acamarata/cascade/" + target + "\"\n"
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				got, err := inboxGateImports(root)
				if err != nil || len(got) != 1 {
					t.Fatalf("seeded import escaped: %v, %v", got, err)
				}
			})
		}
	}
}

// inboxGateImports includes every platform's production files, independent of
// build tags, so a gated import cannot evade the authority boundary.
func inboxGateImports(root string) ([]string, error) {
	var violations []string
	for _, dir := range []string{"internal/notify", "plugins/claude", "pkg/inboxenv"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				return nil
			}
			if dir == "plugins/claude" && !strings.HasPrefix(name, "inbox_") {
				return nil
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, spec := range f.Imports {
				imp, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					return err
				}
				if inboxGateForbidden(dir, imp) {
					violations = append(violations, fmt.Sprintf("%s imports %s", path, imp))
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return violations, nil
}

func inboxGateForbidden(dir, imp string) bool {
	const prefix = "github.com/acamarata/cascade/"
	if dir == "pkg/inboxenv" && strings.HasPrefix(imp, prefix+"internal/") {
		return true
	}
	for _, banned := range []string{"internal/policy", "internal/jobs", "internal/fleet/hookpacks"} {
		if imp == prefix+banned || strings.HasPrefix(imp, prefix+banned+"/") {
			return true
		}
	}
	return false
}
