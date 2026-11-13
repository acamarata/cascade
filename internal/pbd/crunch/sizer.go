package crunch

import "errors"

// ErrInvalidHealth is returned when an unrecognized or malformed health signal is provided.
var ErrInvalidHealth = errors.New("P2-DKT-08")

// Health represents a system health signal.
type Health string

// Supported health signals.
const (
	// HealthHealthy indicates the system is fully healthy and allows maximum concurrency.
	HealthHealthy Health = "healthy"
	// HealthDegraded indicates the system is degraded and allows reduced concurrency.
	HealthDegraded Health = "degraded"
	// HealthDown indicates the system is down and stops admission.
	HealthDown Health = "down"
)

// SizeConcurrency returns the allowed concurrency based on the provided health signal.
// It returns 4 for healthy, 2 for degraded, and 0 for down.
// Unrecognized, empty, padded, or case-variant signals return 0 and ErrInvalidHealth.
func SizeConcurrency(h Health) (int, error) {
	switch h {
	case HealthHealthy:
		return 4, nil
	case HealthDegraded:
		return 2, nil
	case HealthDown:
		return 0, nil
	default:
		return 0, ErrInvalidHealth
	}
}
