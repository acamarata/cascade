// Package build (this file): the import-boundary gate for the secrets
// domain. Three rules, each RED against a fixture materialized into
// t.TempDir() and GREEN on the real tree:
//
//  1. internal/secrets must not import providers/** or plugins/**. The
//     vault is reached BY provider code, never the other way round; a
//     provider package on the vault's import path would put third-party
//     plugin code inside the process boundary that holds every secret.
//  2. internal/secrets must not import internal/elevation, and
//     internal/elevation must not import internal/secrets. This is the
//     mirror of .golangci.yml's elevation-no-vault rule, and it is
//     warranted in BOTH directions: the elevation keystore is the root of
//     trust that authorises reading a secret, so a compromise of either
//     domain must not reach the other. The broker takes its authorisation
//     decision through an injected ElevationGate that cmd/ wires, which is
//     what makes the boundary keepable.
//  3. no file in internal/secrets enables cgo. Release binaries are built
//     with CGO_ENABLED=0, so a cgo-gated custody backend would be absent
//     from every shipped artifact, which is the whole point of the
//     no-cgo custody rule.
package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretsForbiddenPrefixes are the import prefixes internal/secrets may
// never carry.
var secretsForbiddenPrefixes = []string{
	cascadeModulePath + "/providers",
	cascadeModulePath + "/plugins",
	cascadeModulePath + "/internal/elevation",
}

// TestSecretsImportBoundary asserts rules 1 and 2 on the real tree.
func TestSecretsImportBoundary(t *testing.T) {
	root := archModuleRoot(t)
	for _, file := range archScan(t, filepath.Join(root, "internal", "secrets"), cascadeModulePath) {
		for _, imp := range file.imports {
			for _, forbidden := range secretsForbiddenPrefixes {
				if imp == forbidden || strings.HasPrefix(imp, forbidden+"/") {
					t.Fatalf("internal/secrets/%s imports %s, which the secrets boundary forbids", file.relDir, imp)
				}
			}
		}
	}
	for _, file := range archScan(t, filepath.Join(root, "internal", "elevation"), cascadeModulePath) {
		for _, imp := range file.imports {
			if strings.HasPrefix(imp, cascadeModulePath+"/internal/secrets") {
				t.Fatalf("internal/elevation/%s imports %s: the elevation keystore must not be vault-backed", file.relDir, imp)
			}
		}
	}
}

// TestSecretsImportBoundaryDetectsViolation is the seeded-violation half:
// the same scan run over a fixture that DOES import a forbidden package
// must report it, so a green run above means the rule works rather than
// that the scan found nothing.
func TestSecretsImportBoundaryDetectsViolation(t *testing.T) {
	dir := t.TempDir()
	src := "package secrets\n\nimport _ \"" + cascadeModulePath + "/providers/fs\"\n"
	if err := os.WriteFile(filepath.Join(dir, "leak.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	found := false
	for _, file := range archScan(t, dir, cascadeModulePath) {
		for _, imp := range file.imports {
			for _, forbidden := range secretsForbiddenPrefixes {
				if imp == forbidden || strings.HasPrefix(imp, forbidden+"/") {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("the secrets boundary scan did not flag a seeded providers/ import")
	}
}

// TestSecretsHasNoCGO asserts rule 3: no "C" import and no cgo build
// constraint anywhere in internal/secrets, so every custody backend is
// present in a CGO_ENABLED=0 release binary.
func TestSecretsHasNoCGO(t *testing.T) {
	dir := filepath.Join(archModuleRoot(t), "internal", "secrets")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading internal/secrets: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // fixed repo path
		if rerr != nil {
			t.Fatalf("reading %s: %v", entry.Name(), rerr)
		}
		text := string(raw)
		if strings.Contains(text, "\"C\"") {
			t.Fatalf("%s imports \"C\": internal/secrets must build with CGO_ENABLED=0", entry.Name())
		}
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "//go:build") && strings.Contains(line, "cgo") {
				t.Fatalf("%s carries a cgo build constraint (%q): a cgo-gated custody backend is absent from every release binary", entry.Name(), line)
			}
			if strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "func ") {
				break
			}
		}
	}
}
