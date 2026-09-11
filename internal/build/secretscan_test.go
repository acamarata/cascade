// Package build (this file): secretscan.go's own tests. One leg asserts
// the real tracked tree (TestSecretScanGate — the launch-gate check
// itself); the seeded-violation leg (TestSecretScanSeededViolation)
// proves the gate turns RED, generating its credential-shaped fixture at
// test time into t.TempDir() and never committing it (F42, this repo's
// public-push-protection law — see the AGENT-BRIEF split-literal note);
// the remaining legs cover the error and exemption paths.
package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/secrets"
)

// TestSecretScanGate is the gate: zero credential-shaped hits at gate
// confidence across every git-tracked file in the real tree, testdata
// paths excluded by design (secretscan.go's package doc).
func TestSecretScanGate(t *testing.T) {
	root := archModuleRoot(t)
	det, err := NewSecretScanDetector()
	if err != nil {
		t.Fatalf("NewSecretScanDetector: %v", err)
	}
	hits, err := ScanTrackedFilesForSecrets(det, root)
	if err != nil {
		t.Fatalf("ScanTrackedFilesForSecrets: %v", err)
	}
	if len(hits) > 0 {
		t.Fatalf("secret scan found credential-shaped content in tracked files:\n%s", FormatSecretScanHits(hits))
	}
}

// TestSecretScanSeededViolation is the RED proof: a file carrying a
// credential-shaped string materialized at test time, outside testdata/,
// must be reported. The literal is split across two string constants
// (matching internal/secrets/redactor_test.go's shapedSecret and the
// AGENT-BRIEF's rule for this public repo) so no contiguous
// credential-shaped run exists in this file's own source, only in the
// byte string scanFilesForSecrets reads back from the tempdir file it
// wrote.
func TestSecretScanSeededViolation(t *testing.T) {
	root := t.TempDir()
	seeded := "AKIA" + "9QRX2PLM7NZV6WTC" // 20 chars: AKIA + 16 [A-Z0-9]
	rel := filepath.Join("cmd", "leaked.txt")
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("creating fixture dir: %v", err)
	}
	if err := os.WriteFile(full, []byte("AWS_ACCESS_KEY_ID="+seeded+"\n"), 0o600); err != nil {
		t.Fatalf("writing seeded fixture: %v", err)
	}
	det, err := NewSecretScanDetector()
	if err != nil {
		t.Fatalf("NewSecretScanDetector: %v", err)
	}
	hits, err := scanFilesForSecrets(det, root, []string{rel}, nil)
	if err != nil {
		t.Fatalf("scanFilesForSecrets: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected exactly one hit on the seeded fixture, got %d: %+v", len(hits), hits)
	}
	if hits[0].File != rel {
		t.Fatalf("hit.File = %q, want %q", hits[0].File, rel)
	}
	if hits[0].Class != string(secrets.ClassAPIKey) {
		t.Fatalf("hit.Class = %q, want %q", hits[0].Class, secrets.ClassAPIKey)
	}
	if hits[0].Confidence < 0.8 {
		t.Fatalf("hit.Confidence = %v, want >= the gate confidence threshold", hits[0].Confidence)
	}
	if !strings.Contains(hits[0].String(), rel) {
		t.Fatalf("String() = %q, does not name the offending file", hits[0].String())
	}
}

// TestSecretScanCleanFixtureIsQuiet proves the seeded-violation leg is
// not simply reporting every file: an ordinary tracked-looking file with
// no credential shape produces zero hits.
func TestSecretScanCleanFixtureIsQuiet(t *testing.T) {
	root := t.TempDir()
	rel := "notes.txt"
	if err := os.WriteFile(filepath.Join(root, rel), []byte("nothing secret here, just prose.\n"), 0o600); err != nil {
		t.Fatalf("writing clean fixture: %v", err)
	}
	det, err := NewSecretScanDetector()
	if err != nil {
		t.Fatalf("NewSecretScanDetector: %v", err)
	}
	hits, err := scanFilesForSecrets(det, root, []string{rel}, nil)
	if err != nil {
		t.Fatalf("scanFilesForSecrets: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected zero hits on a clean fixture, got %+v", hits)
	}
}

