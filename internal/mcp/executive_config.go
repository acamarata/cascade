package mcp

import (
	"encoding/json"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ExecutiveMCPConfigJSON produces the compact-write MCP configuration for
// an absolute binary path. The launcher must separately verify its bundle.
func ExecutiveMCPConfigJSON(bin string) ([]byte, error) {
	if !filepath.IsAbs(bin) {
		return nil, cascade.ErrInvalidInput
	}
	command, err := json.Marshal(bin)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, err, "mcp: encoding binary path")
	}
	return []byte(`{"mcpServers":{"cascade":{"command":` + string(command) +
		`,"args":["mcp","serve","--stdio","--profile","compact-write"]}}}`), nil
}
