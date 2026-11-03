// Package antigravity registers the builtin Antigravity harness detector.
// It probes PATH without starting a process or changing instruction files.
package antigravity

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

const (
	pluginID  = "antigravity"
	cmdDetect = "detect"
)

func init() {
	plugin.RegisterBuiltin(manifest(), handlers{})
}

// manifest describes the detect-only surface mirrored in manifest.toml.
func manifest() plugin.Manifest {
	return plugin.Manifest{
		ID:          pluginID,
		Name:        "Antigravity Harness",
		Schema:      plugin.SchemaVersion,
		Version:     "0.1.0",
		HostVersion: ">=0.1.0",
		Runtime:     plugin.RuntimeBuiltin,
		Provides: plugin.Provides{
			Commands: []plugin.CommandSpec{
				{Name: cmdDetect, Description: "Report whether the agy binary is present in PATH."},
			},
		},
	}
}

type handlers struct{}

// DispatchTool rejects tools because this plugin declares none.
func (handlers) DispatchTool(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindUnsupported, "antigravity: no such tool %q", name)
}

// DispatchIntent rejects intents because this plugin declares none.
func (handlers) DispatchIntent(_ context.Context, name string, _ []byte) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindUnsupported, "antigravity: no such intent %q", name)
}

// RunCommand dispatches detect and preserves lookup failures for the host.
func (handlers) RunCommand(_ context.Context, name string, _ []string) error {
	if name != cmdDetect {
		return cascade.Newf(cascade.KindUnsupported, "antigravity: unknown command %q", name)
	}
	_, err := Detect()
	return err
}
