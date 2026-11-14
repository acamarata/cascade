// Package flow implements a simple state machine for GC lifecycle events.
package flow

import (
	"errors"
	"reflect"
	"time"
)

// ErrInvalidClock is returned when the clock provided to New is nil.
var ErrInvalidClock = errors.New("invalid clock")

// ErrIllegalEvent is returned when an event is not valid for the current state.
var ErrIllegalEvent = errors.New("illegal event")

// ErrInvalidStart is returned when a Start event has a zero At time.
var ErrInvalidStart = errors.New("invalid start")

// Clock provides the current time.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
}

// State represents the current state of the machine.
type State string

const (
	// Idle is the initial state.
	Idle State = "Idle"
	// Running indicates the machine has started.
	Running State = "Running"
	// Done indicates successful completion.
	Done State = "Done"
	// Failed indicates a failure.
	Failed State = "Failed"
)

// EventKind identifies the type of event.
type EventKind string

const (
	// Start begins the machine.
	Start EventKind = "start"
	// Tick advances time.
	Tick EventKind = "tick"
	// Finish completes the machine.
	Finish EventKind = "finish"
)

// Event is an input to the state machine.
type Event struct {
	// Kind is the type of event.
	Kind EventKind
	// At is the timestamp of the event.
	At time.Time
	// OK indicates success or failure context.
	OK bool
}

// Action is an output produced by a transition.
type Action struct {
	// Kind describes the action.
	Kind string
	// Elapsed is the duration associated with the action.
	Elapsed time.Duration
}

// Machine is a state machine driven by events.
type Machine struct {
	state       State
	clock       Clock
	lastElapsed time.Duration
}

// New creates a new Machine in the Idle state. It returns ErrInvalidClock if c is nil or typed-nil.
func New(c Clock) (*Machine, error) {
	if c == nil {
		return nil, ErrInvalidClock
	}
	v := reflect.ValueOf(c)
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return nil, ErrInvalidClock
	}
	return &Machine{state: Idle, clock: c}, nil
}

// Step processes an event and returns the new state, any actions, and an error.
func (m *Machine) Step(e Event) (State, []Action, error) {
	switch m.state {
	case Done, Failed:
		return m.state, nil, ErrIllegalEvent
	case Idle:
		if e.Kind != Start {
			return m.state, nil, ErrIllegalEvent
		}
		if e.At.IsZero() {
			return m.state, nil, ErrInvalidStart
		}
		m.state = Running
		return m.state, []Action{{Kind: "start", Elapsed: 0}}, nil
	case Running:
		if e.Kind == Start {
			return m.state, nil, ErrIllegalEvent
		}
		if e.Kind == Finish {
			if e.OK {
				m.state = Done
			} else {
				m.state = Failed
			}
			return m.state, []Action{{Kind: "finish", Elapsed: m.lastElapsed}}, nil
		}
		return m.state, nil, ErrIllegalEvent
	default:
		return m.state, nil, ErrIllegalEvent
	}
}
