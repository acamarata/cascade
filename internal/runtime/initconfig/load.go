package initconfig

// Purpose: reading a setup file from disk and refusing one that carries a
//   credential (P1-E16-W4-S35-T7).
// Inputs: a path, and the real H/S-15.T3 detector.
// Outputs: a parsed, scanned *InitConfig, or a refusal.
// Constraints: the secret scan runs over the RAW BYTES before the parse
//   result reaches anyone. A setup file is a file people commit, and a
//   key written into one is the single most likely way this surface leaks
//   a credential.
// SPORT: internal/runtime/initconfig loader (ADD) — P1-E16-W4-S35-T7.

import (
	"os"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// SecretScanner reports credential material in content. Its one method is
// transcribed from (*secrets.Detector).ScanCertain, pinned below, so this
// package holds no pattern list of its own.
type SecretScanner interface {
	ScanCertain(content []byte) []secrets.DetectionHit
}

var _ SecretScanner = (*secrets.Detector)(nil)

// NewSecretScanner builds the production scanner over the real registry
// and the shipped detection defaults.
func NewSecretScanner() (SecretScanner, error) {
	return secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
}

// Load reads, scans and parses the setup file at path.
//
// The ORDER is the reverse of the obvious one: scan first, parse second.
// A file with a key in it is refused whether or not it also parses,
// because a malformed file carrying a credential is still a file carrying
// a credential — and reporting the TOML syntax error first sends its
// author to fix the wrong thing.
func Load(path string, scanner SecretScanner) (*InitConfig, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the path is the operator's own --config argument.
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "cascade init: read the setup file %s", path)
	}
	if err := refuseLiteralSecrets(path, raw, scanner); err != nil {
		return nil, err
	}
	return Parse(raw)
}

// refuseLiteralSecrets fails when the file carries credential material.
//
// ScanCertain, not Scan: this refuses a whole setup run, so it fires on
// pattern-matched credentials and not on the entropy heuristic. A
// high-entropy base_url or a long provider name must not block an
// operator's install, and a false positive here has no workaround short
// of editing the detector.
//
// The refusal names the FILE and the credential class, never the value
// and never the matched span. An error message that quoted what it found
// would copy the secret into a terminal, a CI log and a support ticket.
func refuseLiteralSecrets(path string, raw []byte, scanner SecretScanner) error {
	if scanner == nil {
		// A missing scanner is a refusal, not an unscanned pass. This
		// check is the only thing standing between a committed setup
		// file and a committed credential (Art.1: absence is never a
		// pass).
		return cascade.New(cascade.KindInternal,
			"cascade init: the setup file could not be scanned for credentials; refusing to read it")
	}
	hits := scanner.ScanCertain(raw)
	if len(hits) == 0 {
		return nil
	}
	classes := make([]string, 0, len(hits))
	seen := map[string]bool{}
	for _, h := range hits {
		name := string(h.Class)
		if !seen[name] {
			classes = append(classes, name)
			seen[name] = true
		}
	}
	return cascade.Newf(cascade.KindInvalidInput,
		"cascade init: %s contains credential material (%v). A setup file is a file people commit — "+
			"store the secret with `cascade vault set` or export it, and give key_env the name of the "+
			"variable that holds it", path, classes)
}
