package mcp

// Purpose: the MCP tool profiles a client may ask for, and the member set
//
//	each one exposes (contract:mcp-tool-profile, P1-HRN-05).
//
// Inputs: a profile string from configuration or a client flag.
// Outputs: ToolProfile, ParseToolProfile, the compact and compact-write
//
//	member sets, CompactAlias and ProfileMembers.
//
// Constraints: fail closed. A profile string that is not exactly one of
//
//	the three lowercase names parses to ProfileCompact, the read-only
//	set, never to an error and never to a wider profile. CompactToolNames
//	holds read-only tools only. Producers add members by appending to the
//	sets; no other edit is needed.

// ToolProfile names the tool set an MCP client is offered. The type has no
// permissive zero value: the empty string is not a profile, and
// ParseToolProfile maps it to ProfileCompact.
type ToolProfile string

const (
	// ProfileCompact offers the read-only compact set.
	ProfileCompact ToolProfile = "compact"
	// ProfileCompactWrite offers the compact set plus the compact-write
	// set, which holds the mutating tools a working session needs.
	ProfileCompactWrite ToolProfile = "compact-write"
	// ProfileFull offers every registered tool, with no member filter.
	ProfileFull ToolProfile = "full"
)

// ParseToolProfile maps s to a profile. Only the exact lowercase names
// "compact", "compact-write" and "full" parse to their own profile.
// Anything else, including case or whitespace variants, the empty string
// and unknown names, parses to ProfileCompact. It never returns an error:
// a caller that mistypes a profile gets the narrowest set, not a failure
// it might handle by falling back to something wider.
func ParseToolProfile(s string) ToolProfile {
	switch p := ToolProfile(s); p {
	case ProfileCompact, ProfileCompactWrite, ProfileFull:
		return p
	}
	return ProfileCompact
}

// CompactToolNames is the read-only compact set. Every member is a
// coretools Spec with Mutating false or a plugin tool whose grants are
// read only. Producers append here (P1-HRN-06 inbox reads).
var CompactToolNames = []string{
	"cascade_context_search",
	"cascade_context_show",
	"cascade_context_slice",
	"cascade_memory_recall",
	"cascade_plugin_pbd_status",
}

// CompactWriteToolNames is the set compact-write adds on top of
// CompactToolNames. Producers append here (P1-EXE conductor tools).
var CompactWriteToolNames = []string{
	"cascade_conductor_delegate",
	"cascade_conductor_delegate_wait",
	"cascade_ci_run",
	"cascade_ci_status",
	"cascade_memory_remember",
	"cascade_cpa_send",
}

// CompactAlias maps a short alias to the member tool it names. The aliases
// apply under ProfileCompact and ProfileCompactWrite only; ProfileFull
// uses the registry's own names and aliases.
var CompactAlias = map[string]string{
	"cascade_search": "cascade_context_search",
}

// ProfileMembers returns the tool names profile p exposes. ProfileFull
// returns nil, meaning no member filter. Any value that is not
// ProfileCompactWrite or ProfileFull, including an unparsed string cast to
// ToolProfile, gets the compact set. The result is a fresh slice the
// caller may modify.
func ProfileMembers(p ToolProfile) []string {
	p = ParseToolProfile(string(p))
	if p == ProfileFull {
		return nil
	}
	out := append([]string(nil), CompactToolNames...)
	if p == ProfileCompactWrite {
		out = append(out, CompactWriteToolNames...)
	}
	return out
}
