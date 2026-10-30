// Purpose: the daemon's owner-only `ci` GET /events topic (T0 ruling
//   P1-BF-R298): the ci_results namespace, which carries the dispatcher's
//   ci.checkpoint.planned/dispatched/terminal/aborted events that `ci watch`
//   follows, and the owner-only ci.watch_replay_end method: the sequence of
//   the last stored watch event, so a client that replays the topic from
//   the start knows where the recorded history ends (T0 individual ruling).
// Inputs: the daemon's events.Bus and clock.
// Outputs: WatchTopic and its SSE handler, MethodWatchReplayEnd and its
//   handler; cmd/cascade/wire_ci_watch_topic.go mounts both.
// Constraints: owner-only and fail-closed, like fleet.capacity: the
//   events carry repo ids, tree hashes and sensitivity for every job and no
//   scope model filters them, so a principal caller is refused. The filter
//   accepts only the four checkpoint kinds. A store that cannot be read is
//   an error, never 0.
// SPORT: internal.ci watch topic (ADD, P1-CI-11, P1-BF-R298).

package ci

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// WatchTopic is the GET /events topic over the ci_results namespace.
const WatchTopic = "ci"

// MethodWatchReplayEnd answers {"end": <seq>}: the last stored watch
// event's sequence (0 when none).
const MethodWatchReplayEnd = "ci.watch_replay_end"

var (
	_ = rpc.DeclareTopicAccess(WatchTopic, rpc.AccessOwnerOnly)
	_ = rpc.DeclareAccess(MethodWatchReplayEnd, rpc.AccessOwnerOnly)
)

// isWatchKind reports whether kind is one of the four checkpoint kinds.
func isWatchKind(kind events.EventKind) bool {
	return kind == EventKindCheckpointPlanned || kind == EventKindCheckpointDispatched ||
		kind == EventKindCheckpointTerminal || kind == EventKindCheckpointAborted
}

// NewWatchSSEHandler returns the WatchTopic handler: bus's ci_results
// namespace, whose filter admits only the four checkpoint kinds.
func NewWatchSSEHandler(bus *events.Bus, clock runtime.Clock) *rpc.SSEHandler {
	return rpc.NewSSEHandler(bus, EventNamespace, isWatchKind, clock)
}

// WatchReplayEndHandler serves MethodWatchReplayEnd over bus.
func WatchReplayEndHandler(bus *events.Bus) rpc.HandlerFunc {
	return func(ctx context.Context, _ json.RawMessage) (any, error) {
		end, err := lastWatchSeq(ctx, bus)
		if err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "ci: watch: read the event store")
		}
		return map[string]uint64{"end": end}, nil
	}
}

// lastWatchSeq is the highest stored sequence of a watch kind.
func lastWatchSeq(ctx context.Context, bus *events.Bus) (uint64, error) {
	history, err := bus.Replay(ctx, EventNamespace, 0)
	if err != nil {
		return 0, err
	}
	var end uint64
	for _, ev := range history {
		if isWatchKind(ev.Kind) && ev.Seq > end {
			end = ev.Seq
		}
	}
	return end, nil
}
