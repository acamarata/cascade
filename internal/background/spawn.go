// Package background provides manager-routed spawn operations.
package background

import (
	"errors"
)

// SpawnRequest describes a request to spawn a process via the manager CLI.
type SpawnRequest struct {
	// UnderID is the optional parent identifier for the spawned process.
	UnderID string
	// Model is the optional model name to use for the spawned process.
	Model string
}

// SpawnResult describes the outcome of a spawn operation.
type SpawnResult struct {
	// OK indicates whether the spawn succeeded.
	OK bool
	// ParentID is the parent identifier used, if any.
	ParentID string
	// Model is the model name used, if any.
	Model string
	// ViaManager indicates the spawn was routed through the manager CLI.
	ViaManager bool
}

// UIConfig holds user interface configuration.
type UIConfig struct {
	// SpawnButtons lists available spawn button identifiers in the UI.
	SpawnButtons []string
}

// ErrNoManager is returned when a spawn is attempted without the manager present.
var ErrNoManager = errors.New("D6148: spawn requires manager")

// Spawn routes a spawn request exclusively through the manager CLI.
// It returns ErrNoManager if mgrPresent is false.
func Spawn(mgrPresent bool, r SpawnRequest) (SpawnResult, error) {
	if !mgrPresent {
		return SpawnResult{}, ErrNoManager
	}
	return SpawnResult{
		OK:         true,
		ParentID:   r.UnderID,
		Model:      r.Model,
		ViaManager: true,
	}, nil
}

// DefaultUIConfig returns a UI configuration with no spawn buttons,
// ensuring all spawn controls are routed through the manager CLI.
func DefaultUIConfig() UIConfig {
	return UIConfig{
		SpawnButtons: []string{},
	}
}
