// Package executive defines the closed exec.* event kind vocabulary.
package executive

import (
	"errors"
	"fmt"
)

// Kind is a closed executive event kind.
type Kind string

// Payload holds the raw event payload bytes.
type Payload []byte

// PayloadVersion identifies the schema version of a payload.
type PayloadVersion int

// Producer identifies the component that produced an event.
type Producer string

// ProjectID identifies the project scope of an event.
type ProjectID string

// RunID identifies the run scope of an event.
type RunID string

// StreamID identifies the stream scope of an event.
type StreamID string

// StreamSeq is the server-allocated per-stream monotonic sequence number.
type StreamSeq uint64

const (
	// KindTaskStarted signals that a task has started.
	KindTaskStarted Kind = "exec.task.started"
	// KindTaskCompleted signals that a task completed successfully.
	KindTaskCompleted Kind = "exec.task.completed"
	// KindTaskFailed signals that a task failed.
	KindTaskFailed Kind = "exec.task.failed"
	// KindRunStarted signals that a run has started.
	KindRunStarted Kind = "exec.run.started"
	// KindRunCompleted signals that a run completed successfully.
	KindRunCompleted Kind = "exec.run.completed"
	// KindRunFailed signals that a run failed.
	KindRunFailed Kind = "exec.run.failed"
)

// AllKinds is the closed R-21.37 list of every valid exec.* kind.
var AllKinds = []Kind{
	KindTaskStarted,
	KindTaskCompleted,
	KindTaskFailed,
	KindRunStarted,
	KindRunCompleted,
	KindRunFailed,
}

// ErrUnknownExecKind is returned when a kind string is unset or not in AllKinds.
var ErrUnknownExecKind = errors.New("unknown exec kind")

// ParseExecKind parses s into a Kind, returning ErrUnknownExecKind for unset or unknown values.
func ParseExecKind(s string) (Kind, error) {
	k := Kind(s)
	if k == "" {
		return "", fmt.Errorf("%w: empty", ErrUnknownExecKind)
	}
	for _, v := range AllKinds {
		if v == k {
			return k, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownExecKind, s)
}
