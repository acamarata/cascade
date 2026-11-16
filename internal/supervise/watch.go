// Package supervise provides supervisor functionality for monitoring and
// rebuilding based on git tree hash changes.
package supervise

// TreeHash represents a git tree hash used for rebuild decisions.
// Using tree hash instead of file mtime ensures that a supervisor started
// after a merge builds the merged commit without requiring a file touch.
type TreeHash string

// ComputeTreeHash computes the git tree hash for the given directory path.
// This function is implemented in P2-COP-24.
func ComputeTreeHash(dir string) (TreeHash, error) {
	return "", nil
}

// NeedsRebuild determines whether a rebuild is necessary by comparing the
// current tree hash with the stored tree hash. A rebuild is needed when:
//   - The hashes are different
//   - The stored hash is empty (first run or no previous state)
//
// When both hashes are empty, no rebuild is needed.
func NeedsRebuild(currentHash, storedHash TreeHash) bool {
	if currentHash == "" && storedHash == "" {
		return false
	}
	return currentHash != storedHash
}
