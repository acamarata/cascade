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

// V2BigModelCap is the hard cap for big model requests under v2 policy.
const V2BigModelCap = 0

// ErrInvalidModelSize is returned when an unrecognized or malformed model size is provided.
var ErrInvalidModelSize = errors.New("invalid model size")

// ErrBigModelCap is returned when a big model request exceeds the v2 hard cap.
var ErrBigModelCap = errors.New("P2-CRUN-05")

// CheckBigModel enforces the BigModelCapper policy.
// It accepts only exact modelSize "small" or "big"; all other or empty values return ErrInvalidModelSize.
// For v2=true, any big request returns ErrBigModelCap and small returns nil.
// For v2=false, either valid model size returns nil.
func CheckBigModel(modelSize string, v2 bool) error {
	if modelSize != "small" && modelSize != "big" {
		return ErrInvalidModelSize
	}
	if v2 && modelSize == "big" {
		return ErrBigModelCap
	}
	return nil
}
