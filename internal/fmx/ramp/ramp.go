// Package ramp provides ramp metrics validation and stage metrics for flow control.
package ramp

import (
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/acamarata/cascade/internal/fmx"
)

// ErrInvalidRampMetrics is returned when ramp metrics contain invalid values.
var ErrInvalidRampMetrics = errors.New("invalid ramp metrics")

// ErrRampLog is returned when ramp log operation fails.
var ErrRampLog = errors.New("ramp log failed")

// Metrics represents ramp metrics for flow control.
// Percentages are in [0,100], not fractions.
// Queues is a nonnegative count.
type Metrics struct {
	HangsA     float64
	HangsB     float64
	Shedding   float64
	Queues     float64
	BudgetUsed float64
}

// StageMetrics represents stage-specific metrics for flow control.
// Percentages are in [0,100], not fractions.
// HangLine==0 means absent and defaults to 6.
// WorkWaiting means eligible work waiting beyond the current cap.
type StageMetrics struct {
	WorkWaiting  bool
	StreamsUsed  float64
	SlotsRunning float64
	HangsA       float64
	HangsB       float64
	HangLine     float64
	Code429      bool
	Overloaded   float64
	FailureShare float64
	BudgetUsed   float64
}

// validate checks the given metrics for validity and returns ErrInvalidRampMetrics
// if any values are invalid. It validates:
//   - Nonfinite numeric values (NaN, infinity)
//   - Percentages outside [0,100]
//   - Negative Queues
//   - Empty dataDir
//   - Nil u (including typed nil)
//   - Zero now time
func validate(m interface{}, dataDir string, u interface{}, now time.Time) error {
	// Check empty dataDir
	if dataDir == "" {
		return ErrInvalidRampMetrics
	}

	// Check nil u including typed nil
	if u == nil {
		return ErrInvalidRampMetrics
	}

	// Check for typed nil using reflection
	v := reflect.ValueOf(u)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return ErrInvalidRampMetrics
	}

	// Verify u implements fmx.UsageLog
	if _, ok := u.(fmx.UsageLog); !ok {
		return ErrInvalidRampMetrics
	}

	// Check zero now
	if now.IsZero() {
		return ErrInvalidRampMetrics
	}

	// Validate metrics based on type
	switch metrics := m.(type) {
	case *Metrics:
		if metrics == nil {
			return ErrInvalidRampMetrics
		}
		if err := validateMetrics(metrics); err != nil {
			return err
		}
	case *StageMetrics:
		if metrics == nil {
			return ErrInvalidRampMetrics
		}
		if err := validateStageMetrics(metrics); err != nil {
			return err
		}
	default:
		return ErrInvalidRampMetrics
	}

	return nil
}

// validateMetrics validates Metrics fields.
func validateMetrics(m *Metrics) error {
	// Check nonfinite values
	if math.IsNaN(m.HangsA) || math.IsInf(m.HangsA, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.HangsB) || math.IsInf(m.HangsB, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.Shedding) || math.IsInf(m.Shedding, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.Queues) || math.IsInf(m.Queues, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.BudgetUsed) || math.IsInf(m.BudgetUsed, 0) {
		return ErrInvalidRampMetrics
	}

	// Check percentages in [0,100]
	if m.Shedding < 0 || m.Shedding > 100 {
		return ErrInvalidRampMetrics
	}
	if m.BudgetUsed < 0 || m.BudgetUsed > 100 {
		return ErrInvalidRampMetrics
	}

	// Check nonnegative Queues
	if m.Queues < 0 {
		return ErrInvalidRampMetrics
	}

	return nil
}

// validateStageMetrics validates StageMetrics fields.
func validateStageMetrics(m *StageMetrics) error {
	// Check nonfinite values
	if math.IsNaN(m.StreamsUsed) || math.IsInf(m.StreamsUsed, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.SlotsRunning) || math.IsInf(m.SlotsRunning, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.HangsA) || math.IsInf(m.HangsA, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.HangsB) || math.IsInf(m.HangsB, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.HangLine) || math.IsInf(m.HangLine, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.Overloaded) || math.IsInf(m.Overloaded, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.FailureShare) || math.IsInf(m.FailureShare, 0) {
		return ErrInvalidRampMetrics
	}
	if math.IsNaN(m.BudgetUsed) || math.IsInf(m.BudgetUsed, 0) {
		return ErrInvalidRampMetrics
	}

	// Check percentages in [0,100]
	if m.Overloaded < 0 || m.Overloaded > 100 {
		return ErrInvalidRampMetrics
	}
	if m.FailureShare < 0 || m.FailureShare > 100 {
		return ErrInvalidRampMetrics
	}
	if m.BudgetUsed < 0 || m.BudgetUsed > 100 {
		return ErrInvalidRampMetrics
	}

	return nil
}
