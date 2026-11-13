// Package resilience provides types and interfaces for tracking process health.
package resilience

import "time"

// WaitKind represents the kind of wait condition a process is experiencing.
type WaitKind int

const (
	// WaitingDisk indicates the process is waiting for disk resources.
	WaitingDisk WaitKind = iota
	// WaitingNet indicates the process is waiting for network resources.
	WaitingNet
	// WaitingModels indicates the process is waiting for models to be available.
	WaitingModels
	// WaitingOwner indicates the process is waiting for the owner process.
	WaitingOwner
)

// Wait represents a wait condition with a start time and kind.
type Wait struct {
	Since time.Time
	Kind  WaitKind
}

// Restart tracks restart attempts and the last restart time.
type Restart struct {
	Count int
	Last  time.Time
}

// Event represents a timed event in the process lifecycle.
type Event struct {
	At time.Time
}

// Store defines the interface for persisting and querying process resilience state.
type Store interface {
	// SetWait records that the process is waiting for the given kind of resource.
	SetWait(kind WaitKind) error
	// ClearWait removes the wait condition for the given kind.
	ClearWait(kind WaitKind) error
	// RecordHeartbeat records a heartbeat signal.
	RecordHeartbeat() error
	// MarkSeenRunning marks the process as seen running.
	MarkSeenRunning() error
	// NextRestartDelay returns the delay before the next restart attempt.
	NextRestartDelay() time.Duration
	// RecordStall records a stall condition with the given reason.
	RecordStall(reason string) error
	// EmitEvent records a lifecycle event.
	EmitEvent(ev Event) error
	// TrimEvents removes old events to keep the store size bounded.
	TrimEvents() error
}
