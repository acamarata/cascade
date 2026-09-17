package context

// Purpose: the harness state-file parser (P1-E16-W4-S35-T3) — the small
//   amount a detector may learn from a config file the HARNESS owns: the
//   version it recorded, and whether cascade's MCP server is registered
//   in it.
// Inputs: raw bytes from a file this program did not write.
// Outputs: a HarnessConfig, or a typed refusal.
// Constraints: this is a decoder over untrusted input, so it is fuzzed
//   (FuzzHarnessConfigParse) and it never panics on any input. It is
//   deliberately INCURIOUS: it reads two facts and ignores everything
//   else, including every field a harness adds between releases. A parser
//   that refused an unknown field would report a machine as unreadable
//   the day its harness updated, and a parser that read more would put
//   somebody's configuration into a report nobody asked for.
// SPORT: internal/context harness config parse (ADD) — P1-E16-W4-S35-T3.

import (
	"bytes"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// CascadeMCPServerName is the key cascade registers itself under in a
// harness MCP configuration. It matches plugins/claude's MCPServerName;
// the two are asserted equal by that package's own test rather than
// shared, because plugins/** may not import internal/**.
const CascadeMCPServerName = "cascade"

// maxHarnessConfigBytes bounds a parse. Harness configs are small; a file
// larger than this is either not one or is not worth reading to find out.
const maxHarnessConfigBytes = 8 << 20

// HarnessConfig is everything detection learns from a harness's own
// state file.
type HarnessConfig struct {
	// Version is the harness version the file records, when it records
	// one. Empty is normal: not every harness writes a version, and
	// absence is reported rather than guessed.
	Version string `json:"version,omitempty"`
	// CascadeRegistered reports whether cascade appears as an MCP server
	// in this configuration, at any scope the file expresses.
	CascadeRegistered bool `json:"cascade_registered"`
}

// harnessConfigDoc is the subset of a harness config this parser reads.
//
// The field names are the harnesses' own. `mcpServers` is the shape every
// supported harness uses for its MCP table; `projects` is the per-project
// nesting one of them adds, whose inner objects carry their own
// `mcpServers`. Both are read; a file with neither simply reports
// nothing registered.
type harnessConfigDoc struct {
	FirstStartVersion string                       `json:"firstStartVersion"`
	Version           string                       `json:"version"`
	MCPServers        map[string]json.RawMessage   `json:"mcpServers"`
	Projects          map[string]harnessProjectRec `json:"projects"`
}

// harnessProjectRec is one per-project record's readable part.
type harnessProjectRec struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// ParseHarnessConfig reads raw.
//
// An EMPTY input is not an error and not a config: it reports the zero
// HarnessConfig. A file a harness has created but not yet written to is a
// normal state, and refusing it would make a fresh install indistinguishable
// from a corrupt one.
func ParseHarnessConfig(raw []byte) (HarnessConfig, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return HarnessConfig{}, nil
	}
	if len(trimmed) > maxHarnessConfigBytes {
		return HarnessConfig{}, cascade.Newf(cascade.KindInvalidInput,
			"context: harness config exceeds %d bytes", maxHarnessConfigBytes)
	}
	var doc harnessConfigDoc
	if err := json.Unmarshal(trimmed, &doc); err != nil {
		return HarnessConfig{}, cascade.Wrap(cascade.KindInvalidInput, err,
			"context: harness config is not valid JSON")
	}
	version := doc.Version
	if version == "" {
		version = doc.FirstStartVersion
	}
	return HarnessConfig{Version: version, CascadeRegistered: cascadeRegisteredIn(doc)}, nil
}

// cascadeRegisteredIn reports whether cascade appears in any MCP table
// the document carries.
//
// Any scope counts. A caller asking "is cascade wired into this harness"
// is asking whether a session would see it, and a per-project
// registration answers yes for that project as surely as a user-scoped
// one does everywhere.
func cascadeRegisteredIn(doc harnessConfigDoc) bool {
	if _, ok := doc.MCPServers[CascadeMCPServerName]; ok {
		return true
	}
	for _, project := range doc.Projects {
		if _, ok := project.MCPServers[CascadeMCPServerName]; ok {
			return true
		}
	}
	return false
}
