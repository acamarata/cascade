package jobs

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CriticalInFlight reports whether any job whose stored RiskClass is
// "critical" is currently in leased, running, verifying or reviewing
// state. The learn router uses this as a JobGate: when true or on error,
// the router defers new critical work until the existing critical jobs
// clear. A closed store surfaces an error (never false), matching the
// fail-closed policy the router requires.
func (s *Store) CriticalInFlight(ctx context.Context) (bool, error) {
	const q = `SELECT COUNT(1) FROM ` + tableJob + ` WHERE risk_class = ? AND state IN (?, ?, ?, ?)`
	var n int
	err := s.db.QueryRowContext(ctx, q,
		"critical",
		string(JobStateLeased),
		string(JobStateRunning),
		string(JobStateVerifying),
		string(JobStateReviewing),
	).Scan(&n)
	if err != nil {
		return false, cascade.Wrap(cascade.KindUnavailable, err, "jobs: critical in flight")
	}
	return n > 0, nil
}
