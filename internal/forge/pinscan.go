// Package forge provides utilities for manifest pin scanning.
package forge

import (
	"crypto/sha256"
	"fmt"
	"os"
)

// Fixture represents a pinned reference to a manifest file.
type Fixture struct {
	// Path is the file path of the fixture.
	Path string
	// SHA is the expected SHA-256 hash of the referenced manifest.
	SHA string
}

// PinViolation records a mismatch between a manifest's actual hash and a fixture's pinned hash.
type PinViolation struct {
	// ManifestPath is the path of the manifest that changed.
	ManifestPath string
	// FixturePath is the path of the fixture that pins the manifest.
	FixturePath string
	// ExpectedSHA is the SHA-256 hash recorded in the fixture.
	ExpectedSHA string
	// ActualSHA is the computed SHA-256 hash of the manifest.
	ActualSHA string
}

// ScanPins computes the SHA-256 of each manifest file and compares it against
// every fixture that references it. If a manifest's actual SHA differs from
// any fixture's pinned SHA, a PinViolation is appended. An error is returned
// if a manifest or fixture file cannot be read.
func ScanPins(manifests []string, fixtures []Fixture) ([]PinViolation, error) {
	var violations []PinViolation

	for _, m := range manifests {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("reading manifest %s: %w", m, err)
		}
		actualSHA := fmt.Sprintf("%x", sha256.Sum256(data))

		for _, f := range fixtures {
			fData, err := os.ReadFile(f.Path)
			if err != nil {
				return nil, fmt.Errorf("reading fixture %s: %w", f.Path, err)
			}
			_ = fData // fixture content available for extended checks

			if f.SHA != actualSHA {
				violations = append(violations, PinViolation{
					ManifestPath: m,
					FixturePath:  f.Path,
					ExpectedSHA:  f.SHA,
					ActualSHA:    actualSHA,
				})
			}
		}
	}

	return violations, nil
}
