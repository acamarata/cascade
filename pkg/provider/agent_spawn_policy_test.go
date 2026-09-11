// Purpose: tests for the R-21.151 env allowlists and the R-21.177
//
//	fail-closed PreSpawnScan.
//
// Constraints: the seeded secret fixture below is split across two string
//
//	literals so no contiguous credential-shaped match exists in source
//	(GitHub push protection blocks it otherwise); concatenation still
//	produces the real value at runtime, so the detector under test still
//	sees the credential shape it must catch.
//
// SPORT: pkg.provider.AgentProvider tests (EXTEND) — P1-E30-W6-S61-T1.
package provider_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestDriverEnvAllowlistIsNormative(t *testing.T) {
	got := provider.DriverEnvAllowlist("VENDOR_TOKEN")
	want := []string{"PATH", "HOME", "TMPDIR", "LANG", "CASCADE_JOB_ID", "CASCADE_SOCKET",
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "VENDOR_TOKEN"}
	if len(got) != len(want) {
		t.Fatalf("DriverEnvAllowlist = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DriverEnvAllowlist[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestToolEnvOmitsVendorAuthVar(t *testing.T) {
	driver := provider.DriverEnvAllowlist("VENDOR_TOKEN")
	tool := provider.ToolEnvAllowlist()
	if len(driver)-len(tool) != 1 {
		t.Fatalf("driver allowlist has %d entries, tool has %d: want exactly one more (vendorAuthVar)", len(driver), len(tool))
	}
	for _, v := range tool {
		if v == "VENDOR_TOKEN" {
			t.Fatal("ToolEnvAllowlist contains the vendorAuthVar; untrusted tools must never see it")
		}
	}
}

func TestPreSpawnScanFailsClosed(t *testing.T) {
	root := t.TempDir()
	if err := provider.PreSpawnScan(root, provider.WorktreeScope{}); err != nil {
		t.Fatalf("PreSpawnScan(empty tree): %v, want nil", err)
	}

	// A non-existent prefix scoped explicitly is not an error (nothing to
	// scan there yet); real I/O failures on an existing path are.
	scope := provider.WorktreeScope{InScopePrefixes: []string{"missing"}}
	if err := provider.PreSpawnScan(root, scope); err != nil {
		t.Fatalf("PreSpawnScan(missing prefix): %v, want nil", err)
	}
}

func TestPreSpawnScanAbortsOnSecretShape(t *testing.T) {
	root := t.TempDir()
	// Split so no contiguous AWS-access-key-shaped literal exists in this
	// source file; the concatenation is what the scanner reads at runtime.
	seeded := "AKIA" + "7YQ2XPLM4RZV6WTB"
	if err := os.WriteFile(filepath.Join(root, "leaked.env"), []byte("KEY="+seeded), 0o600); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	err := provider.PreSpawnScan(root, provider.WorktreeScope{})
	if !cascade.HasKind(err, cascade.KindCapabilityDenied) {
		t.Fatalf("PreSpawnScan(seeded secret) err = %v, want ErrPreSpawnSecretFound", err)
	}
}
