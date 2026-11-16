// Package setup defines the types and contracts for the application setup flow.
package setup

import (
	"context"
	"errors"
)

// ErrSignInFailed is returned when the external sign-in step fails.
var ErrSignInFailed = errors.New("setup: sign-in failed")

// ErrOllamaStartFailed is returned when the Ollama service fails to start.
var ErrOllamaStartFailed = errors.New("setup: ollama start failed")

// ErrBridgeStartFailed is returned when the bridge service fails to start.
var ErrBridgeStartFailed = errors.New("setup: bridge start failed")

// ErrGrantApplyFailed is returned when applying the grant fails.
var ErrGrantApplyFailed = errors.New("setup: grant apply failed")

// State tracks the progress of each setup step.
type State struct {
	// SignIn indicates whether the external sign-in window completed successfully.
	SignIn bool
	// OllamaStarted indicates whether the Ollama service has started.
	OllamaStarted bool
	// BridgeStarted indicates whether the bridge service has started.
	BridgeStarted bool
	// GrantApplied indicates whether the standing grant was applied.
	GrantApplied bool
	// DoctorReady indicates whether the doctor check passed.
	DoctorReady bool
	// Ready is true when all setup steps have completed successfully.
	Ready bool
}

// Flow manages the setup state machine and exposes methods to control it.
type Flow struct {
	state State
}

// NewFlow creates a new Flow with an initial empty state.
func NewFlow() *Flow {
	return &Flow{}
}

// Start begins the setup flow from the beginning.
// The sign-in step triggers an external window and waits for completion;
// no credentials are typed by any agent code path.
func (f *Flow) Start(ctx context.Context) error {
	f.state = State{}
	return nil
}

// Resume continues the setup flow from the current state.
func (f *Flow) Resume(ctx context.Context) error {
	return nil
}

// Status returns the current state of the setup flow.
func (f *Flow) Status() State {
	return f.state
}
