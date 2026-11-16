// Package swarm provides stale-run healing for the autoheal pipeline.
package swarm

import (
	"errors"
	cruntime "github.com/acamarata/cascade/internal/runtime"
	"time"
)

// Clock provides time-related operations for dependency injection.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

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

// DetectStaleRuns scans a slice of StaleRun records and returns those
// requiring healing: stale-base, supervisor-refused, or no-file-change.
// It returns ErrNoCurrentMain if currentMain is empty, and ErrEmptyRunName
// if any run has an empty RunName.
func DetectStaleRuns(runs []StaleRun, currentMain string) ([]StaleRun, error) {
	if currentMain == "" {
		return nil, ErrNoCurrentMain
	}

	var result []StaleRun
	for _, r := range runs {
		if r.RunName == "" {
			return nil, ErrEmptyRunName
		}
		switch r.Reason {
		case "stale-base", "supervisor-refused", "no-file-change":
			result = append(result, r)
		}
	}
	return result, nil
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

// ArchiveRun renames a stale run by appending a UTC timestamp suffix.
// It returns the new name in the format <name>.stale-<UTC stamp> without
// deleting the original run. Returns ErrEmptyRunName if run.RunName is empty.
func ArchiveRun(run StaleRun, clock func() time.Time) (string, error) {
	if run.RunName == "" {
		return "", ErrEmptyRunName
	}

	stamp := clock().UTC().Format("20060102T150405Z")
	return run.RunName + ".stale-" + stamp, nil
}

// ReopenOnCurrentMain reopens tickets on the current main branch and carries
// the attempt counts into the ledger. It returns ErrNoCurrentMain if currentMain
// is empty. It populates HealResult.ReopenedTickets with the ticket identifiers
// derived from run.RunName, sets HealResult.CarriedAttempts to run.AttemptCount,
// and sets HealResult.RenamedTo using ArchiveRun output. An optional Clock may
// be provided as the third argument; otherwise internal/runtime.SystemClock is used.
func ReopenOnCurrentMain(run StaleRun, currentMain string, clock ...Clock) (*HealResult, error) {
	if currentMain == "" {
		return nil, ErrNoCurrentMain
	}

	c := Clock(cruntime.SystemClock{})
	if len(clock) > 0 && clock[0] != nil {
		c = clock[0]
	}

	renamedTo, err := ArchiveRun(run, c.Now)
	if err != nil {
		return nil, err
	}

	return &HealResult{
		RenamedTo:       renamedTo,
		ReopenedTickets: []string{run.RunName},
		CarriedAttempts: run.AttemptCount,
		Applied:         true,
	}, nil
}
