package graph

import "time"

// MaxVerdictStaleness is the maximum age allowed for a node readiness
// verdict before it is considered stale.
const MaxVerdictStaleness = 60 * time.Second

// VerdictFresh reports whether a node readiness verdict reported at
// reportedAt is still fresh at now.
//
// A verdict aged MaxVerdictStaleness or less is fresh. A verdict older
// than MaxVerdictStaleness is stale. A future reportedAt timestamp is
// treated as fresh because only the verdict age is checked.
func VerdictFresh(reportedAt, now time.Time) bool {
	return now.Sub(reportedAt) <= MaxVerdictStaleness
}
