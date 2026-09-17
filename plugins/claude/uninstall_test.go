package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose (this file): the uninstall's one promise — it removes exactly
//   what this plugin installed, and nothing else.
//
// "Nothing else" is where the risk lives. The harness config directory is
//   the operator's: it holds other integrations' MCP entries, and it may
//   hold an instruction file the operator has since edited. Every test
//   below is some form of "did this delete something it did not write".
// SPORT: plugins/claude uninstall tests (ADD) — P1-E16-W4-S34-T1.

// uninstallEnv is a harness config tree under t.TempDir(), plus the cwd
// whose instruction file the generator produces.
type uninstallEnv struct {
	paths   Paths
	cwd     string
	instr   string
	content []byte
}

// newUninstallEnv installs a full cascade-claude footprint and returns it.
func newUninstallEnv(t *testing.T) uninstallEnv {
	t.Helper()
	root := t.TempDir()
	cwd := t.TempDir()
	env := uninstallEnv{
		paths: Paths{
			ConfigRoot: root,
			MCPConfig:  filepath.Join(root, "mcp.json"),
			HookConfig: filepath.Join(root, "hooks"),
		},
		cwd:     cwd,
		instr:   filepath.Join(cwd, "CLAUDE.md"),
		content: []byte("# generated instructions\n"),
	}
	if err := os.MkdirAll(env.paths.HookConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	// The generator is the authority on which instruction files are ours,
	// so the test wires the same one the uninstall will ask.
	prev := Generate
	Generate = func(context.Context, string) ([]GeneratedFile, error) {
		return []GeneratedFile{{Path: env.instr, Content: env.content}}, nil
	}
	t.Cleanup(func() { Generate = prev })

	if err := os.WriteFile(env.instr, env.content, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{hookPackFile, hookPackVersionFile} {
		if err := os.WriteFile(filepath.Join(env.paths.HookConfig, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

// writeMCP writes a config document with the given servers.
func writeMCP(t *testing.T, path string, servers map[string]any, extra map[string]any) {
	t.Helper()
	doc := map[string]any{mcpServersKey: servers}
	for k, v := range extra {
		doc[k] = v
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestUninstallRemovesEverythingItInstalled is the happy path across all
// three surfaces at once, because a partial uninstall is the failure mode
// that actually happens.
func TestUninstallRemovesEverythingItInstalled(t *testing.T) {
	env := newUninstallEnv(t)
	writeMCP(t, env.paths.MCPConfig, map[string]any{
		MCPServerName: map[string]any{"command": "cascade"},
	}, nil)

	results, err := Uninstall(context.Background(), env.paths, env.cwd, nil)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("%d results, want one per managed file (instructions, pack, version, mcp)", len(results))
	}
	for _, r := range results {
		if !r.Removed {
			t.Errorf("%s was not removed (kept=%v)", r.Path, r.Kept)
		}
	}
	for _, p := range []string{env.instr,
		filepath.Join(env.paths.HookConfig, hookPackFile),
		filepath.Join(env.paths.HookConfig, hookPackVersionFile)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists after uninstall", p)
		}
	}
}

// TestTheConfigDirectoryItselfSurvives is the rule that matters most. The
// directory is the operator's; a plugin that removed one it did not create
// would take unrelated configuration with it.
func TestTheConfigDirectoryItselfSurvives(t *testing.T) {
	env := newUninstallEnv(t)
	bystander := filepath.Join(env.paths.HookConfig, "someone-elses-pack.json")
	if err := os.WriteFile(bystander, []byte("not ours"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(context.Background(), env.paths, env.cwd, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.paths.HookConfig); err != nil {
		t.Errorf("the hook config directory was removed: %v", err)
	}
	if _, err := os.Stat(env.paths.ConfigRoot); err != nil {
		t.Errorf("the harness config root was removed: %v", err)
	}
	if _, err := os.Stat(bystander); err != nil {
		t.Errorf("another pack's config was removed: %v", err)
	}
}

// TestAnEditedInstructionFileIsKept holds the second rule. The operator
// changed it, or something else owns it now; either way deleting it would
// destroy work this plugin did not do.
func TestAnEditedInstructionFileIsKept(t *testing.T) {
	env := newUninstallEnv(t)
	if err := os.WriteFile(env.instr, []byte("# generated\n\n## my own notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := Uninstall(context.Background(), env.paths, env.cwd, nil)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range results {
		if r.Path != env.instr {
			continue
		}
		found = true
		if !r.Kept || r.Removed {
			t.Errorf("an edited file reported kept=%v removed=%v, want kept", r.Kept, r.Removed)
		}
	}
	if !found {
		t.Fatal("the instruction file was not reported at all")
	}
	if _, err := os.Stat(env.instr); err != nil {
		t.Errorf("the edited instruction file was deleted: %v", err)
	}
}

// TestOtherMCPServersSurvive is the third rule. Deleting mcp.json would
// uninstall every other integration the operator has.
func TestOtherMCPServersSurvive(t *testing.T) {
	env := newUninstallEnv(t)
	writeMCP(t, env.paths.MCPConfig, map[string]any{
		MCPServerName:  map[string]any{"command": "cascade"},
		"someone-else": map[string]any{"command": "their-binary", "args": []string{"--serve"}},
	}, map[string]any{"unrelatedTopLevelKey": "preserved"})

	if _, err := Uninstall(context.Background(), env.paths, env.cwd, nil); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(env.paths.MCPConfig)
	if err != nil {
		t.Fatalf("the MCP config was deleted outright: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the MCP config is no longer JSON: %v", err)
	}
	if _, still := doc["unrelatedTopLevelKey"]; !still {
		t.Error("an unrelated top-level key was dropped")
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(doc[mcpServersKey], &servers); err != nil {
		t.Fatal(err)
	}
	if _, ours := servers[MCPServerName]; ours {
		t.Error("our own MCP entry survived the uninstall")
	}
	if _, theirs := servers["someone-else"]; !theirs {
		t.Error("another integration's MCP entry was removed")
	}
}

// TestUninstallIsIdempotent is what a re-run after a partial failure looks
// like: every file already gone, and that is a success.
func TestUninstallIsIdempotent(t *testing.T) {
	env := newUninstallEnv(t)
	writeMCP(t, env.paths.MCPConfig, map[string]any{
		MCPServerName: map[string]any{"command": "cascade"},
	}, nil)

	if _, err := Uninstall(context.Background(), env.paths, env.cwd, nil); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}
	results, err := Uninstall(context.Background(), env.paths, env.cwd, nil)
	if err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	for _, r := range results {
		if r.Removed {
			t.Errorf("%s was removed twice", r.Path)
		}
		if r.Kept {
			t.Errorf("%s was reported kept on a second run", r.Path)
		}
	}
}

// TestNothingIsRemovedWhenTheGeneratorIsUnwired holds the refusal that
// keeps this from becoming a delete-by-guess. Without a generator this
// plugin cannot say which instruction files are its own, so it removes
// none of them.
func TestNothingIsRemovedWhenTheGeneratorIsUnwired(t *testing.T) {
	env := newUninstallEnv(t)
	Generate = unwiredGenerator

	_, err := Uninstall(context.Background(), env.paths, env.cwd, nil)
	if err == nil {
		t.Fatal("an unwired generator did not refuse")
	}
	if !strings.Contains(err.Error(), "which instruction files are ours") {
		t.Errorf("err = %q, want it to name the reason", err)
	}
	if _, statErr := os.Stat(env.instr); statErr != nil {
		t.Error("the instruction file was removed despite the refusal")
	}
}

// TestAMissingMCPConfigIsNothingToRemove covers the first-uninstall state
// on a harness that never had an MCP config.
func TestAMissingMCPConfigIsNothingToRemove(t *testing.T) {
	env := newUninstallEnv(t)
	results, err := Uninstall(context.Background(), env.paths, env.cwd, nil)
	if err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	for _, r := range results {
		if r.Path == env.paths.MCPConfig && (r.Removed || r.Kept) {
			t.Errorf("a missing MCP config reported removed=%v kept=%v", r.Removed, r.Kept)
		}
	}
	if _, err := os.Stat(env.paths.MCPConfig); !os.IsNotExist(err) {
		t.Error("an MCP config was created by the uninstall")
	}
}

// TestAConfigWithoutOurEntryIsLeftByteIdentical keeps the uninstall from
// rewriting a file it had nothing to change in. A new mtime on a config
// this plugin does not appear in is a change an operator would have to
// explain.
func TestAConfigWithoutOurEntryIsLeftByteIdentical(t *testing.T) {
	env := newUninstallEnv(t)
	writeMCP(t, env.paths.MCPConfig, map[string]any{
		"someone-else": map[string]any{"command": "their-binary"},
	}, nil)
	before, err := os.ReadFile(env.paths.MCPConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(context.Background(), env.paths, env.cwd, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(env.paths.MCPConfig)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("the config was rewritten:\n before %s\n after  %s", before, after)
	}
}
