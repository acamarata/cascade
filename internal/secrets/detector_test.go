package secrets

// Purpose: the detector's behavioural tests. Three properties dominate:
//
//	the corpus produces exactly one hit per credential class, the
//	adversarial near-miss corpus produces zero QUARANTINE entries
//	end-to-end, and no output of any kind carries a planted canary value.
//
// Constraints: this file plants real-shaped canaries and then proves they
//
//	are absent from every rendered form of the result, following the
//	canary pattern custody_darwin_test.go and import_test.go established
//	in this package.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// corpusDir is the seeded detector corpus.
const corpusDir = "testdata/detector"

// testDetector builds a detector on the ratified defaults.
func testDetector(t *testing.T) *Detector {
	t.Helper()
	d, err := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if err != nil {
		t.Fatalf("building the detector: %v", err)
	}
	return d
}

// readCorpus loads one fixture.
func readCorpus(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir, name))
	if err != nil {
		t.Fatalf("reading the fixture %s: %v", name, err)
	}
	return raw
}

// TestScanCredentialCorpus asserts the acceptance criterion directly:
// exactly one hit per credential fixture, of the expected class, above
// the quarantine threshold, carrying a usable UPPER_SNAKE name.
func TestScanCredentialCorpus(t *testing.T) {
	cases := []struct {
		file  string
		class Class
		name  string
	}{
		{"api-key.txt", ClassAPIKey, "OPENAI_API_KEY"},
		{"jwt.txt", ClassJWT, "JWT_TOKEN"},
		{"bearer.txt", ClassBearer, "AUTHORIZATION"},
		{"pem.txt", ClassPEM, "PRIVATE_KEY"},
		{"conn-str.txt", ClassConnString, "CONNECTION_STRING"},
		{"base64-json.txt", ClassBase64JSON, "ENCODED_CREDENTIAL"},
	}
	d := testDetector(t)
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			hits := d.Scan(readCorpus(t, tc.file))
			if len(hits) != 1 {
				t.Fatalf("%s produced %d hits, want exactly 1: %+v", tc.file, len(hits), hits)
			}
			hit := hits[0]
			if hit.Class != tc.class {
				t.Errorf("class = %q, want %q", hit.Class, tc.class)
			}
			if hit.SuggestedName != tc.name {
				t.Errorf("suggested name = %q, want %q", hit.SuggestedName, tc.name)
			}
			if hit.Confidence < Confidence(DefaultConfidenceThreshold) {
				t.Errorf("confidence %v is below the default threshold", hit.Confidence)
			}
			if err := validateSecretName(hit.SuggestedName); err != nil {
				t.Errorf("suggested name is not a usable vault name: %v", err)
			}
		})
	}
}

// TestScanPlainProseIsClean asserts the other half of the precision
// contract: ordinary writing produces nothing at all.
func TestScanPlainProseIsClean(t *testing.T) {
	if hits := testDetector(t).Scan(readCorpus(t, "plain-prose.txt")); len(hits) != 0 {
		t.Fatalf("plain prose produced %d hits: %+v", len(hits), hits)
	}
}

// TestNearMissCorpusQuarantinesNothing is the measured-precision
// assertion, run END TO END through the quarantine gate rather than only
// against the hit list: UUIDs, git shas, base64 images and high-entropy
// non-secrets must leave the store empty.
func TestNearMissCorpusQuarantinesNothing(t *testing.T) {
	d := testDetector(t)
	store := testStore(t)
	nearMisses, err := filepath.Glob(filepath.Join(corpusDir, "near-miss-*.txt"))
	if err != nil || len(nearMisses) == 0 {
		t.Fatalf("the near-miss corpus is missing (%v)", err)
	}
	for _, path := range nearMisses {
		raw := readCorpus(t, filepath.Base(path))
		for _, hit := range d.ScanCertain(raw) {
			if _, perr := store.Put(hit, path, raw[hit.Offset:hit.Offset+hit.Len]); perr != nil {
				t.Fatalf("quarantining: %v", perr)
			}
		}
	}
	count, err := store.PendingCount()
	if err != nil {
		t.Fatalf("counting: %v", err)
	}
	if count != 0 {
		entries, _ := store.List()
		t.Fatalf("the near-miss corpus produced %d quarantine entries: %+v", count, entries)
	}
}

