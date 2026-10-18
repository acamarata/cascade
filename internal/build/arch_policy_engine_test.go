// Package build (this file): the one-policy-engine gate. Every production
// policy.Engine is built by cmd/cascade/policy_engine.go, which wires the
// stored deny list, the operator's autonomy profile and the shared redacting
// audit log. A second policy.NewEngine call anywhere else would be an engine
// on the empty default deny list that ignores a deny row in the policy
// store, or one that audits outside the redacting log.
//
// The rule is RED against a seeded fixture and GREEN on the real tree. Test
// files are exempt: a test may build a bare engine over a fixture.
package build

import (
	"go/ast"
	"strings"
	"testing"
)

const (
	policyImportPath = cascadeModulePath + "/internal/policy"
	policyCtorFile   = "cmd/cascade/policy_engine.go"
	policyCtorFunc   = "newPolicyEngine"
)

// withoutFunc returns a shallow copy of f with the named top-level function
// removed, so a reference counted on the copy lies outside that function.
func withoutFunc(f *ast.File, name string) *ast.File {
	cp := *f
	cp.Decls = nil
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			continue
		}
		cp.Decls = append(cp.Decls, d)
	}
	return &cp
}

// policyEngineCtorViolations returns every shipping file outside
// internal/policy that names policy.NewEngine, through any import alias or as
// a function value. The constructor file is allowed only inside the function
// newPolicyEngine: a second builder in the same file would bypass the stored
// deny list and the shared audit log just as well as one in another file.
func policyEngineCtorViolations(srcs []archSource) []string {
	var bad []string
	for _, s := range srcs {
		if underDir(s.rel, "internal/policy") {
			continue
		}
		file := s.file
		if s.rel == policyCtorFile {
			file = withoutFunc(file, policyCtorFunc)
		}
		if selectorRefs(file, policyImportPath, "NewEngine") > 0 {
			bad = append(bad, s.rel)
		}
	}
	return bad
}

func TestArchPolicyEngineOnlyInConstructor(t *testing.T) {
	if bad := policyEngineCtorViolations(shippingSources(t, archModuleRoot(t))); len(bad) > 0 {
		t.Fatalf("shipping files build a policy.Engine outside the newPolicyEngine function in cmd/cascade/policy_engine.go:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func TestArchPolicyEngineDetectsViolation(t *testing.T) {
	srcs := seededSources(t, map[string]string{
		"cmd/cascade/mcp_tools.go":      "package main\nimport \"" + policyImportPath + "\"\nvar _, _ = policy.NewEngine(nil, nil, nil)\n",
		"cmd/cascade/alias.go":          "package main\nimport p \"" + policyImportPath + "\"\nvar f = p.NewEngine\n",
		"cmd/cascade/dot.go":            "package main\nimport . \"" + policyImportPath + "\"\n",
		"internal/jobs/own.go":          "package jobs\nimport \"" + policyImportPath + "\"\nvar _, _ = policy.NewEngine(nil, nil, nil)\n",
		"cmd/cascade/policy_engine.go":  "package main\nimport \"" + policyImportPath + "\"\nfunc newPolicyEngine() { _ = policy.NewEngine }\n",
		"internal/policy/engine.go":     "package policy\nimport \"" + policyImportPath + "\"\nvar _ = policy.NewEngine\n",
		"cmd/cascade/other_package.go":  "package main\nimport \"other/policy\"\nvar _ = policy.NewEngine\n",
		"cmd/cascade/uses_only_type.go": "package main\nimport \"" + policyImportPath + "\"\nvar _ *policy.Engine\n",
		"cmd/cascade/controller.go":     "package main\nimport \"" + policyImportPath + "\"\nvar _ = policy.NewController\n",
	})
	got := strings.Join(policyEngineCtorViolations(srcs), ",")
	want := "cmd/cascade/alias.go,cmd/cascade/dot.go,cmd/cascade/mcp_tools.go,internal/jobs/own.go"
	if got != want {
		t.Fatalf("policy engine constructor violations = %s, want %s", got, want)
	}
}

// A second NewEngine reference in the constructor file but outside
// newPolicyEngine is a violation, whether in a function or a package var.
func TestArchPolicyEngineDetectsViolationInConstructorFile(t *testing.T) {
	srcs := seededSources(t, map[string]string{
		policyCtorFile: "package main\nimport \"" + policyImportPath + "\"\n" +
			"func newPolicyEngine() { _ = policy.NewEngine }\n" +
			"func githubCIEngine() { _, _ = policy.NewEngine(nil, nil, nil) }\n",
	})
	if got := strings.Join(policyEngineCtorViolations(srcs), ","); got != policyCtorFile {
		t.Fatalf("constructor-file violations = %q, want %q", got, policyCtorFile)
	}
	srcs = seededSources(t, map[string]string{
		policyCtorFile: "package main\nimport \"" + policyImportPath + "\"\nvar build = policy.NewEngine\n",
	})
	if got := strings.Join(policyEngineCtorViolations(srcs), ","); got != policyCtorFile {
		t.Fatalf("package-var violation = %q, want %q", got, policyCtorFile)
	}
}
