package relay

import "errors"

// ErrNotImplemented is returned when a relay operation is not yet implemented.
var ErrNotImplemented = errors.New("relay: operation unavailable")

// ErrNotConfigured is returned when the relay is not properly configured.
var ErrNotConfigured = errors.New("relay: not configured")

// ErrLogWriteFailed is returned when writing to the log fails.
var ErrLogWriteFailed = errors.New("relay: log write failed")

// ErrQuarantined is returned when a path has been quarantined.
var ErrQuarantined = errors.New("relay: quarantined")

// Planner is a function type for planning operations.
type Planner func()

// Scrub is a function type for scrubbing operations.
type Scrub func()

// Check is a function type for check operations.
type Check func()

// EgressGate is a function type for egress gate operations.
type EgressGate func()

// AskFunc is a function type that returns an error.
type AskFunc func() error

// TopicState represents the state of a topic.
type TopicState struct {
	Topic   string
	Corrupt bool
	Stuck   bool
}

// Relay represents a relay configuration with various function hooks.
type Relay struct {
	Planner    Planner
	Scrub      Scrub
	Check      Check
	EgressGate EgressGate
	Ask        AskFunc
	LogPath    string
}

// LoadState loads the topic state from the given path.
func LoadState(_ string) (*TopicState, error) {
	return nil, ErrNotImplemented
}

// SaveState saves the topic state to the given path.
func SaveState(_ string, ts *TopicState) error {
	return ErrNotImplemented
}

// Quarantine marks the given path as quarantined.
func Quarantine(_ string) error {
	return ErrNotImplemented
}
