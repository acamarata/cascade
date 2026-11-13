// Package farm defines stream planning data types.
package farm

import "time"

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
