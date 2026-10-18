// Purpose: the content matcher the pre-spawn scan runs, built over the
// process's one secret detector so no second pattern list exists.
// Inputs: secrets.ProcessDetector().
// Outputs: a func that reports the credential class of the first
// confident hit in a byte slice, or false when there is none.
// Constraints: the detector's own error is returned unchanged (a source
// that failed to resolve fails the spawn closed); only hits at the
// configured confidence threshold count (ScanCertain), so a high-entropy
// file that is not credential-shaped does not abort a spawn; the class
// string is the class name only, never any byte of the match.
// SPORT: internal.providers.agents/ADD (P1-AGT-15, R16 M-10).

package agents

import "github.com/acamarata/cascade/internal/secrets"

// ProcessDetectorMatch returns the matcher provider.PreSpawnScan takes,
// backed by the process detector. The matcher is safe for concurrent use.
func ProcessDetectorMatch() (func([]byte) (string, bool), error) {
	detector, err := secrets.ProcessDetector()
	if err != nil {
		return nil, err
	}
	return func(content []byte) (string, bool) {
		hits := detector.ScanCertain(content)
		if len(hits) == 0 {
			return "", false
		}
		return string(hits[0].Class), true
	}, nil
}
