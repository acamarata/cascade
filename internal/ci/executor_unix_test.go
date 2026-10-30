//go:build !windows

package ci

// Tests for ComputeToolchainFingerprint off Windows (P1-CI-03).

import (
	"context"
	"os/exec"
	"regexp"
	"testing"
)

var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// TestToolchainFingerprintStable: the same inputs give the same lowercase
// hex twice; the toolchain list is order-insensitive and does change it.
func TestToolchainFingerprintStable(t *testing.T) {
	t.Cleanup(func() { SetToolchainVersions(nil) })
	a, errA := ComputeToolchainFingerprint(context.Background())
	b, errB := ComputeToolchainFingerprint(context.Background())
	if errA != nil || errB != nil || a != b || !lowerHex64.MatchString(a) {
		t.Fatalf("fingerprints %q (%v), %q (%v); want one stable lowercase hex digest", a, errA, b, errB)
	}
	SetToolchainVersions([]string{"node 22", "go1.24"})
	c, _ := ComputeToolchainFingerprint(context.Background())
	SetToolchainVersions([]string{"go1.24", "node 22"})
	d, _ := ComputeToolchainFingerprint(context.Background())
	if c != d || c == a {
		t.Fatalf("list fingerprints %q, %q, base %q; want order-insensitive and different from the empty list", c, d, a)
	}
}

// TestToolchainFingerprintPerPlatform: the fingerprint hashes this host's
// `go version` and `uname -sm`, so another platform's answer differs.
func TestToolchainFingerprintPerPlatform(t *testing.T) {
	goVersion, err := exec.Command("go", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	host, err := exec.Command("uname", "-sm").Output()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ComputeToolchainFingerprint(context.Background())
	if err != nil || got != fingerprintOf(string(goVersion), string(host)) {
		t.Fatalf("fingerprint %q, %v; want the hash of this host's go version and uname -sm", got, err)
	}
	if fingerprintOf(string(goVersion), "Linux x86_64") == fingerprintOf(string(goVersion), "Darwin arm64") ||
		fingerprintOf("go version go1.24.0", string(host)) == fingerprintOf("go version go1.25.0", string(host)) {
		t.Fatal("platform or go version does not change the fingerprint")
	}
}
