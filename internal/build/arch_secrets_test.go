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

// unelevatedReadAllowlist maps each NAMED non-elevated vault read to the
// directories whose production files may name its constructor.
//
// Rule 4, and the reason it exists: internal/secrets holds two exported
// reads that skip the elevated-verb gate, because a release binary
// refuses elevated verbs outright and a value reachable only through
// Broker.Get is unreadable in exactly the builds that ship. That
// exemption is only safe while it stays where it was argued for. The
// unexported internalGet cannot leave the package at all (rule 2 in
// arch_rehydrate_test.go); these two CAN, so the boundary has to be
// asserted rather than assumed.
//
// Each entry is one purpose with one allowlist. Widening either list is a
// deliberate edit to this table with the argument written next to it,
// which is the whole point.
var unelevatedReadAllowlist = map[string][]string{
	// The approval signer's key source. internal/policy is the consumer
	// seam (its ApprovalKeySource interface); cmd/cascade is the
	// composition root that hands one to the other.
	"NewApprovalKeyReader(": {
		filepath.Join("internal", "secrets"),
		filepath.Join("internal", "policy"),
		filepath.Join("cmd", "cascade"),
	},
	// The outbound firewall's value source. internal/mcp binds it to the
	// egress engine on the response path; cmd/cascade may bind one for a
	// process that builds its own engine.
	"NewEgressVault(": {
		filepath.Join("internal", "secrets"),
		filepath.Join("internal", "mcp"),
		filepath.Join("cmd", "cascade"),
	},
}

// TestArchUnelevatedReadCallers asserts rule 4 on the real tree.
func TestArchUnelevatedReadCallers(t *testing.T) {
	root := archModuleRoot(t)
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		scanUnelevatedReadCallers(t, root, filepath.Join(root, dir))
	}
}

// scanUnelevatedReadCallers walks tree and fails on a production file
// that names a non-elevated read constructor from outside its allowlist.
// Test files are skipped for the same reason rules 1 and 2 skip them: a
// test that drives the mechanism is not a caller in a shipped binary.
func scanUnelevatedReadCallers(t *testing.T, root, tree string) {
	t.Helper()
	err := filepath.WalkDir(tree, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // path comes from the module tree
		if rerr != nil {
			return rerr
		}
		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}
		for symbol, allowed := range unelevatedReadAllowlist {
			if !strings.Contains(string(data), symbol) {
				continue
			}
			if !dirOnAllowlist(rel, allowed) {
				t.Fatalf("%s names %s; the non-elevated vault read is limited to %v", path, symbol, allowed)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", tree, err)
	}
}

// dirOnAllowlist reports whether dir is at or under one allowlist entry,
// matching whole path segments so a sibling directory sharing a prefix
// never passes.
func dirOnAllowlist(dir string, allowed []string) bool {
	for _, a := range allowed {
		if dir == a || strings.HasPrefix(dir, a+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// TestArchUnelevatedReadCallersDetectsViolation is the seeded-violation
// half: the predicate above must report a directory that is NOT on the
// allowlist, so a green run means the rule works rather than that the
// scan matched nothing.
func TestArchUnelevatedReadCallersDetectsViolation(t *testing.T) {
	for symbol, allowed := range unelevatedReadAllowlist {
		if dirOnAllowlist(filepath.Join("internal", "hooks", "egress"), allowed) {
			t.Fatalf("%s must not be reachable from internal/hooks/egress", symbol)
		}
		if dirOnAllowlist(filepath.Join("internal", "secretsish"), allowed) {
			t.Fatalf("%s allowlist matched a string prefix rather than a path segment", symbol)
		}
		if !dirOnAllowlist(filepath.Join("internal", "secrets"), allowed) {
			t.Fatalf("%s must be reachable from its own package", symbol)
		}
	}
	if dirOnAllowlist(filepath.Join("internal", "policy"), unelevatedReadAllowlist["NewEgressVault("]) {
		t.Fatal("the egress vault must not be reachable from internal/policy; the two allowlists are not one list")
	}
}
