// Purpose: `cascade ci watch <job-id>`: follows one job's
//   ci.checkpoint.planned/dispatched/terminal events and exits once the
//   job's latest acceptance re-run is terminal for every kind its plan
//   event lists. The stream arrives through CmdDeps.WatchEvents, which the
//   ci mount file binds to (*client.Client).JobEvents; this package never
//   imports internal/client (ci -> client -> daemon -> ci would cycle).
// Inputs: a job id; the CIEventFrame channel WatchEvents opens. The
//   stream first replays the stored history, then sends one
//   watchReplayedEvent frame, then follows live events.
// Outputs: one line (or NDJSON record) per frame of that job; the exit
//   code: 0 when every planned kind is terminal and passed, 1
//   (KindInternal) when a planned kind failed or aborted, the stream ends first or a
//   frame cannot be read, 5 (KindUnavailable) when no stream is bound.
// Constraints: fail closed (T0 individual ruling on P1-CI-11). Only
//   acceptance=true frames count, and only the plan event defines the
//   kinds: the dispatcher runs kinds in parallel, so dispatched events say
//   nothing about what is still to start. No verdict is taken while the
//   history replays, so an older acceptance run never answers for a newer
//   one; at the end of the replay a run that already finished is reported
//   at once, so a late watch never waits forever.
// SPORT: internal.ci watch (ADD, P1-CI-11).

package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
)

// watchReplayedEvent marks the end of the replayed history. Its literal is
// duplicated in internal/client/job_events.go, which may not import ci.
const watchReplayedEvent = "ci.watch.replayed"

// CIEventFrame is one event of a job's CI stream: its kind and its JSON
// payload, as the daemon published it on the ci_results namespace.
type CIEventFrame struct { //nolint:revive // contract:ci-cli-attach names it CIEventFrame (exact)
	Event string
	Data  []byte
}

// watchFrame is the part of the dispatcher's payload the watch reads. Its
// tags match streamEventPayload (stream_dispatch.go).
type watchFrame struct {
	Event        string   `json:"event"`
	JobID        string   `json:"job_id"`
	CheckpointID string   `json:"checkpoint_id"`
	Kind         string   `json:"kind,omitempty"`
	Kinds        []string `json:"kinds,omitempty"`
	Acceptance   bool     `json:"acceptance"`
	Passed       bool     `json:"passed"`
	Error        string   `json:"error,omitempty"`
}

// watchState is the watch's view of the job's acceptance runs.
type watchState struct {
	plans    map[string]map[string]*bool // checkpoint -> planned kind -> passed (nil: running)
	latest   string                      // newest planned acceptance checkpoint
	replayed bool
}

// newCIWatchCmd builds `ci watch <job-id>`.
func newCIWatchCmd(deps CmdDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "watch <job-id>",
		Short: "Follow a job's CI checkpoint events until its acceptance re-run is terminal",
		Args:  exactlyOneJobID,
		RunE: func(cmd *cobra.Command, args []string) error {
			if deps.WatchEvents == nil {
				return cascade.New(cascade.KindUnavailable,
					"ci watch: no daemon event stream is bound; start `cascade daemon run` and use the cascade binary")
			}
			frames, closeStream, err := deps.WatchEvents(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			defer closeStream()
			return watchAcceptance(cmd.Context(), frames, args[0], outputWriter(cmd))
		},
	}
}

// exactlyOneJobID refuses anything but one non-blank job id, as
// KindInvalidInput.
func exactlyOneJobID(_ *cobra.Command, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return cascade.New(cascade.KindInvalidInput, "ci watch: exactly one job id is required")
	}
	return nil
}

// watchAcceptance reads frames until the job's latest acceptance plan is
// decided.
func watchAcceptance(ctx context.Context, frames <-chan CIEventFrame, jobID string, w *output.Writer) error {
	st := &watchState{plans: map[string]map[string]*bool{}}
	for {
		select {
		case <-ctx.Done():
			return cascade.Wrap(cascade.KindCanceled, ctx.Err(), "ci watch: canceled")
		case raw, open := <-frames:
			if !open {
				return cascade.Newf(cascade.KindInternal,
					"ci watch: the event stream for job %s ended before its acceptance re-run was terminal", jobID)
			}
			if raw.Event == watchReplayedEvent {
				st.replayed = true
			} else if err := st.apply(raw, jobID, w); err != nil {
				return err
			}
			if done, err := st.verdict(jobID); done {
				return err
			}
		}
	}
}

