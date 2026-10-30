//go:build !windows

// Purpose (this file): ComputeToolchainFingerprint on every platform but
// Windows (P1-CI-03): lowercase hex sha256 of `go version`, `uname -sm` and
// the sorted [ci].toolchain_versions list.
//
// Inputs: the go and uname binaries on PATH; SetToolchainVersions' list.
// Outputs: the fingerprint, or the probe's error.
// Constraints: the two probes run once per process after their first
// success (a failure is not cached); the toolchain list is read on every
// call so a new list takes effect without a restart. Exactly one
// ComputeToolchainFingerprint definition per GOOS.
// SPORT: internal.ci.ComputeToolchainFingerprint/ADDED (P1-CI-03).

package ci

import (
	"context"
	"os/exec"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

var toolchainProbe struct {
	mu              sync.Mutex
	done            bool
	goVersion, host string
}

// ComputeToolchainFingerprint returns this controller's toolchain fingerprint.
func ComputeToolchainFingerprint(ctx context.Context) (string, error) {
	toolchainProbe.mu.Lock()
	defer toolchainProbe.mu.Unlock()
	if !toolchainProbe.done {
		goVersion, err := exec.CommandContext(ctx, "go", "version").Output()
		if err != nil {
			return "", cascade.Wrap(cascade.KindUnavailable, err, "ci: toolchain fingerprint: running go version")
		}
		host, err := exec.CommandContext(ctx, "uname", "-sm").Output()
		if err != nil {
			return "", cascade.Wrap(cascade.KindUnavailable, err, "ci: toolchain fingerprint: running uname -sm")
		}
		toolchainProbe.goVersion, toolchainProbe.host, toolchainProbe.done = string(goVersion), string(host), true
	}
	return fingerprintOf(toolchainProbe.goVersion, toolchainProbe.host), nil
}
