package crunch

import "errors"

// RevertEvent describes a revert request that must stop the current stage
// run and mark the stage as aborting network activity.
type RevertEvent struct {
	// Reason explains why the revert happened. It may be empty.
	Reason string
}

var (
	// ErrM1454 reports that a revert was requested but no stage map is
	// available, so the system fails closed.
	ErrM1454 = errors.New("M1454: stage missing")

	// ErrRevertHalt reports that a valid revert was applied and the caller
	// must halt further processing.
	ErrRevertHalt = errors.New("M1454: revert requires halt")
)

// SetAbortNetOnRevert applies an in-memory revert to the stage map.
//
// If ev is nil, the stage is returned unchanged and no error is reported,
// even when stage is nil.
//
// If ev is non-nil and stage is nil, it returns nil and ErrM1454 so the
// missing stage fails closed.
//
// If ev is non-nil and stage is non-nil, it sets stage["abortNet"] = true on
// the supplied map, preserves all other keys, and returns the same map with
// ErrRevertHalt to signal that processing must halt. Persistence to stage.json
// is the caller's responsibility.
func SetAbortNetOnRevert(ev *RevertEvent, stage map[string]any) (map[string]any, error) {
	if ev == nil {
		return stage, nil
	}
	if stage == nil {
		return nil, ErrM1454
	}
	stage["abortNet"] = true
	return stage, ErrRevertHalt
}
