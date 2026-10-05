// Purpose: the last fail-closed check between the Redactor and the disk.
// Every fixture and README byte is scanned before anything is written.
// Inputs: rendered fixture or README bytes.
// Outputs: nil, or a redaction-failure refusal naming the file and the
// check class (never the matched bytes).
// Constraints: refuses on any certain internal/secrets detector hit, a home
// or temp path, the scratch dir prefix, or a synthetic vault name or value
// (MIG_FIXTURE_SECRET_ / NONSECRET-) that the Redactor should have mapped.
// SPORT: migration/golden/ADD (P1-E26-W10-S54-T3).
package main

import (
	"os"
	"strings"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// forbiddenOutput are substrings no committed byte may hold.
var forbiddenOutput = []string{"/Users/", "/home/", `\Users\`, "cascade-golden-", fixtureSecretPrefix, "NONSECRET-"}

// guardFixtures checks every fixture's bytes.
func guardFixtures(fixtures []fixture) error {
	det, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "golden harvest: build the secret detector")
	}
	for _, fx := range fixtures {
		if err := guardBytes(det, fx.Name, fx.Data); err != nil {
			return err
		}
	}
	return nil
}

// guardBytes refuses data that carries a secret-shaped span, a private
// path or an unmapped synthetic vault form.
func guardBytes(det *secrets.Detector, name string, data []byte) error {
	if hits := det.ScanCertain(data); len(hits) > 0 {
		return redactionFailure(name, "a secret-shaped value")
	}
	text := string(data)
	for _, needle := range append(append([]string(nil), forbiddenOutput...), environmentPaths()...) {
		if strings.Contains(text, needle) {
			return redactionFailure(name, "a private path or an unmapped synthetic name")
		}
	}
	return nil
}

// environmentPaths are the live home and temp dirs, when long enough to be
// a meaningful needle.
func environmentPaths() []string {
	var out []string
	for _, p := range []string{os.Getenv("HOME"), os.Getenv("USERPROFILE"), os.TempDir()} {
		trimmed := strings.TrimRight(p, `/\`)
		if len(trimmed) > 4 {
			out = append(out, trimmed)
		}
	}
	return out
}

// redactionFailure is the refusal guardBytes returns.
func redactionFailure(name, class string) error {
	return cascade.Newf(cascade.KindIntegrity, "golden harvest: redaction failure: %s would carry %s", name, class)
}