// apply decodes, prints and records one frame.
func (st *watchState) apply(raw CIEventFrame, jobID string, w *output.Writer) error {
	f, ok, err := decodeWatchFrame(raw, jobID)
	if err != nil || !ok {
		return err
	}
	if err := emitWatchFrame(w, f); err != nil {
		return err
	}
	if !f.Acceptance {
		return nil
	}
	switch f.Event {
	case string(EventKindCheckpointPlanned):
		plan := map[string]*bool{}
		for _, k := range f.Kinds {
			plan[k] = nil
		}
		st.plans[f.CheckpointID], st.latest = plan, f.CheckpointID
	case string(EventKindCheckpointTerminal), string(EventKindCheckpointAborted):
		plan, known := st.plans[f.CheckpointID]
		if !known {
			return nil // a run whose plan predates the stored history
		}
		if _, planned := plan[f.Kind]; !planned {
			return cascade.Newf(cascade.KindInternal, "ci watch: terminal for kind %s is outside checkpoint %s's plan", f.Kind, f.CheckpointID)
		}
		passed := f.Passed && f.Event == string(EventKindCheckpointTerminal)
		plan[f.Kind] = &passed
	}
	return nil
}

// verdict reports whether the latest plan is decided and its exit error.
func (st *watchState) verdict(jobID string) (bool, error) {
	if !st.replayed || st.latest == "" {
		return false, nil
	}
	var failed []string
	for kind, passed := range st.plans[st.latest] {
		if passed == nil {
			return false, nil
		}
		if !*passed {
			failed = append(failed, kind)
		}
	}
	if len(failed) == 0 {
		return true, nil
	}
	slices.Sort(failed)
	return true, cascade.Newf(cascade.KindInternal, "ci watch: job %s acceptance re-run %s failed: %s",
		jobID, st.latest, strings.Join(failed, ", "))
}

// decodeWatchFrame decodes raw. ok is false for a frame of another job or
// another event. A frame of this job that cannot be read is an error.
func decodeWatchFrame(raw CIEventFrame, jobID string) (watchFrame, bool, error) {
	if !isWatchKind(events.EventKind(raw.Event)) {
		return watchFrame{}, false, nil
	}
	var f watchFrame
	if err := json.Unmarshal(raw.Data, &f); err != nil {
		return watchFrame{}, false, cascade.Wrapf(cascade.KindInternal, err, "ci watch: unreadable %s frame", raw.Event)
	}
	if f.JobID != jobID {
		return watchFrame{}, false, nil
	}
	f.Event = raw.Event
	if f.CheckpointID == "" || !validKinds(f) {
		return watchFrame{}, false, cascade.Newf(cascade.KindInternal,
			"ci watch: %s frame for job %s lacks a checkpoint or known kinds", raw.Event, jobID)
	}
	return f, true, nil
}

// validKinds checks a plan's non-empty kind list, or another frame's kind.
func validKinds(f watchFrame) bool {
	if f.Event != string(EventKindCheckpointPlanned) {
		return RequirementKind(f.Kind).Valid()
	}
	if len(f.Kinds) == 0 {
		return false
	}
	for _, k := range f.Kinds {
		if !RequirementKind(k).Valid() {
			return false
		}
	}
	return true
}

// emitWatchFrame writes f as one human line or one NDJSON record.
func emitWatchFrame(w *output.Writer, f watchFrame) error {
	if w.Mode().JSON {
		return w.NDJSON().Emit(f)
	}
	line := fmt.Sprintf("%s checkpoint=%s kind=%s acceptance=%t", f.Event, f.CheckpointID, f.Kind, f.Acceptance)
	switch f.Event {
	case string(EventKindCheckpointPlanned):
		line = fmt.Sprintf("%s checkpoint=%s kinds=%s acceptance=%t", f.Event, f.CheckpointID, strings.Join(f.Kinds, ","), f.Acceptance)
	case string(EventKindCheckpointTerminal), string(EventKindCheckpointAborted):
		line += fmt.Sprintf(" passed=%t", f.Passed)
		if f.Error != "" {
			line += fmt.Sprintf(" error=%q", f.Error)
		}
	}
	w.Println(line)
	return nil
}
