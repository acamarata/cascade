// Package write provides control plane write operations.
package write

import "errors"

// RunConfig holds the configuration for a run operation.
type RunConfig struct {
	Context       string
	NoVerifyNodes bool
}

// ErrNoVerifyNodesForbidden is returned when noVerifyNodes is used in a forbidden context.
var ErrNoVerifyNodesForbidden = errors.New("M1098")

// ValidateRunConfig validates the run configuration and rejects noVerifyNodes during restart or tuning.
func ValidateRunConfig(cfg RunConfig) error {
	if cfg.NoVerifyNodes && (cfg.Context == "restart" || cfg.Context == "tuning") {
		return ErrNoVerifyNodesForbidden
	}
	return nil
}
