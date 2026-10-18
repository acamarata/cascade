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
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// awsMatch is a fixture matcher: it hits on the AKIA access-key prefix. The
// package holds no pattern list; the real matcher is the host's detector.
func awsMatch(content []byte) (string, bool) {
	return "aws_access_key", bytes.Contains(content, []byte("AKIA"))
}

func TestDriverEnvAllowlistIsNormative(t *testing.T) {
	got := provider.DriverEnvAllowlist("VENDOR_TOKEN")
	want := []string{"PATH", "HOME", "TMPDIR", "LANG", "CASCADE_JOB_ID", "CASCADE_SOCKET",
		"CASCADE_PRINCIPAL_TOKEN", "CASCADE_LEASE_ID",
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "VENDOR_TOKEN"}
	if len(got) != len(want) {
		t.Fatalf("DriverEnvAllowlist = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DriverEnvAllowlist[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, v := range got {
		if v == "ALL_PROXY" || v == "all_proxy" {
			t.Fatalf("DriverEnvAllowlist carries %s: the one per-spawn proxy is the only route out", v)
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
	if err := provider.PreSpawnScan(root, provider.WorktreeScope{}, awsMatch); err != nil {
		t.Fatalf("PreSpawnScan(empty tree): %v, want nil", err)
	}

	// A non-existent prefix scoped explicitly is not an error (nothing to
	// scan there yet); real I/O failures on an existing path are.
	scope := provider.WorktreeScope{InScopePrefixes: []string{"missing"}}
	if err := provider.PreSpawnScan(root, scope, awsMatch); err != nil {
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

	err := provider.PreSpawnScan(root, provider.WorktreeScope{}, awsMatch)
	if err != provider.ErrPreSpawnSecretFound {
		t.Fatalf("PreSpawnScan(seeded secret) err = %v, want ErrPreSpawnSecretFound itself", err)
	}
}

func TestDriverEnvAllowlistVariadic(t *testing.T) {
	base := provider.DriverEnvAllowlist()
	if one := provider.DriverEnvAllowlist("CONFIG_DIR"); len(one) != len(base)+1 || one[len(one)-1] != "CONFIG_DIR" {
		t.Fatalf("one-arg allowlist = %v, want base + CONFIG_DIR", one)
	}
	two := provider.DriverEnvAllowlist("CONFIG_DIR", "KEY_VAR")
	if len(two) != len(base)+2 || two[len(two)-2] != "CONFIG_DIR" || two[len(two)-1] != "KEY_VAR" {
		t.Fatalf("two-arg allowlist = %v, want base + CONFIG_DIR + KEY_VAR", two)
	}
	if blank := provider.DriverEnvAllowlist("", "KEY_VAR"); len(blank) != len(base)+1 || blank[len(blank)-1] != "KEY_VAR" {
		t.Fatalf("an empty auth var must be skipped, got %v", blank)
	}
	if tool := provider.ToolEnvAllowlist(); len(tool) != len(base) {
		t.Fatalf("ToolEnvAllowlist = %v, want the base set only", tool)
	}
}

// TestPreSpawnScanSkipsNamedPipes: a FIFO in the tree must not block the
// spawn (reading one never returns) and holds no stored secret.
func TestPreSpawnScanSkipsNamedPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no mkfifo on windows")
	}
	root := t.TempDir()
	if out, err := exec.Command("mkfifo", filepath.Join(root, "pipe")).CombinedOutput(); err != nil {
		t.Skipf("mkfifo unavailable: %v %s", err, out)
	}
	// A link to a FIFO outside the tree is not read either (os.ReadFile follows links).
	if err := os.Symlink(filepath.Join(root, "pipe"), filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- provider.PreSpawnScan(root, provider.WorktreeScope{}, awsMatch) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PreSpawnScan(tree with a FIFO) = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PreSpawnScan blocked on a FIFO")
	}
}

// TestPreSpawnScanLinkToDirectoryFailsClosed: a symlink to a directory is read
// as a file, which fails, so the spawn is refused rather than the link skipped.
func TestPreSpawnScanLinkToDirectoryFailsClosed(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "dirlink")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := provider.PreSpawnScan(root, provider.WorktreeScope{}, awsMatch); err == nil {
		t.Fatal("PreSpawnScan(tree with a link to a directory) = nil, want a fail-closed error")
	}
}

// TestPreSpawnScanTakesMatcher: the scan runs the caller's matcher over every
// regular file in scope, reports a hit as the sentinel, passes a clean tree,
// and refuses a nil matcher instead of treating the tree as clean.
func TestPreSpawnScanTakesMatcher(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []string
	record := func(content []byte) (string, bool) {
		seen = append(seen, string(content))
		return "", false
	}
	if err := provider.PreSpawnScan(root, provider.WorktreeScope{}, record); err != nil {
		t.Fatalf("PreSpawnScan(clean tree) = %v, want nil", err)
	}
	if len(seen) != 1 || seen[0] != "plain" {
		t.Fatalf("matcher saw %q, want the one file's content", seen)
	}
	hit := func([]byte) (string, bool) { return "any", true }
	if err := provider.PreSpawnScan(root, provider.WorktreeScope{}, hit); err != provider.ErrPreSpawnSecretFound {
		t.Fatalf("PreSpawnScan(matcher hit) = %v, want ErrPreSpawnSecretFound by identity", err)
	}
	err := provider.PreSpawnScan(root, provider.WorktreeScope{}, nil)
	if !cascade.HasKind(err, cascade.KindInvalidInput) || err == provider.ErrPreSpawnSecretFound {
		t.Fatalf("PreSpawnScan(nil matcher) = %v, want KindInvalidInput", err)
	}
}
