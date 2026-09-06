// Purpose: the patterns-loaded and quarantine-depth halves of the secrets
//
//	doctor checks, split from doctor_checks.go under the repo's 300-line
//	file cap. quarantine-depth is the one fixable check of the five: its
//	Fix flushes the pending queue.
//
// Inputs: the shared DoctorCheckDeps.
// Outputs: doctor.CheckResult and, for the flush, doctor.FixResult
//
//	carrying a count. A quarantine record's flagged value was never
//	stored, so nothing here can reach one.
//
// Constraints: an empty pattern library is an error, not a pass - a
//
//	detector with no patterns reports every payload clean, which is the
//	most dangerous possible false green (Art.1). A flush on an empty queue
//	is success with Applied=false, per the idempotency rule.
//
// SPORT: SECRETS_DOCTOR_CHECKS: ADD (patterns-loaded, quarantine-depth).

package secrets

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/internal/doctor"
)

// quarantineFlushReason is recorded against every entry the doctor's fix
// retires, so a release stays accounted for in the ledger.
const quarantineFlushReason = "flushed by cascade doctor --fix"

// patternsLoadedCheck asserts the detector has a non-empty pattern
// library.
type patternsLoadedCheck struct{ deps DoctorCheckDeps }

func (patternsLoadedCheck) Name() string { return "secrets/patterns-loaded" }

func (patternsLoadedCheck) Describe() string {
	return "asserts the secret detector loaded a non-empty pattern library"
}

func (patternsLoadedCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: true, Fixable: false}
}

func (patternsLoadedCheck) Fix(context.Context) (doctor.FixResult, error) {
	return doctor.FixResult{}, doctor.ErrCheckNotFixable
}

// Run counts the loaded patterns. Zero is an error: a detector with no
// patterns finds nothing and therefore reports every payload clean, which
// reads to an operator exactly like a payload that really is clean.
func (c patternsLoadedCheck) Run(context.Context) (doctor.CheckResult, error) {
	loaded := len(c.deps.Detector.registry.Patterns())
	if loaded == 0 {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "the secret detector has no patterns loaded",
			Detail:      "a detector with an empty pattern library reports every payload clean",
			Remediation: "restore the default pattern registry, or correct the [secrets.detection] section of config.toml",
		}, nil
	}
	return doctor.CheckResult{
		Status:  doctor.StatusOK,
		Message: fmt.Sprintf("%d detector pattern(s) loaded", loaded),
	}, nil
}

// quarantineDepthCheck reports the pending quarantine queue's depth.
type quarantineDepthCheck struct{ deps DoctorCheckDeps }

func (quarantineDepthCheck) Name() string { return "secrets/quarantine-depth" }

func (quarantineDepthCheck) Describe() string {
	return "reports how many detections are waiting in the quarantine queue, against the configured threshold"
}

func (quarantineDepthCheck) Metadata() doctor.CheckMeta {
	return doctor.CheckMeta{FirstRun: false, Fixable: true}
}

// Run reads the queue depth. It discloses a count, never a record.
func (c quarantineDepthCheck) Run(context.Context) (doctor.CheckResult, error) {
	depth, err := c.deps.Quarantine.PendingCount()
	if err != nil {
		return doctor.CheckResult{
			Status:      doctor.StatusError,
			Message:     "could not read the quarantine queue",
			Detail:      err.Error(),
			Remediation: "check that the quarantine ledger under the data directory is readable",
		}, nil
	}
	if depth > c.deps.QuarantineThreshold {
		return doctor.CheckResult{
			Status:      doctor.StatusWarn,
			Message:     fmt.Sprintf("%d quarantined detection(s), above the threshold of %d", depth, c.deps.QuarantineThreshold),
			Remediation: "review them with `cascade vault quarantine list`, or flush them with `cascade doctor --fix`",
		}, nil
	}
	return doctor.CheckResult{
		Status:  doctor.StatusOK,
		Message: fmt.Sprintf("%d quarantined detection(s), threshold %d", depth, c.deps.QuarantineThreshold),
	}, nil
}

// Fix flushes the pending queue. Each entry is released with a recorded
// reason, so the ledger still accounts for every exit from quarantine
// rather than losing the record.
//
// An already-empty queue is success with Applied=false and no delta: Fix
// observing "already correct" is not an error, and a second --fix run
// must not claim to have changed something.
func (c quarantineDepthCheck) Fix(context.Context) (doctor.FixResult, error) {
	entries, err := c.deps.Quarantine.List()
	if err != nil {
		return doctor.FixResult{}, err
	}
	if len(entries) == 0 {
		return doctor.FixResult{Applied: false}, nil
	}
	flushed := 0
	for _, entry := range entries {
		if derr := c.deps.Quarantine.Delete(entry.ID, quarantineFlushReason); derr != nil {
			return doctor.FixResult{Applied: flushed > 0, Delta: fmt.Sprintf("flushed %d of %d quarantined detection(s)", flushed, len(entries))}, derr
		}
		flushed++
	}
	return doctor.FixResult{
		Applied: true,
		Delta:   fmt.Sprintf("flushed %d quarantined detection(s)", flushed),
	}, nil
}
