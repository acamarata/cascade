// Package governor (escalation_seams.go) defines the five interfaces
// EscalationLadder.Advance calls through (P1-E13-W3-S27-T3). Each is
// injectable, has no direct coupling to its runtime satisfier's package,
// and is stubbed only in _test.go files per Art.1 (no stub seam
// implementations ship in a non-test file). Composition — wiring a real
// satisfier into a *EscalationLadder — happens at the daemon entry point,
// outside this ticket's files_scope.
package governor

import "context"

// ConfidenceProvider reports how confident the caller should be that
// entityID is making progress on its own, in [0, 1]. Advance treats a
// reading at or above EscalationPolicy.ConfidenceThreshold as "not stuck"
// and a reading below it, or an error, as stuck (fail closed — an error
// never silently skips escalation). Satisfied at runtime by the S-25.T1
// fleet session state machine.
type ConfidenceProvider interface {
	// Confidence returns entityID's current confidence reading.
	Confidence(ctx context.Context, entityID string) (float64, error)
}

// Retryer re-attempts entityID's stuck work with no additional context.
// Satisfied at runtime by the admission controller (S-26.T2).
type Retryer interface {
	// Retry re-attempts entityID's work.
	Retry(ctx context.Context, entityID string) error
}

// ContextEnricher adds context to entityID before a further retry, and
// reports how much it added. Satisfied at composition root by the
// retrieval/context package.
type ContextEnricher interface {
	// Enrich adds context to entityID and reports the amount added.
	Enrich(ctx context.Context, entityID string) (added int, err error)
}

// SupervisorCreator creates a supervisor task that monitors entityID, and
// reports the created task's identifier. Satisfied at composition root by
// the task/PBD engine.
type SupervisorCreator interface {
	// CreateSupervisor creates a supervisor task for entityID.
	CreateSupervisor(ctx context.Context, entityID string) (supervisorID string, err error)
}

// HumanNotifier alerts a human operator about event, the ladder's terminal
// rung. Satisfied at composition root by internal/notify.
type HumanNotifier interface {
	// Notify delivers event to a human operator.
	Notify(ctx context.Context, event EscalationEvent) error
}
