// Package build (this file): the one-firewall, one-detector gates. Each
// rule is RED against a seeded fixture and GREEN on the real tree:
//
//  1. no shipping file outside internal/secrets and internal/build names
//     secrets.DefaultDetectionConfig, through any import alias or as a
//     function value: every scan site uses secrets.ProcessDetector, so the
//     operator's [secrets] settings cannot be bypassed by a local copy.
//  2. no shipping file outside cmd/cascade/egress_firewall.go and
//     internal/hooks/egress names egress.NewEngine: every package takes its
//     engine from egress.ProcessEngine or an injected factory.
//  3. no shipping type outside internal/secrets implements egress.Vault (a
//     List(ctx) ([]string, error) and Get(ctx, name) ([]byte, error) method
//     pair), whatever its underlying type (struct, map, ...), and no
//     shipping package-level variable outside internal/secrets is declared
//     as an egress.Vault. The pair is matched per package directory, so the
//     halves may not hide in two files, and the context parameter through
//     any import name or alias. The retired stand-ins are refused by name.
//  4. no shipping file outside internal/secrets and internal/build names
//     secrets.NewDetector or secrets.NewScrubber: a hand-built
//     DetectionConfig must not bypass the process detector either.
//
// Test files are exempt: a test that drives an engine over a fixture vault
// is not a shipped value source. The go/ast helpers are in
// arch_detector_ast_test.go.
package build

import (
	"go/ast"
	"go/token"
	"sort"
	"strings"
	"testing"
)

const (
	secretsImportPath = cascadeModulePath + "/internal/secrets"
	egressImportPath  = cascadeModulePath + "/internal/hooks/egress"
)

// retiredEmptyVaults are the stand-in value sources rule 3 retired.
var retiredEmptyVaults = map[string]bool{
	"emptyEgressVault": true, "nselfEmptyVault": true, "dispatchRemoteEmptyVault": true,
}

// defaultDetectorViolations applies rule 1.
func defaultDetectorViolations(srcs []archSource) []string {
	var bad []string
	for _, s := range srcs {
		if underDir(s.rel, "internal/secrets") || underDir(s.rel, "internal/build") {
			continue
		}
		if selectorRefs(s.file, secretsImportPath, "DefaultDetectionConfig") > 0 {
			bad = append(bad, s.rel)
		}
	}
	return bad
}

// newEngineViolations applies rule 2.
func newEngineViolations(srcs []archSource) []string {
	var bad []string
	for _, s := range srcs {
		if s.rel == "cmd/cascade/egress_firewall.go" || underDir(s.rel, "internal/hooks/egress") {
			continue
		}
		if selectorRefs(s.file, egressImportPath, "NewEngine") > 0 {
			bad = append(bad, s.rel)
		}
	}
	return bad
}

// vaultViolations applies rule 3.
func vaultViolations(srcs []archSource) []string {
	var outside []archSource
	var out []string
	for _, s := range srcs {
		if underDir(s.rel, "internal/secrets") {
			continue
		}
		outside = append(outside, s)
		for _, name := range declaredNames(s.file) {
			if retiredEmptyVaults[name] {
				out = append(out, s.rel+": declares "+name)
			}
		}
		if !underDir(s.rel, "cmd/cascade") {
			for _, v := range packageVaultVars(s.file) {
				out = append(out, s.rel+": package-level egress.Vault "+v)
			}
		}
	}
	out = append(out, vaultImplementers(outside)...)
	sort.Strings(out)
	return out
}

// detectorCtorViolations applies rule 4.
func detectorCtorViolations(srcs []archSource) []string {
	var bad []string
	for _, s := range srcs {
		if underDir(s.rel, "internal/secrets") || underDir(s.rel, "internal/build") {
			continue
		}
		if selectorRefs(s.file, secretsImportPath, "NewDetector")+selectorRefs(s.file, secretsImportPath, "NewScrubber") > 0 {
			bad = append(bad, s.rel)
		}
	}
	return bad
}

// declaredNames returns every top-level type name f declares.
func declaredNames(f *ast.File) []string {
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				out = append(out, ts.Name.Name)
			}
		}
	}
	return out
}

// packageVaultVars returns the package-level variables f declares with the
// type egress.Vault.
func packageVaultVars(f *ast.File) []string {
	local := map[string]bool{}
	for _, name := range importNames(f, egressImportPath) {
		local[name] = true
	}
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil {
				continue
			}
			sel, ok := vs.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Vault" {
				continue
			}
			if id, ok := sel.X.(*ast.Ident); ok && local[id.Name] {
				for _, n := range vs.Names {
					out = append(out, n.Name)
				}
			}
		}
	}
	return out
}

