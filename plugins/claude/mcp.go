package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Purpose: register (and unregister) the Cascade MCP server in the
//   harness's user-owned MCP configuration, converging idempotently and
//   preserving every entry and key this plugin does not own.
// Inputs: resolved Paths; the process PATH (via the injectable lookPath);
//   the existing on-disk config, which is USER-OWNED and therefore
//   untrusted input — hence FuzzCCConfigMerge over mergeMCPConfig.
// Outputs: the merged MCP config file.
// Constraints: os/exec is denied to plugins/** (internal/build/
//   egress_allow.go's EgressExecNormative names only internal/plugins/
//   process, "the one place a plugin subprocess is started"; a PATH
//   existence probe is not a spawn, but importing os/exec at all trips the
//   gate). defaultLookPath is therefore the same small, real
//   reimplementation of exec.LookPath's PATH-search semantics that
//   plugins/opencode/detect.go carries, for the same reason.
// SPORT: plugins/claude mcp-registration (ADD) — P1-E16-W4-S34-T1.

// MCPServerName is the key this plugin owns in the harness MCP config. It
// owns this key and nothing else: every other server entry, and every
// top-level key, survives both registration and unregistration with its
// CONTENT intact. Formatting is not preserved — the document is re-encoded
// with standard indentation on every write, so a merge normalizes the whole
// file's layout. That is a real, visible consequence for a file the user
// owns and edits, so it is stated rather than implied.
const MCPServerName = "cascade"

// cascadeBinary is the stable installed binary name written as the MCP
// command. It is a NAME, never an absolute path: an absolute path into a
// source or build tree is the exact v1 failure this ticket names, since it
// breaks the moment the tree moves or the build directory is cleaned.
const cascadeBinary = "cascade"

// mcpServersKey is the top-level key holding the server table.
const mcpServersKey = "mcpServers"

// mcpCommandArgs are the arguments that start the MCP server over stdio,
// matching cmd/cascade/mcp.go's real `mcp serve --stdio` surface.
var mcpCommandArgs = []string{"mcp", "serve", "--stdio"}

// lookPath resolves a binary name to its full path, following PATH. It is
// a package variable, not a hard-coded call, so mcp_test.go controls both
// the resolvable and unresolvable states without touching the real
// filesystem.
var lookPath = defaultLookPath

// defaultLookPath searches PATH for an executable file named name, the
// same semantics exec.LookPath provides, without importing os/exec (see
// the file's Constraints). On Windows it also tries each PATHEXT suffix,
// matching exec.LookPath's own Windows behavior; elsewhere a candidate
// must have at least one execute bit set.
func defaultLookPath(name string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, cand := range candidateNames(name) {
			path := filepath.Join(dir, cand)
			if isExecutableFile(path) {
				return path, nil
			}
		}
	}
	return "", fmt.Errorf("%s: executable file not found in $PATH", name)
}

// candidateNames returns the file names to probe for name in one PATH
// directory: name itself, plus every PATHEXT suffix on Windows.
func candidateNames(name string) []string {
	if runtime.GOOS != "windows" {
		return []string{name}
	}
	exts := strings.Split(os.Getenv("PATHEXT"), string(filepath.ListSeparator))
	out := make([]string, 0, len(exts)+1)
	for _, ext := range exts {
		if ext != "" {
			out = append(out, name+ext)
		}
	}
	return append(out, name)
}

// isExecutableFile reports whether path names a regular file with at least
// one execute bit set (non-Windows), or simply exists as a regular file
// (Windows, where the candidate name already carries a PATHEXT suffix).
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode()&0o111 != 0
}

// mcpServerEntry is the Cascade server entry this plugin writes. Only this
// plugin's own entry is modeled as a struct; every other server is carried
// through the merge as an opaque json.RawMessage so an entry shape this
// plugin has never seen survives a round trip unchanged.
type mcpServerEntry struct {
	// Command is the binary NAME to start, resolved via PATH by the
	// harness at session time.
	Command string `json:"command"`
	// Args are the arguments that select the stdio MCP transport.
	Args []string `json:"args"`
	// ProtocolVersion records the MCP revision this entry was written
	// for, pinned by R-14.14 and asserted in mcp_test.go against the real
	// wire golden in testdata/ rather than against a constant restated in
	// the test — so a protocol bump that never reaches this entry fails.
	ProtocolVersion string `json:"protocol_version"`
}

