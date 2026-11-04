package learn

// Purpose: proposal contract types and structured failure signatures.
// Inputs: opaque repository ids and structured telemetry classifications.
// Outputs: proposal metadata and a deterministic SHA-256 signature.
// Constraints: authority types come from P1-LRN-01; no prompt text is read.
// SPORT: internal.learn.proposal/ADDED (P1-LRN-02a).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const (
	// MinOccurrences is the minimum number of supporting outcomes.
	MinOccurrences = 3
	// MinDistinctSources is the minimum number of supporting lanes.
	MinDistinctSources = 3
	// Window is the fixed maximum age of supporting outcomes.
	Window = 14 * 24 * time.Hour
)

// FailureSignature hashes the ordered tuple (repoID, component, taskClass,
// riskClass, family, category). Canonical encoding is a compact JSON string
// array, preserving field boundaries and exact values without normalization.
// Callers supply structured classifications and opaque ids, never prompt text.
func FailureSignature(repoID, component, taskClass, riskClass string, fam Family, cat Category) string {
	tuple := [6]string{repoID, component, taskClass, riskClass, string(fam), string(cat)}
	raw, _ := json.Marshal(tuple) // A fixed array of strings cannot fail to marshal.
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
