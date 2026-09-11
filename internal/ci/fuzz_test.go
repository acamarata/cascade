// Purpose: FuzzNormalizeActionsRun (06 §5.7's decoder-fuzz requirement),
//
//	driving normalizeActionsRun with malformed/truncated/adversarial JSON.
//	Seed corpus at internal/ci/testdata/fuzz/FuzzNormalizeActionsRun/
//	(package-local per R-21.266, this check names exactly this package,
//	never a /... pattern).
//
// SPORT: internal.ci.normalizeActionsRun/FUZZED (P1-E25-W5-S51-T2).
package ci

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzNormalizeActionsRun's property under test: never panic, and every
// input either decodes to a run slice with every element's Status/
// Conclusion a member of the closed vocabulary, or returns a non-nil
// error with a nil slice -- no third outcome, matching the fixture-
// derived seed corpus that anchors the real GitHub Actions dialect.
func FuzzNormalizeActionsRun(f *testing.F) {
	seedDir := filepath.Join("testdata", "fuzz", "FuzzNormalizeActionsRun")
	entries, err := os.ReadDir(seedDir)
	if err != nil {
		f.Fatalf("reading the seed corpus: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(seedDir, e.Name()))
		if readErr != nil {
			f.Fatalf("reading seed %s: %v", e.Name(), readErr)
		}
		f.Add(data)
	}
	f.Add([]byte(nil))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"workflow_runs":[{"status":"a-value-github-has-not-documented-yet"}]}`))
	f.Add([]byte(`not json at all`))

	f.Fuzz(func(t *testing.T, data []byte) {
		runs, err := normalizeActionsRun(data)
		if err != nil {
			if runs != nil {
				t.Fatalf("a refused input produced a non-nil slice alongside an error: %v", err)
			}
			return
		}
		for _, r := range runs {
			if !runStatusValid(r.Status) {
				t.Fatalf("normalized Run carries an out-of-vocabulary Status: %q", r.Status)
			}
			if !conclusionValid(r.Conclusion) {
				t.Fatalf("normalized Run carries an out-of-vocabulary Conclusion: %q", r.Conclusion)
			}
		}
	})
}

// runStatusValid reports whether s is one of the four closed RunStatus
// members (including Unknown -- the fail-closed catch-all IS a valid
// member, never itself a defect).
func runStatusValid(s RunStatus) bool {
	switch s {
	case RunStatusQueued, RunStatusInProgress, RunStatusCompleted, RunStatusUnknown:
		return true
	}
	return false
}

// conclusionValid reports whether c is one of the closed RunConclusion
// members.
func conclusionValid(c RunConclusion) bool {
	switch c {
	case ConclusionSuccess, ConclusionFailure, ConclusionCancelled, ConclusionSkipped,
		ConclusionTimedOut, ConclusionNone, ConclusionUnknown:
		return true
	}
	return false
}
