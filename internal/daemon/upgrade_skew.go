//go:build !windows

// Package daemon implements the long-lived cascade daemon. This file holds
// the build stamp and version-skew detection.
//
// Split from upgrade.go so the comparison that decides whether to relaunch
// sits on its own. That decision has one sharp edge, described on CheckSkew,
// and it is easier to see when it is not buried in the drain and relaunch
// machinery.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/acamarata/cascade/pkg/cascade"
)

// BuildHash returns the build-time embedded hash. It is a package
// function (every UpgradeManager in one process shares the same answer)
// and a method below, matching the contract's "UpgradeManager.BuildHash()"
// call shape for holders of an instance.
func BuildHash() string { return buildHash }

// unstampedBuildHash is what buildHash holds when the linker did not stamp
// a real digest, meaning a local or test build rather than a release.
const unstampedBuildHash = "dev"

// BuildHash returns the running binary's embedded build hash.
func (m *UpgradeManager) BuildHash() string { return BuildHash() }

// CheckSkew reports whether the binary on disk at binaryPath differs from
// the running process's embedded BuildHash, streaming the file through
// SHA-256 rather than loading it whole. Any I/O failure (missing file,
// permission denied, a directory instead of a file) is a typed
// cascade.KindUnavailable error, never swallowed into a false "no skew".
func (m *UpgradeManager) CheckSkew(binaryPath string) (bool, error) {
	// An unstamped build reports the sentinel rather than a hash of
	// itself, and the sentinel can never equal a hex digest. Comparing
	// them directly therefore reports skew on every call, so an unstamped
	// daemon would decide it had been upgraded every time it was asked,
	// drain, and relaunch itself on an ordinary shutdown signal. That is
	// exactly what happened once: wiring the manager in made every dev
	// build relaunch on SIGTERM instead of exiting.
	//
	// The composition root also declines to wire the manager into an
	// unstamped build, which is the right place to make that policy
	// decision. This check stays anyway. That guard sits at one call site,
	// and a single-site defence stops working the moment a second call
	// site appears, which is the shape of the original bug.
	if m.BuildHash() == unstampedBuildHash {
		return false, nil
	}
	sum, err := hashFile(binaryPath)
	if err != nil {
		return false, cascade.Wrapf(cascade.KindUnavailable, err, "daemon: upgrade: hash %s", binaryPath)
	}
	return sum != m.BuildHash(), nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
