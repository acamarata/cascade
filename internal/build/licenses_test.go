package build

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// licensesModuleRoot locates the repo root by walking up from this file.
func licensesModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("license gate: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("license gate: no go.mod found walking up from %s", file)
		}
		dir = parent
	}
}

// licensesMaterialize copies the single-file fixture at src into a fresh
// t.TempDir() (Art.7.1) and returns the copy's path.
func licensesMaterialize(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("license gate: reading fixture %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("license gate: writing fixture copy %s: %v", dst, err)
	}
	return dst
}

// TestLicenses_RealTreeGreen: every module in the real go.mod is registered
// with a permissive, allowlisted license.
func TestLicenses_RealTreeGreen(t *testing.T) {
	root := licensesModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("license gate: reading real go.mod: %v", err)
	}
	deps, err := ParseGoModRequires(data)
	if err != nil {
		t.Fatalf("license gate: parsing real go.mod: %v", err)
	}
	if len(deps) == 0 {
		t.Fatal("license gate: parsed zero requires from real go.mod — parser regression")
	}
	v := CheckLicenses(deps, KnownModuleLicenses)
	if len(v) != 0 {
		for _, viol := range v {
			t.Errorf("%s: %s (license=%q)", viol.Module, viol.Reason, viol.License)
		}
	}
}

// TestParseGoModRequires_BlockAndSingleLine covers both require syntaxes
// this parser must support.
func TestParseGoModRequires_BlockAndSingleLine(t *testing.T) {
	src := []byte(`module example.com/x

go 1.26.2

require example.com/single v1.0.0

require (
	example.com/block-a v2.0.0
	example.com/block-b v3.0.0 // indirect
)
`)
	deps, err := ParseGoModRequires(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := map[string]string{
		"example.com/single":  "v1.0.0",
		"example.com/block-a": "v2.0.0",
		"example.com/block-b": "v3.0.0",
	}
	if len(deps) != len(want) {
		t.Fatalf("got %d deps, want %d: %+v", len(deps), len(want), deps)
	}
	for _, d := range deps {
		if wantVer, ok := want[d.Path]; !ok || wantVer != d.Version {
			t.Errorf("unexpected dep %+v", d)
		}
	}
}

// TestLicenses_SeededViolationRed_Copyleft: the copyleft fixture's module is
// registered (in a fixture-local registry, never the production one) as
// GPL-3.0, which is not on LicenseAllowlist — the gate must flag it.
func TestLicenses_SeededViolationRed_Copyleft(t *testing.T) {
	root := licensesModuleRoot(t)
	src := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "licenses", "copyleft", "go.mod")
	fixture := licensesMaterialize(t, src)
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("reading materialized fixture: %v", err)
	}
	deps, err := ParseGoModRequires(data)
	if err != nil {
		t.Fatalf("parsing fixture go.mod: %v", err)
	}
	fixtureRegistry := map[string]string{
		"example.com/gplcopyleft": "GPL-3.0",
	}
	v := CheckLicenses(deps, fixtureRegistry)
	if len(v) == 0 {
		t.Fatal("expected a copyleft license violation in seeded fixture, found none")
	}
	if v[0].License != "GPL-3.0" || v[0].Reason != "license not on allowlist" {
		t.Fatalf("unexpected violation shape: %+v", v[0])
	}
}

// TestLicenses_SeededViolationRed_Unknown: the unknown fixture's module has
// no registry entry at all — the gate must fail closed.
func TestLicenses_SeededViolationRed_Unknown(t *testing.T) {
	root := licensesModuleRoot(t)
	src := filepath.Join(root, "internal", "build", "testdata", "seeded-violations", "licenses", "unknown", "go.mod")
	fixture := licensesMaterialize(t, src)
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatalf("reading materialized fixture: %v", err)
	}
	deps, err := ParseGoModRequires(data)
	if err != nil {
		t.Fatalf("parsing fixture go.mod: %v", err)
	}
	v := CheckLicenses(deps, map[string]string{}) // empty registry: nothing known
	if len(v) == 0 {
		t.Fatal("expected an unknown-license violation in seeded fixture, found none")
	}
	if v[0].Reason != "no registry entry (unknown license)" {
		t.Fatalf("unexpected violation shape: %+v", v[0])
	}
}
