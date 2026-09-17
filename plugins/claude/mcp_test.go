package claude

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempPaths builds a Paths rooted in a fresh temp directory.
func tempPaths(t *testing.T) Paths {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(home, configDirName)
	return Paths{
		ConfigRoot: root,
		MCPConfig:  filepath.Join(home, mcpConfigName),
		Settings:   filepath.Join(root, settingsName),
		HookConfig: filepath.Join(root, hookDirName),
	}
}

// withLookPath swaps the PATH resolver for the duration of a test.
func withLookPath(t *testing.T, f func(string) (string, error)) {
	t.Helper()
	prev := lookPath
	lookPath = f
	t.Cleanup(func() { lookPath = prev })
}

// resolvesTo returns a lookPath that resolves any name to path.
func resolvesTo(path string) func(string) (string, error) {
	return func(string) (string, error) { return path, nil }
}

// goldenProtocolVersion reads the pinned MCP revision out of the captured
// wire golden. The test never restates the revision as a literal: both the
// server and this plugin's entry must trace to one captured artifact, so a
// protocol bump that never reaches registration fails here (testdata/
// README.md §1).
func goldenProtocolVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "v1-goldens", "tools_list.golden.json"))
	if err != nil {
		t.Fatalf("read the captured MCP wire golden: %v", err)
	}
	var doc struct {
		Response struct {
			Result struct {
				ProtocolVersion string `json:"protocol_version"`
			} `json:"result"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode the captured MCP wire golden: %v", err)
	}
	if doc.Response.Result.ProtocolVersion == "" {
		t.Fatal("the captured MCP wire golden carries no protocol_version")
	}
	return doc.Response.Result.ProtocolVersion
}

// decodeServers returns the server table written to path.
func decodeServers(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	servers := map[string]json.RawMessage{}
	if err := json.Unmarshal(doc[mcpServersKey], &servers); err != nil {
		t.Fatalf("decode the server table: %v", err)
	}
	return servers
}

// TestCascadeClaudeMCPRegistration is the contract's named registration
// check: the entry lands, carries the captured protocol revision, names the
// binary rather than a path, and re-registration converges.
func TestCascadeClaudeMCPRegistration(t *testing.T) {
	paths := tempPaths(t)
	withLookPath(t, resolvesTo("/usr/local/bin/cascade"))

	first, err := RegisterMCP(paths)
	if err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if !first.Changed {
		t.Fatal("first registration reported no change")
	}

	var entry mcpServerEntry
	if err := json.Unmarshal(decodeServers(t, paths.MCPConfig)[MCPServerName], &entry); err != nil {
		t.Fatalf("decode the registered entry: %v", err)
	}
	if entry.Command != cascadeBinary {
		t.Fatalf("command = %q, want the bare binary name %q", entry.Command, cascadeBinary)
	}
	if want := goldenProtocolVersion(t); entry.ProtocolVersion != want {
		t.Fatalf("protocol_version = %q, want %q from the captured wire golden", entry.ProtocolVersion, want)
	}

	second, err := RegisterMCP(paths)
	if err != nil {
		t.Fatalf("re-registration: %v", err)
	}
	if second.Changed {
		t.Fatal("re-registration rewrote the config; registration is not converging")
	}
}

// TestMCPEntryCarriesNoTreePath is the contract's explicit guard against
// the v1 failure: the written entry must contain no repo or build-tree path
// segment anywhere in it.
func TestMCPEntryCarriesNoTreePath(t *testing.T) {
	paths := tempPaths(t)
	withLookPath(t, resolvesTo("/usr/local/bin/cascade"))
	if _, err := RegisterMCP(paths); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths.MCPConfig) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"/target/", "/.git/", "_build", string(filepath.Separator) + "cascade" + string(filepath.Separator)} {
		if strings.Contains(string(raw), bad) {
			t.Fatalf("the written entry contains the tree path segment %q:\n%s", bad, raw)
		}
	}
}

// TestRegisterMCPRefusesUnresolvableBinary proves an unresolvable binary is
// a hard, actionable error rather than an entry written on hope.
func TestRegisterMCPRefusesUnresolvableBinary(t *testing.T) {
	paths := tempPaths(t)
	withLookPath(t, func(string) (string, error) { return "", errors.New("not found") })
	_, err := RegisterMCP(paths)
	if err == nil {
		t.Fatal("registration with an unresolvable binary succeeded, want a hard error")
	}
	for _, want := range []string{"not on PATH", "install cascade"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want an actionable message containing %q", err, want)
		}
	}
	if _, statErr := os.Stat(paths.MCPConfig); !os.IsNotExist(statErr) {
		t.Fatal("a refused registration still wrote a config file")
	}
}

// TestRegisterMCPRefusesBuildTreeBinary proves a binary resolving inside a
// source or build tree is refused at registration time.
func TestRegisterMCPRefusesBuildTreeBinary(t *testing.T) {
	paths := tempPaths(t)
	withLookPath(t, resolvesTo(filepath.Join("/home/dev/cascade", "target", "debug", "cascade")))
	_, err := RegisterMCP(paths)
	if err == nil {
		t.Fatal("registration of a build-tree binary succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Fatalf("error = %v, want it to name the offending segment", err)
	}
}

// TestMergePreservesForeignContent is the property the whole merge exists
// for: everything this plugin does not own survives registration
// untouched, including a server entry whose shape it has never seen.
func TestMergePreservesForeignContent(t *testing.T) {
	paths := tempPaths(t)
	withLookPath(t, resolvesTo("/usr/local/bin/cascade"))
	original := []byte(`{
  "mcpServers": {
    "other-server": {"command": "other", "args": ["--serve"], "unknown_field": {"nested": true}}
  },
  "unrelatedTopLevelKey": [1, 2, 3]
}`)
	if err := os.WriteFile(paths.MCPConfig, original, 0o644); err != nil { //nolint:gosec // test-local temp path.
		t.Fatal(err)
	}

	if _, err := RegisterMCP(paths); err != nil {
		t.Fatal(err)
	}
	servers := decodeServers(t, paths.MCPConfig)
	if _, ok := servers[MCPServerName]; !ok {
		t.Fatal("registration did not add this plugin's entry")
	}
	assertForeignIntact(t, paths.MCPConfig)
}

// assertForeignIntact checks the foreign server and the unrelated
// top-level key survived whatever the merge just did.
func assertForeignIntact(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // test-local temp path.
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Compared as decoded values, not raw bytes: the merge re-encodes the
	// whole document, so an untouched key comes back semantically identical
	// but re-indented. Content preservation is the property; byte-identical
	// formatting of the user's file is explicitly NOT claimed (testdata/
	// README.md §2).
	var unrelated []int
	if err := json.Unmarshal(doc["unrelatedTopLevelKey"], &unrelated); err != nil {
		t.Fatalf("unrelated top-level key did not survive: %v", err)
	}
	if len(unrelated) != 3 || unrelated[0] != 1 || unrelated[2] != 3 {
		t.Fatalf("unrelated top-level key = %v, want [1 2 3] carried through unchanged", unrelated)
	}
	var foreign map[string]any
	if err := json.Unmarshal(decodeServers(t, path)["other-server"], &foreign); err != nil {
		t.Fatalf("the foreign server entry did not survive: %v", err)
	}
	if _, ok := foreign["unknown_field"]; !ok {
		t.Fatal("the foreign entry lost a field this plugin does not understand")
	}
}

// TestMergeRejectsNonObjectConfig proves malformed user-owned input is a
// typed refusal rather than a silent overwrite of the user's file.
func TestMergeRejectsNonObjectConfig(t *testing.T) {
	for _, input := range []string{`["not", "an", "object"]`, `"a string"`, `{`} {
		if _, err := mergeMCPConfig([]byte(input), nil); err == nil {
			t.Fatalf("mergeMCPConfig(%q) succeeded, want a refusal", input)
		}
	}
}

// FuzzCCConfigMerge drives the user-owned-config decoder with arbitrary
// bytes (06 §5.7). The contract under fuzzing is totality: every input
// either merges or returns a typed error, and none panics. When a merge
// does succeed its output must itself be a valid object that still holds
// the server table, so a "success" that produced an unparseable file is a
// failure here.
func FuzzCCConfigMerge(f *testing.F) {
	f.Add([]byte(`{"mcpServers":{"other":{"command":"x"}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))
	f.Add([]byte(`{"mcpServers":"not-an-object"}`))
	entry := mcpServerEntry{Command: cascadeBinary, Args: mcpCommandArgs, ProtocolVersion: MCPProtocolVersion}
	f.Fuzz(func(t *testing.T, existing []byte) {
		for _, e := range []*mcpServerEntry{&entry, nil} {
			out, err := mergeMCPConfig(existing, e)
			if err != nil {
				continue
			}
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("merge succeeded but produced undecodable output %q: %v", out, err)
			}
			if _, ok := doc[mcpServersKey]; !ok {
				t.Fatalf("merge succeeded but dropped the %q table: %q", mcpServersKey, out)
			}
		}
	})
}
