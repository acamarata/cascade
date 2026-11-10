package executive

import (
	"context"
	"fmt"

	"github.com/acamarata/cascade/internal/events"
)

// Stream identity for exec.* events. The executive never allocates a
// sequence: events.Bus stamps StreamSeq at publish time and recovers each
// stream head from its Store after a restart.
const (
	// ExecNamespace is the events.Bus namespace every exec.* event is published on.
	ExecNamespace = "exec"
	// ProjectStreamPrefix prefixes the stream of a project-scoped event with no run.
	ProjectStreamPrefix = "project:"
)

// DeriveStreamID returns the stream an exec.* event belongs to: run when it
// is non-empty, else ProjectStreamPrefix+project. Both empty is
// ErrNoStreamIdentity.
func DeriveStreamID(run RunID, project ProjectID) (StreamID, error) {
	if run != "" {
		return StreamID(run), nil
	}
	if project != "" {
		return StreamID(ProjectStreamPrefix + string(project)), nil
	}
	return "", fmt.Errorf("%w: run and project are both empty", ErrNoStreamIdentity)
}

// StreamHead returns the last StreamSeq the bus allocated on stream in
// ExecNamespace (0 for an unused stream). A nil bus or an empty stream is
// ErrNoStreamIdentity.
func StreamHead(ctx context.Context, bus *events.Bus, stream StreamID) (StreamSeq, error) {
	if bus == nil {
		return 0, fmt.Errorf("%w: nil bus", ErrNoStreamIdentity)
	}
	if stream == "" {
		return 0, fmt.Errorf("%w: empty stream", ErrNoStreamIdentity)
	}
	head, err := bus.StreamHead(ctx, ExecNamespace, string(stream))
	if err != nil {
		return 0, err
	}
	return StreamSeq(head), nil
}
