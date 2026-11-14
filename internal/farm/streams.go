// Package farm defines stream planning data types.
package farm

import (
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// clock is the time source for stream planning.
var clock runtime.Clock = runtime.NewSystemClock()

// Binding identifies the constraint that limits a stream plan.
type Binding string

const (
	// BindingReady indicates that available ready work limits streams.
	BindingReady Binding = "ready"
	// BindingTarget indicates that the desired stream target limits streams.
	BindingTarget Binding = "target"
	// BindingCeiling indicates that the stream ceiling limits streams.
	BindingCeiling Binding = "ceiling"
	// BindingRPM indicates that the requests-per-minute limit constrains streams.
	BindingRPM Binding = "rpm"
)

// ActionType identifies the next step for a stream plan.
type ActionType int

const (
	// ActionStart starts streams immediately.
	ActionStart ActionType = iota
	// ActionWait defers starting streams until the specified time.
	ActionWait
	// ActionEscalate selects the specified escalation destination.
	ActionEscalate
)

// Action describes the next step for a stream plan.
type Action struct {
	// Type identifies the next step.
	Type ActionType
	// WaitUntil specifies when a waiting plan may resume.
	WaitUntil time.Time
	// EscalateTo identifies the escalation destination.
	EscalateTo string
}

// Plan records a stream count, its limiting constraint, and the next action.
type Plan struct {
	// Streams is the planned stream count.
	Streams int
	// Binding identifies the limiting constraint.
	Binding Binding
	// Action describes the next step.
	Action Action
}

// PlanStreams computes the optimal number of streams to start based on
// ready work, the target stream count (200), and the learned ceiling.
// When rpmUsed reaches rpmLimit it chooses between waiting for the next
// window and escalating to the next ladder rung. The returned Plan always
// carries a Start, Wait, or Escalate action; it never blocks.
func PlanStreams(ready, ceiling, rpmUsed, rpmLimit int, ladder []string) Plan {
	target := 200
	streams := ready
	binding := BindingReady

	if ceiling < streams {
		streams = ceiling
		binding = BindingCeiling
	}
	if target < streams {
		streams = target
		binding = BindingTarget
	}

	action := Action{Type: ActionStart}

	if rpmLimit > 0 && rpmUsed >= rpmLimit {
		binding = BindingRPM
		if len(ladder) > 1 {
			action.Type = ActionEscalate
			action.EscalateTo = ladder[1]
		} else {
			action.Type = ActionWait
			now := clock.Now()
			action.WaitUntil = now.Truncate(time.Minute).Add(time.Minute)
		}
	}

	return Plan{
		Streams: streams,
		Binding: binding,
		Action:  action,
	}
}