// MCPProtocolVersion is the pinned MCP revision (R-14.14), the same value
// internal/mcp's server reports and testdata/tools_list.golden.json
// carries.
const MCPProtocolVersion = "2026-07-28"

// cascadeEntry builds this plugin's server entry after proving the binary
// it names actually resolves.
func cascadeEntry() (mcpServerEntry, error) {
	resolved, err := lookPath(cascadeBinary)
	if err != nil {
		return mcpServerEntry{}, fmt.Errorf(
			"cascade-claude: cannot register the MCP server: the %q binary is not on PATH (%w); "+
				"install cascade so that %q resolves, then re-run the harness sync",
			cascadeBinary, err, cascadeBinary)
	}
	if seg := buildTreeSegment(resolved); seg != "" {
		return mcpServerEntry{}, fmt.Errorf(
			"cascade-claude: refusing to register the MCP server: %q resolves to %s, inside a %s tree; "+
				"an entry pointing into a source or build tree breaks as soon as that tree moves",
			cascadeBinary, resolved, seg)
	}
	return mcpServerEntry{Command: cascadeBinary, Args: mcpCommandArgs, ProtocolVersion: MCPProtocolVersion}, nil
}

// buildTreeSegments are path segments that mark a source or build tree.
var buildTreeSegments = []string{"target", ".git", "_build"}

// buildTreeSegment returns the first build-tree segment in path, or "".
func buildTreeSegment(path string) string {
	for _, seg := range strings.Split(filepath.ToSlash(path), "/") {
		for _, bad := range buildTreeSegments {
			if seg == bad {
				return seg
			}
		}
	}
	return ""
}

// RegisterMCP writes the Cascade server entry into the harness MCP config
// at paths.MCPConfig, preserving every other server and every top-level key
// the file already carries. First write and re-registration are the same
// code path: both converge on the same bytes, so a second run reports
// Changed=false.
func RegisterMCP(paths Paths) (InstallResult, error) {
	entry, err := cascadeEntry()
	if err != nil {
		return InstallResult{}, err
	}
	existing, err := readMCPConfig(paths.MCPConfig)
	if err != nil {
		return InstallResult{}, err
	}
	merged, err := mergeMCPConfig(existing, &entry)
	if err != nil {
		return InstallResult{}, err
	}
	changed, err := writeIfChanged(paths.MCPConfig, merged)
	return InstallResult{Path: paths.MCPConfig, Changed: changed}, err
}

// readMCPConfig reads the existing config, reporting a missing file as
// empty bytes rather than an error: no config yet is the ordinary
// first-install state.
func readMCPConfig(path string) ([]byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path derives from the resolved config root, not user input.
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cascade-claude: read %s: %w", path, err)
	}
	return raw, nil
}

// mergeMCPConfig merges entry into existing, or removes this plugin's
// entry when entry is nil, and returns the re-encoded document.
//
// existing is USER-OWNED, untrusted JSON: FuzzCCConfigMerge drives this
// function directly (06 §5.7). Its contract under fuzzing is total — every
// input either merges or returns a typed error, and never panics. Unknown
// top-level keys and unknown sibling server entries are carried through as
// opaque json.RawMessage, so this plugin can never silently drop a setting
// it does not understand.
func mergeMCPConfig(existing []byte, entry *mcpServerEntry) ([]byte, error) {
	doc := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(existing))) > 0 {
		if err := json.Unmarshal(existing, &doc); err != nil {
			return nil, fmt.Errorf("cascade-claude: the harness MCP config is not a JSON object: %w", err)
		}
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := doc[mcpServersKey]; ok && len(strings.TrimSpace(string(raw))) > 0 {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, fmt.Errorf("cascade-claude: the harness MCP config's %q is not a JSON object: %w", mcpServersKey, err)
		}
	}

	if entry == nil {
		delete(servers, MCPServerName)
	} else {
		encoded, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("cascade-claude: encode MCP server entry: %w", err)
		}
		servers[MCPServerName] = encoded
	}

	encodedServers, err := json.Marshal(servers)
	if err != nil {
		return nil, fmt.Errorf("cascade-claude: encode MCP server table: %w", err)
	}
	doc[mcpServersKey] = encodedServers
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("cascade-claude: encode MCP config: %w", err)
	}
	return append(out, '\n'), nil
}