// TestSecretScanSkipsTestdata proves the testdata exemption: the same
// seeded credential shape, placed under a testdata/ path, produces zero
// hits — the gate's documented, deliberate blind spot (secretscan.go's
// package doc), which is what keeps this repo's own redaction/doctor
// fixtures (e.g. internal/doctor/testdata/fixture_corpus.txt) from
// permanently failing the gate.
func TestSecretScanSkipsTestdata(t *testing.T) {
	root := t.TempDir()
	seeded := "AKIA" + "3QRX2PLM7NZV6WTD"
	rel := filepath.Join("internal", "fixture", "testdata", "leak.txt")
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("creating fixture dir: %v", err)
	}
	if err := os.WriteFile(full, []byte(seeded+"\n"), 0o600); err != nil {
		t.Fatalf("writing seeded fixture: %v", err)
	}
	det, err := NewSecretScanDetector()
	if err != nil {
		t.Fatalf("NewSecretScanDetector: %v", err)
	}
	hits, err := scanFilesForSecrets(det, root, []string{rel}, nil)
	if err != nil {
		t.Fatalf("scanFilesForSecrets: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected the testdata path to be skipped, got %+v", hits)
	}
}

// TestSecretScanMissingFileFailsClosed proves an unreadable tracked file
// is a hard error, never a silent skip (secretscan.go's fail-closed law,
// matching SweepFiles's).
func TestSecretScanMissingFileFailsClosed(t *testing.T) {
	det, err := NewSecretScanDetector()
	if err != nil {
		t.Fatalf("NewSecretScanDetector: %v", err)
	}
	if _, err := scanFilesForSecrets(det, t.TempDir(), []string{"does/not/exist.txt"}, nil); err == nil {
		t.Fatal("expected an error for a missing file, not a silent skip")
	}
}

// TestSecretScanExemptionsAreLive proves each SecretScanExemptions entry
// still exists and still produces the hit it was exempted for. A path
// that no longer exists, or no longer trips the detector, is stale and
// must be deleted from the map — the same discipline
// TestDoctorMountExemptionsAreLive applies to DoctorMountExemptions.
func TestSecretScanExemptionsAreLive(t *testing.T) {
	root := archModuleRoot(t)
	det, err := NewSecretScanDetector()
	if err != nil {
		t.Fatalf("NewSecretScanDetector: %v", err)
	}
	for rel, reason := range SecretScanExemptions {
		if len(strings.TrimSpace(reason)) < 20 {
			t.Fatalf("the exemption for %s carries no usable reason", rel)
		}
		full := filepath.Join(root, rel)
		if _, statErr := os.Stat(full); statErr != nil {
			t.Fatalf("stale secret-scan exemption %s: file no longer exists (%v)", rel, statErr)
		}
		hits, scanErr := scanOneFileUnexempted(det, root, rel)
		if scanErr != nil {
			t.Fatalf("scanning exempted file %s: %v", rel, scanErr)
		}
		if len(hits) == 0 {
			t.Fatalf("stale secret-scan exemption %s: no longer produces a hit; delete the entry", rel)
		}
	}
}

// TestSecretScanExcludesTestFiles proves the _test.go exclusion is real
// and not accidental: the same seeded shape that trips the gate in a
// non-test file (TestSecretScanSeededViolation) produces zero hits when
// the file is named *_test.go.
func TestSecretScanExcludesTestFiles(t *testing.T) {
	root := t.TempDir()
	seeded := "AKIA" + "5QRX2PLM7NZV6WTE"
	rel := filepath.Join("internal", "fixture", "widget_test.go")
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("creating fixture dir: %v", err)
	}
	if err := os.WriteFile(full, []byte("package fixture\n\nconst k = \""+seeded+"\"\n"), 0o600); err != nil {
		t.Fatalf("writing seeded fixture: %v", err)
	}
	det, err := NewSecretScanDetector()
	if err != nil {
		t.Fatalf("NewSecretScanDetector: %v", err)
	}
	hits, err := scanFilesForSecrets(det, root, []string{rel}, nil)
	if err != nil {
		t.Fatalf("scanFilesForSecrets: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected the _test.go exclusion to suppress this hit, got %+v", hits)
	}
}