func TestArchNoDefaultDetectorOutsideSecrets(t *testing.T) {
	if bad := defaultDetectorViolations(shippingSources(t, archModuleRoot(t))); len(bad) > 0 {
		t.Fatalf("shipping files name secrets.DefaultDetectionConfig outside internal/secrets; use secrets.ProcessDetector:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func TestArchNoEmptyEgressVault(t *testing.T) {
	srcs := shippingSources(t, archModuleRoot(t))
	if bad := newEngineViolations(srcs); len(bad) > 0 {
		t.Errorf("shipping files call egress.NewEngine outside the process firewall; use egress.ProcessEngine:\n  %s",
			strings.Join(bad, "\n  "))
	}
	if bad := vaultViolations(srcs); len(bad) > 0 {
		t.Errorf("shipping code declares an egress value source outside internal/secrets:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func TestArchNoDetectorCtorOutsideSecrets(t *testing.T) {
	if bad := detectorCtorViolations(shippingSources(t, archModuleRoot(t))); len(bad) > 0 {
		t.Fatalf("shipping files build a detector or scrubber outside internal/secrets; use secrets.ProcessDetector:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func TestArchDetectorCtorDetectsViolation(t *testing.T) {
	srcs := seededSources(t, map[string]string{
		"internal/a/hand.go": "package a\nimport s \"" + secretsImportPath + "\"\n" +
			"var _, _ = s.NewDetector(s.DefaultRegistry(), s.DetectionConfig{ConfidenceThreshold: 0.1})\n",
		"internal/b/scrub.go":       "package b\nimport \"" + secretsImportPath + "\"\nvar f = secrets.NewScrubber\n",
		"internal/build/scan.go":    "package build\nimport \"" + secretsImportPath + "\"\nvar f = secrets.NewDetector\n",
		"internal/secrets/scrub.go": "package secrets\nimport \"" + secretsImportPath + "\"\nvar f = secrets.NewScrubber\n",
	})
	if got := strings.Join(detectorCtorViolations(srcs), ","); got != "internal/a/hand.go,internal/b/scrub.go" {
		t.Fatalf("detector/scrubber violations = %s", got)
	}
}

func TestArchNoDefaultDetectorDetectsViolation(t *testing.T) {
	srcs := seededSources(t, map[string]string{
		"internal/a/direct.go":   "package a\nimport \"" + secretsImportPath + "\"\nvar _ = secrets.DefaultDetectionConfig()\n",
		"internal/b/alias.go":    "package b\nimport s \"" + secretsImportPath + "\"\nvar f = s.DefaultDetectionConfig\n",
		"internal/c/dot.go":      "package c\nimport . \"" + secretsImportPath + "\"\n",
		"internal/secrets/ok.go": "package secrets\nimport \"" + secretsImportPath + "\"\nvar _ = secrets.DefaultDetectionConfig()\n",
		"internal/d/other.go":    "package d\nimport \"other/secrets\"\nvar _ = secrets.DefaultDetectionConfig()\n",
	})
	got := defaultDetectorViolations(srcs)
	want := []string{"internal/a/direct.go", "internal/b/alias.go", "internal/c/dot.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("violations = %v, want %v", got, want)
	}
}

func TestArchNoEmptyEgressVaultDetectsViolation(t *testing.T) {
	srcs := seededSources(t, map[string]string{
		"internal/a/engine.go":           "package a\nimport eg \"" + egressImportPath + "\"\nvar build = eg.NewEngine\n",
		"cmd/cascade/egress_firewall.go": "package main\nimport \"" + egressImportPath + "\"\nvar _ = egress.NewEngine\n",
		"cmd/cascade/other.go":           "package main\nimport \"" + egressImportPath + "\"\nvar _ = egress.NewEngine\n",
		"internal/b/vault.go": "package b\nimport \"context\"\ntype mapVault map[string][]byte\n" +
			"func (m mapVault) List(context.Context) ([]string, error) { return nil, nil }\n" +
			"func (m mapVault) Get(_ context.Context, n string) ([]byte, error) { return m[n], nil }\n",
		// The halves in two files, one through an aliased context import.
		"internal/e/list.go": "package e\nimport stdctx \"context\"\ntype split struct{}\n" +
			"func (split) List(stdctx.Context) ([]string, error) { return nil, nil }\n",
		"internal/e/get.go": "package e\nimport \"context\"\ntype ctx = context.Context\n" +
			"func (split) Get(ctx, string) ([]byte, error) { return nil, nil }\n",
		"internal/c/empty.go":  "package c\ntype nselfEmptyVault struct{}\n",
		"internal/d/pkgvar.go": "package d\nimport \"" + egressImportPath + "\"\nvar fallback egress.Vault\n",
		"internal/secrets/real.go": "package secrets\nimport \"context\"\ntype EgressVault struct{}\n" +
			"func (*EgressVault) List(context.Context) ([]string, error) { return nil, nil }\n" +
			"func (*EgressVault) Get(context.Context, string) ([]byte, error) { return nil, nil }\n",
	})
	engines := newEngineViolations(srcs)
	if strings.Join(engines, ",") != "cmd/cascade/other.go,internal/a/engine.go" {
		t.Fatalf("NewEngine violations = %v", engines)
	}
	vaults := vaultViolations(srcs)
	want := []string{
		"internal/b: type mapVault implements egress.Vault",
		"internal/c/empty.go: declares nselfEmptyVault",
		"internal/d/pkgvar.go: package-level egress.Vault fallback",
		"internal/e: type split implements egress.Vault",
	}
	if strings.Join(vaults, "|") != strings.Join(want, "|") {
		t.Fatalf("vault violations = %v, want %v", vaults, want)
	}
}
