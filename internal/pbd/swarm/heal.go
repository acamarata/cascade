// Package swarm provides stale-run healing for the autoheal pipeline.
package swarm

import (
	"errors"
	"time"
)

// ErrNoCurrentMain is returned when HealConfig.CurrentMain is empty.
var ErrNoCurrentMain = errors.New("swarm: no current main branch configured")

// ErrEmptyRunName is returned when a StaleRun has an empty RunName.
var ErrEmptyRunName = errors.New("swarm: empty run name")

// StaleRun describes a run that requires healing. Reasons include
// stale-base, supervisor-refused, and no-file-change.
type StaleRun struct {
	// RunName is the identifier of the stale run.
	RunName string
	// Reason indicates why the run is stale (stale-base, supervisor-refused, no-file-change).
	Reason string
	// AttemptCount tracks how many heal attempts have been made.
	AttemptCount int
	// Timestamp records when the staleness was detected.
	Timestamp time.Time
}

// HealConfig controls the behaviour of the heal process.
// DryRun defaults to true so that changes are previewed before application.
type HealConfig struct {
	// DryRun when true prevents actual mutations during healing.
	DryRun bool
	// CurrentMain is the reference branch used to detect stale bases.
	CurrentMain string
}

// DefaultHealConfig returns a HealConfig with DryRun set to true.
func DefaultHealConfig() HealConfig {
	return HealConfig{
		DryRun: true,
	}
}

// HealResult captures the outcome of a single heal operation.
type HealResult struct {
	// RenamedTo holds the new run name if the run was renamed.
	RenamedTo string
	// ReopenedTickets lists tickets that were reopened as part of healing.
	ReopenedTickets []string
	// CarriedAttempts is the number of attempts carried forward.
	CarriedAttempts int
	// Applied indicates whether the heal was actually applied (false in dry-run).
	Applied bool
}
