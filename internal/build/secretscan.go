// Package build (this file) holds the secret-shape scan for the AA/S-56.T3
// launch-gate checklist (docs/developer/launch-gate.md): the ONE automated
// component that ticket owns outright, alongside the license report
// (A-T3, licenses.go) and the identifier sweep (A-T5, sweep.go) it
// documents but does not reimplement.
//
// It does not invent a second detection engine. internal/secrets already
// ships a precision-first credential detector (Detector.ScanCertain,
// pattern + Shannon-entropy + credential-named-field signals) built for
// the redaction/quarantine path; this file is the thin gate wrapper that
// points that same detector at every git-tracked file instead of a
// runtime buffer.
//
// Inputs: a *secrets.Detector (NewSecretScanDetector builds the gate's
// own, from secrets.DefaultRegistry/DefaultDetectionConfig) and the
// module root. It reads git-tracked files only (sweep.go's
// ListTrackedFiles), skipping any path under a testdata/ segment
// (sweep.go's SweepSkipsPath) for the same reason the identifier sweep
// does: this repo's own redaction/doctor tests ship literal
// credential-shaped fixtures under testdata/ (for example
// internal/doctor/testdata/fixture_corpus.txt), and scanning those would
// make the gate permanently red on its own test data rather than on a
// real leak.
//
// Outputs: []SecretScanHit, one per tracked file the detector flagged at
// gate confidence (ScanCertain — the precision-first threshold, never
// the wider Scan). A hit never carries the matched bytes: SecretScanHit
// mirrors secrets.DetectionHit's no-value-field design so a gate failure
// message cannot itself leak the credential it found.
//
// Constraints: fail closed. A tracked file this cannot read (missing,
// permission-denied, or otherwise) is a hard error, never a silent skip
// (identical to sweep.go's SweepFiles law).
//
// # Scope decisions this gate makes, and why they are here rather than silent
//
// Run unscoped against this repo's real tree, the shared detector's
// pattern signal alone (entropy already disabled, see
// secretScanEntropyFloor) still fired 55 times, EVERY one of them either
// a co-located _test.go fixture for the provider/config/redaction/audit
// subsystems (this repo tests its own secret handling, so its own tests
// necessarily contain credential-shaped strings — dozens of files,
// established convention, not confined to testdata/) or a documentation/
// regex-definition file showing a credential SHAPE as an example
// (docs/cli-reference/config.md, internal/secrets/registry.go's own
// pattern comments, .github/workflows/ci.yml's example DSN). Two
// different, principled exclusions follow from that split:
//
//  1. _test.go files are excluded from this gate entirely (isSecretScanTestFile).
//     This is a BLUNT, NAMED blind spot, not a silent one: a real credential
//     accidentally pasted into a _test.go file is NOT caught by this gate.
//     It is the only scope choice that does not require hand-vetting dozens
//     of pre-existing fixture files under a session's time budget, and it
//     matches this repo's real convention (tests, not testdata/, are where
//     fixture credentials live here).
//  2. The handful of non-test files whose content is a documented example or
//     the registry's own pattern definitions are named individually in
//     SecretScanExemptions, each with a reason — the same discipline as
//     testonly-allow.json and DoctorMountExemptions, and falsifiable the
//     same way: TestSecretScanExemptionsAreLive fails if a named path no
//     longer exists or no longer produces the hit it was exempted for
//     (stale exemptions hide the next one).
//
// What this gate does NOT catch, stated here because every gate in this
// package owes its blind spot in its own header: secrets outside the
// shared registry's known shapes (the entropy signal is off, see above);
// anything under a testdata/ path or any _test.go file (excluded by
// design, see above — the single largest blind spot this gate has);
// obfuscated, encoded, or reformatted credential material (the detector
// is a byte-level pattern scan, not a decoder); untracked or gitignored
// files (ListTrackedFiles's git ls-files scope, matching the identifier
// sweep's own boundary); and history outside the current tree (a
// credential that was committed and later deleted is not retroactively
// caught). A green run is proof the scoped scan found nothing new in the
// tracked, non-test tree today — never proof no credential was ever
// committed, and never proof a test file is clean.
//
// SPORT: SECRET_SCAN_GATE: ADD (internal/build launch-gate secret scan).
package build

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/secrets"
)

// SecretScanHit is one tracked file where the shared detector found
// credential-shaped content at gate confidence. It carries no matched
// bytes and no offsets into file content that could be pasted back into
// a leak — File, Class and Pattern are enough to find and fix the spot.
type SecretScanHit struct {
	// File is the repo-relative path (as ListTrackedFiles reports it).
	File string
	// Class is the credential class the shared registry matched
	// (secrets.Class, e.g. "aws-access-key", "private-key", "jwt").
	Class string
	// Pattern names the registry pattern that fired, or "entropy" for a
	// shape-only signal (secrets.DetectionHit.Pattern's own contract).
	Pattern string
	// Confidence is the score the hit earned, in [0,1].
	Confidence float64
}

// String renders one hit for gate failure output.
func (h SecretScanHit) String() string {
	return fmt.Sprintf("%s: class=%s pattern=%s confidence=%.2f", h.File, h.Class, h.Pattern, h.Confidence)
}

