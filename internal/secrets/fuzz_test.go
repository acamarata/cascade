package secrets

// Purpose: FuzzDetector, the §5.7 fuzz target this ticket owes for the
//
//	pattern registry's base64-JSON decoder and the scanner around it.
//
// Constraints: the properties asserted are stronger than "it did not
//
//	panic". Every hit must address bytes that actually exist (an
//	out-of-range Offset/Len would make a rewriter corrupt the document it
//	is trying to protect), every hit must carry a promotable name, no two
//	hits may overlap, the order must be the documented one, and a second
//	scan of the same input must return the identical result.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// detectorFuzzSeedDir is the by-target seed corpus, in the shared home
// every fuzz target in this module uses.
const detectorFuzzSeedDir = "../testdata/fuzz/FuzzDetector"

// FuzzDetector drives Detector.Scan over arbitrary content.
func FuzzDetector(f *testing.F) {
	addDetectorSeeds(f)
	detector, err := NewDetector(DefaultRegistry(), DefaultDetectionConfig())
	if err != nil {
		f.Fatalf("building the detector: %v", err)
	}
	f.Fuzz(func(t *testing.T, data string) {
		hits := detector.Scan([]byte(data))
		checkHitsAddressableAndOrdered(t, data, hits)
		if again := detector.Scan([]byte(data)); !sameHits(hits, again) {
			t.Fatalf("two scans of the same input differed: %+v vs %+v", hits, again)
		}
		for _, hit := range detector.ScanCertain([]byte(data)) {
			if hit.Confidence < Confidence(DefaultConfidenceThreshold) {
				t.Fatalf("ScanCertain returned a hit below the threshold: %+v", hit)
			}
		}
	})
}

// checkHitsAddressableAndOrdered asserts the invariants every consumer of
// a hit relies on.
func checkHitsAddressableAndOrdered(t *testing.T, data string, hits []DetectionHit) {
	t.Helper()
	previousEnd := -1
	previousOffset := -1
	for _, hit := range hits {
		if hit.Offset < 0 || hit.Len <= 0 || hit.Offset+hit.Len > len(data) {
			t.Fatalf("hit %+v does not address %d bytes of content", hit, len(data))
		}
		if hit.SuggestedName == "" || validateSecretName(hit.SuggestedName) != nil {
			t.Fatalf("hit %+v carries an unusable suggested name", hit)
		}
		if hit.Confidence <= 0 || hit.Confidence > 1 {
			t.Fatalf("hit %+v carries a confidence outside (0,1]", hit)
		}
		if hit.Offset < previousOffset {
			t.Fatalf("hits are not sorted by offset: %+v", hits)
		}
		if hit.Offset < previousEnd {
			t.Fatalf("hit %+v overlaps the previous hit", hit)
		}
		previousOffset, previousEnd = hit.Offset, hit.Offset+hit.Len
	}
}

// addDetectorSeeds loads the checked-in corpus. A missing corpus is a
// failure, not a skip: a fuzz target with no seeds explores far less than
// the one its corpus was written for.
func addDetectorSeeds(f *testing.F) {
	f.Helper()
	entries, err := os.ReadDir(detectorFuzzSeedDir)
	if err != nil {
		f.Fatalf("reading the seed corpus: %v", err)
	}
	if len(entries) == 0 {
		f.Fatal("the seed corpus is empty")
	}
	for _, entry := range entries {
		raw, rerr := os.ReadFile(filepath.Join(detectorFuzzSeedDir, entry.Name())) //nolint:gosec // fixed test corpus path
		if rerr != nil {
			f.Fatalf("reading seed %s: %v", entry.Name(), rerr)
		}
		f.Add(decodeDetectorSeed(string(raw)))
	}
	f.Add("")
	f.Add("password: ")
}

// decodeDetectorSeed pulls the string literal out of a go-test corpus
// file, so the same files seed both `go test` and `go test -fuzz`.
func decodeDetectorSeed(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		if !strings.HasPrefix(line, "string(") {
			continue
		}
		unquoted, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(line, "string("), ")"))
		if err == nil {
			return unquoted
		}
	}
	return raw
}
