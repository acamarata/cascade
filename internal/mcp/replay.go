// Purpose: the protocol revisions this server negotiates, part of the
// capture-and-replay contract (the harness itself lives in
// replay_harness_test.go and compiles only into tests).

package mcp

// SupportedProtocolVersions is every revision this server negotiates. A
// client offering another version is answered with this one.
var SupportedProtocolVersions = []string{MCPProtocolVersion}
