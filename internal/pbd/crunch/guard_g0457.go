// Package crunch provides execution environment guards.
package crunch

import (
	"errors"
	"time"
)

// ErrQuotaFloor is returned when a quota check denies admission.
var ErrQuotaFloor = errors.New("quota floor")

// ErrCooldown is returned when a lane is still cooling down.
var ErrCooldown = errors.New("lane cooldown")

// ErrInvalidGuard is returned for invalid guard inputs such as nil receiver or empty lane.
var ErrInvalidGuard = errors.New("invalid guard input")

// ExecEnv holds the execution environment state including time source, quota checker, and cooldown tracking.
type ExecEnv struct {
	// Now returns the current time. It is called once per valid Guard operation.
	Now func() time.Time
	// Quota checks whether the given lane has available quota. Nil return admits; non-nil denies.
	Quota func(lane string) error
	// cooldownUntil tracks per-lane cooldown deadlines.
	cooldownUntil map[string]time.Time
}

// ReportStatus records an HTTP response status for the given lane.
// A 429 status triggers a 10-minute cooldown for that lane, extending any existing deadline.
// Other valid statuses (100..599) are no-ops and never clear or shorten cooldown.
// Returns ErrInvalidGuard if the receiver is nil, the lane is empty, Now/Quota are nil, or status is outside 100..599.
func (e *ExecEnv) ReportStatus(lane string, status int) error {
	if e == nil || lane == "" || e.Now == nil || e.Quota == nil {
		return ErrInvalidGuard
	}
	if status < 100 || status > 599 {
		return ErrInvalidGuard
	}

	if status == 429 {
		if e.cooldownUntil == nil {
			e.cooldownUntil = make(map[string]time.Time)
		}
		now := e.Now()
		newDeadline := now.Add(10 * time.Minute)
		if existing, ok := e.cooldownUntil[lane]; ok && existing.After(newDeadline) {
			e.cooldownUntil[lane] = existing
		} else {
			e.cooldownUntil[lane] = newDeadline
		}
	}

	return nil
}

// Guard checks quota and cooldown for the specified lane before admitting execution.
// It returns ErrInvalidGuard if the receiver is nil, the lane is empty, or Now/Quota are nil.
// Quota is always called exactly once before any cooldown check.
// A quota error wraps ErrQuotaFloor with the underlying error via errors.Join.
// If the lane is in cooldown, ErrCooldown is returned.
func (e *ExecEnv) Guard(lane string) error {
	if e == nil || lane == "" || e.Now == nil || e.Quota == nil {
		return ErrInvalidGuard
	}

	now := e.Now()

	quotaErr := e.Quota(lane)
	if quotaErr != nil {
		return errors.Join(ErrQuotaFloor, quotaErr)
	}

	if e.cooldownUntil != nil {
		if deadline, ok := e.cooldownUntil[lane]; ok && now.Before(deadline) {
			return ErrCooldown
		}
	}

	return nil
}