// TestScanIsDeterministic locks Art.7: same input, same hits, same order,
// every time. A map iterated anywhere on the output path breaks this.
func TestScanIsDeterministic(t *testing.T) {
	d := testDetector(t)
	content := []byte("api_key=sk-Nn4vQ2tR7yWx1Zb8Ka3Ld6Mf9Ph0Sj2Uv5Xy8Ac1De4Gi7 " +
		"and token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1 " +
		"and password: Qp7Zx2Mk9Lw4Rt6Vb")
	first := d.Scan(content)
	if len(first) < 2 {
		t.Fatalf("expected several hits, got %d", len(first))
	}
	for i := 0; i < 25; i++ {
		if got := d.Scan(content); !sameHits(first, got) {
			t.Fatalf("run %d differed:\n first=%+v\n got=%+v", i, first, got)
		}
	}
	for i := 1; i < len(first); i++ {
		if first[i].Offset < first[i-1].Offset {
			t.Fatalf("hits are not sorted by offset: %+v", first)
		}
	}
}

// sameHits compares two hit slices element-wise.
func sameHits(a, b []DetectionHit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestScanNeverEmitsTheValue is the canary test. A planted secret must
// not appear in any hit field, nor in the JSON rendering a CLI or an
// event payload would produce from one.
func TestScanNeverEmitsTheValue(t *testing.T) {
	const canary = "sk-CANARYvalue0123456789abcdefghijklmnop"
	hits := testDetector(t).Scan([]byte("api_key=" + canary + "\n"))
	if len(hits) == 0 {
		t.Fatal("the canary was not detected at all, so this test proves nothing")
	}
	rendered, err := json.Marshal(hits)
	if err != nil {
		t.Fatalf("rendering hits: %v", err)
	}
	if strings.Contains(string(rendered), canary) || strings.Contains(string(rendered), "CANARYvalue") {
		t.Fatalf("a hit rendering carried the value: %s", rendered)
	}
	for _, hit := range hits {
		if strings.Contains(hit.SuggestedName, "CANARY") || strings.Contains(hit.Pattern, "CANARY") {
			t.Fatalf("a hit field carried the value: %+v", hit)
		}
	}
}

// TestEntropyAloneNeverReachesTheThreshold is the precision-first rule
// itself: a random-looking run with no structural marker and no
// credential-named field beside it is reported, but never quarantined.
func TestEntropyAloneNeverReachesTheThreshold(t *testing.T) {
	d := testDetector(t)
	content := []byte("checksum 7Kq2mZx9PLw4Rt6VbN3sQe8Hj1Cd5Fg0 verified")
	hits := d.Scan(content)
	if len(hits) != 1 || hits[0].Class != ClassHighEntropy {
		t.Fatalf("expected one high-entropy hint, got %+v", hits)
	}
	if hits[0].Confidence != ConfidenceWeak {
		t.Errorf("confidence = %v, want ConfidenceWeak", hits[0].Confidence)
	}
	if certain := d.ScanCertain(content); len(certain) != 0 {
		t.Fatalf("an entropy-only hit passed the threshold: %+v", certain)
	}
}

// TestNamedEntropyIsCorroborated is the other side of the same rule: the
// same run beside a credential-named field IS quarantine-eligible.
func TestNamedEntropyIsCorroborated(t *testing.T) {
	hits := testDetector(t).ScanCertain([]byte("wifi password: 7Kq2mZx9PLw4Rt6VbN3sQe8Hj1Cd5Fg0"))
	if len(hits) != 1 {
		t.Fatalf("expected one corroborated hit, got %+v", hits)
	}
	if hits[0].SuggestedName != "WIFI_PASSWORD" {
		t.Errorf("suggested name = %q, want WIFI_PASSWORD", hits[0].SuggestedName)
	}
}

// TestStructuredIdentifiersStayBelowTheThreshold pins the deliberate
// false negative documented in structuredIdentifier: a UUID or a git sha
// is not quarantined even when a credential-named field sits beside it,
// because trace ids and commit ids are everywhere in developer content
// and quarantining them is how a detector gets switched off.
func TestStructuredIdentifiersStayBelowTheThreshold(t *testing.T) {
	d := testDetector(t)
	for _, content := range []string{
		"session_token = 550e8400-e29b-41d4-a716-446655440000",
		"auth commit 9f2b7c1d4e6a8b0c3d5f7a9e1b3c5d7f9a1b3c5d",
	} {
		if hits := d.ScanCertain([]byte(content)); len(hits) != 0 {
			t.Fatalf("%q was quarantine-eligible: %+v", content, hits)
		}
	}
}

// TestScanErrorPaths covers the degenerate inputs: nil, empty, and
// content that is entirely below the entropy floor.
func TestScanErrorPaths(t *testing.T) {
	d := testDetector(t)
	if hits := d.Scan(nil); hits != nil {
		t.Errorf("nil content produced %+v", hits)
	}
	if hits := d.Scan([]byte{}); hits != nil {
		t.Errorf("empty content produced %+v", hits)
	}
	if hits := d.Scan([]byte("aaaaaaaaaaaaaaaaaaaa1")); len(hits) != 0 {
		t.Errorf("an all-one-character run was reported: %+v", hits)
	}
	if hits := d.Scan([]byte("short 1a2b")); len(hits) != 0 {
		t.Errorf("a sub-minimum run was reported: %+v", hits)
	}
}

// TestOverlappingSignalsCollapseToOneHit covers the regex-collision path:
// an API key is also a high-entropy run and also sits inside a bearer
// header, and one span of bytes must still produce one hit.
func TestOverlappingSignalsCollapseToOneHit(t *testing.T) {
	hits := testDetector(t).Scan([]byte("Authorization: Bearer sk-Nn4vQ2tR7yWx1Zb8Ka3Ld6Mf9Ph0Sj2Uv5Xy8"))
	if len(hits) != 1 {
		t.Fatalf("overlapping signals produced %d hits: %+v", len(hits), hits)
	}
	if hits[0].Class != ClassAPIKey {
		t.Errorf("the more specific class lost: %+v", hits[0])
	}
}

// TestReloadKeepsRunningConfigOnRejection asserts the hot-reload
// contract: a bad edit degrades to "the old rules still apply".
func TestReloadKeepsRunningConfigOnRejection(t *testing.T) {
	d := testDetector(t)
	before := d.Config()
	if err := d.Reload(DetectionConfig{EntropyFloor: -1, ConfidenceThreshold: 0.8}); err == nil {
		t.Fatal("a negative entropy floor was accepted")
	}
	if d.Config() != before {
		t.Fatalf("a rejected reload changed the running config: %+v", d.Config())
	}
	if err := d.Reload(DetectionConfig{EntropyFloor: 4.5, ConfidenceThreshold: 0.99}); err != nil {
		t.Fatalf("a valid reload was refused: %v", err)
	}
	if d.Config().EntropyFloor != 4.5 {
		t.Fatalf("the reload did not take: %+v", d.Config())
	}
}

// TestNewDetectorRefusesInvalidConfig: a detector must never start under
// a threshold nobody asked for.
func TestNewDetectorRefusesInvalidConfig(t *testing.T) {
	if _, err := NewDetector(DefaultRegistry(), DetectionConfig{}); err == nil {
		t.Fatal("a zero DetectionConfig was accepted")
	}
}

// fixedClock is the injected clock the store tests use.
type fixedClock struct{ at time.Time }

// Now returns the frozen instant.
func (c fixedClock) Now() time.Time { return c.at }