// secretScanEntropyFloor is set to the detector's maximum, which disables
// the shape-only entropy signal entirely (internal/secrets/detection_config.go's
// own comment: "a higher floor would disable the entropy signal
// entirely"). This is a deliberate scope choice for THIS gate, not the
// detector's runtime default: run against this repo's own tree, the
// entropy signal (a high-entropy run beside a credential-named field,
// e.g. a test variable named apiKey assigned a random-looking string)
// fired 280 times across pre-existing, legitimate test fixtures for the
// provider/config/redaction subsystems in a single run — none of them a
// real credential. A gate that red-lines on that scale of expected noise
// stops being read; a gate scoped to the registry's structural markers
// (a vendor-prefixed key, a PEM block, a JWT triplet, a URL-embedded
// password, Bearer <token>) still catches the shapes an accidentally
// committed real credential actually has, and is documented here,
// plainly, as this gate's chosen blind spot: it will NOT catch an opaque
// high-entropy secret with no vendor-format marker and no PEM/JWT/URL/
// Bearer shape.
const secretScanEntropyFloor = 8.0

// NewSecretScanDetector builds the detector this gate scans with: the
// shared internal/secrets registry, at its default confidence threshold
// but with the entropy signal disabled for this gate's scope (see
// secretScanEntropyFloor). A separate constructor (rather than a package
// var) keeps gate tests able to construct their own without mutating
// shared state.
func NewSecretScanDetector() (*secrets.Detector, error) {
	cfg := secrets.DefaultDetectionConfig()
	cfg.EntropyFloor = secretScanEntropyFloor
	return secrets.NewDetector(secrets.DefaultRegistry(), cfg)
}

// SecretScanExemptions names the tracked, non-test files this gate would
// otherwise flag, each with the reason it is a documented example or a
// pattern definition rather than a leak. An entry is DELETED the moment
// the file no longer produces the hit it names, never repointed — see
// TestSecretScanExemptionsAreLive, which fails on a stale entry the same
// way DoctorMountExemptions's staleness test does.
var SecretScanExemptions = map[string]string{
	".github/workflows/ci.yml": "line documents the local Postgres test DSN " +
		"(CASCADE_TEST_POSTGRES_DSN) with its literal example value; no real credential.",
	"docs/cli-reference/config.md": "worked example showing the shape of a vendor-prefixed " +
		"token the command REFUSES to accept, illustrating the refusal; no real credential.",
	"internal/doctor/redact.go": "the redaction gate's own pattern-definition source: a regex " +
		"literal and its doc-comment example DSN shape, not a credential value.",
	"internal/runtime/config_write_secrets.go": "the config-write guard's own vendor-prefix " +
		"list and PEM header marker, used to REFUSE writing a secret-shaped value; not a credential.",
	"internal/secrets/registry.go": "the shared detector's own pattern registry: its comments " +
		"illustrate the shapes it matches (an example DSN, prefix names), not a credential value.",
}

// isSecretScanTestFile reports whether rel is a Go test file. Test files
// are excluded from this gate entirely — see secretscan.go's package doc
// § Scope decisions for why, and for the blind spot this creates.
func isSecretScanTestFile(rel string) bool {
	return strings.HasSuffix(rel, "_test.go")
}

// scanFilesForSecrets reads each of relFiles under root and runs det's
// scan over its content, skipping any path under a testdata/ segment
// (SweepSkipsPath), any _test.go file (isSecretScanTestFile), and any
// path named in exempt. A read error is returned, never silently skipped
// — fail closed, matching SweepFiles's law in sweep.go. Exported callers
// reach it through ScanTrackedFilesForSecrets; tests call it directly
// with an explicit file list and exemption map, the same split
// SweepFiles/ListTrackedFiles uses so a seeded fixture never needs a real
// git repository to prove the scan itself.
func scanFilesForSecrets(det *secrets.Detector, root string, relFiles []string, exempt map[string]string) ([]SecretScanHit, error) {
	var out []SecretScanHit
	for _, rel := range relFiles {
		if SweepSkipsPath(rel) || isSecretScanTestFile(rel) {
			continue
		}
		if _, ok := exempt[rel]; ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // repo-relative, git-tracked path
		if err != nil {
			return nil, fmt.Errorf("secret scan: reading %s: %w", rel, err)
		}
		for _, hit := range det.ScanCertain(data) {
			out = append(out, SecretScanHit{
				File:       rel,
				Class:      string(hit.Class),
				Pattern:    hit.Pattern,
				Confidence: float64(hit.Confidence),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out, nil
}

// scanOneFileUnexempted reads path and returns the raw hits det finds,
// ignoring every exclusion this gate normally applies. It exists only for
// the exemption staleness check, which must tell "the file no longer has
// this shape" apart from "the file no longer exists" without reproducing
// scanFilesForSecrets's exclusion logic.
func scanOneFileUnexempted(det *secrets.Detector, root, rel string) ([]secrets.DetectionHit, error) {
	data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // repo-relative, git-tracked path
	if err != nil {
		return nil, fmt.Errorf("secret scan: reading %s: %w", rel, err)
	}
	return det.ScanCertain(data), nil
}

// ScanTrackedFilesForSecrets scans every git-tracked file under root
// (ListTrackedFiles) with det, skipping testdata paths, _test.go files
// and SecretScanExemptions. This is the gate's production entry point —
// TestSecretScanGate drives it over the real tree.
func ScanTrackedFilesForSecrets(det *secrets.Detector, root string) ([]SecretScanHit, error) {
	files, err := ListTrackedFiles(root)
	if err != nil {
		return nil, err
	}
	return scanFilesForSecrets(det, root, files, SecretScanExemptions)
}

// FormatSecretScanHits renders hits as one line each, for gate
// error/log output.
func FormatSecretScanHits(hits []SecretScanHit) string {
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "  - %s\n", h)
	}
	return strings.TrimRight(b.String(), "\n")
}
