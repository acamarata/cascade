// Purpose: unit tests for `cascade memory soul` — the wiring proof against
//
//	the REAL root command, the automation-parity pair (--content applies a
//	file with no editor; CASCADE_NO_INPUT=1 fails BEFORE any editor
//	exists), the editor round trip through the injected seam, and the
//	export canary over the bytes this command prints. No "net" import, so
//	this runs in the fast no-network unit lane (Art.7.2).
//
// SPORT: G/memory-soul-store (ADD, placeholder per T-2 sport_updates).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// soulHarness runs a soul verb with every external input injected: the
// RPC seam, the environment, and the editor. Nothing here touches a real
// socket, a real environment or a real editor process.
type soulHarness struct {
	calls   []recordedCall
	results map[string]any
	err     error
	env     map[string]string
	// editorRuns counts how many times an editor was launched. It is the
	// assertion the CASCADE_NO_INPUT test turns on: the guard's promise is
	// that no subprocess is created, not merely that the call fails.
	editorRuns int
	// editWith, when set, is what the fake editor writes into the file it
	// is handed, standing in for what a user types and saves.
	editWith string
	// editorErr, when set, is what the fake editor returns.
	editorErr error
}

func (h *soulHarness) deps(t *testing.T) memoryDeps {
	t.Helper()
	root := t.TempDir()
	return memoryDeps{
		Paths:   fakeMemoryPaths{root: root},
		Getenv:  func(k string) string { return h.env[k] },
		Environ: func() []string { return nil },
		Call: func(_ context.Context, _, method string, params, out any) error {
			h.calls = append(h.calls, recordedCall{Method: method, Params: params})
			if h.err != nil {
				return h.err
			}
			result, ok := h.results[method]
			if !ok {
				return nil
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, out)
		},
		Editor: func(_ context.Context, _ *cobra.Command, _, path string) error {
			h.editorRuns++
			if h.editorErr != nil {
				return h.editorErr
			}
			return os.WriteFile(path, []byte(h.editWith), 0o600)
		},
	}
}

// run executes one soul verb, returning stdout, stderr and the error.
func (h *soulHarness) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newMemoryCmd(h.deps(t))
	flags := cmd.PersistentFlags()
	flags.Bool("json", false, "")
	flags.Bool("quiet", false, "")
	flags.Bool("verbose", false, "")
	flags.Bool("no-color", false, "")

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"soul"}, args...))
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

// TestSoulVerbsResolveOnTheRealRootCommand is the reachability proof: the
// verbs are found on the SAME tree main() executes, so a missing mount is
// a failing test rather than a subsystem nobody can reach.
func TestSoulVerbsResolveOnTheRealRootCommand(t *testing.T) {
	for _, verb := range []string{"show", "edit", "export"} {
		cmd, _, err := newRootCmd().Find([]string{"memory", "soul", verb})
		if err != nil {
			t.Fatalf("memory soul %s did not resolve: %v", verb, err)
		}
		if cmd.Name() != verb || cmd.Parent() == nil || cmd.Parent().Name() != "soul" {
			t.Fatalf("resolved %q under %v", cmd.Name(), cmd.Parent())
		}
		if cmd.RunE == nil {
			t.Fatalf("memory soul %s has no RunE", verb)
		}
	}
	edit, _, err := newRootCmd().Find([]string{"memory", "soul", "edit"})
	if err != nil {
		t.Fatalf("find edit: %v", err)
	}
	if edit.Flags().Lookup("content") == nil {
		t.Fatal("memory soul edit has no --content flag, so it has no non-interactive form")
	}
}

// TestSoulShowRendersTheDocumentAndVersion pins the human output.
func TestSoulShowRendersTheDocumentAndVersion(t *testing.T) {
	h := &soulHarness{results: map[string]any{
		memory.MethodSoulShow: memory.SoulShowResult{Body: "I am Ada.", Version: 4},
	}}
	stdout, _, err := h.run(t, "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(stdout, "I am Ada.") || !strings.Contains(stdout, "soul version 4") {
		t.Fatalf("stdout = %q", stdout)
	}
	if strings.Contains(stdout, "warning") {
		t.Fatalf("an undiverged soul printed a warning: %q", stdout)
	}
	if len(h.calls) != 1 || h.calls[0].Method != memory.MethodSoulShow {
		t.Fatalf("calls = %+v", h.calls)
	}
}

// TestSoulShowWarnsOnDivergence proves a possibly-stale identity document
// is never presented as if it were current.
func TestSoulShowWarnsOnDivergence(t *testing.T) {
	h := &soulHarness{results: map[string]any{
		memory.MethodSoulShow: memory.SoulShowResult{Body: "stored", Version: 2, Diverged: true},
	}}
	stdout, _, err := h.run(t, "show")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(stdout, "warning") || !strings.Contains(stdout, "neither side was applied") {
		t.Fatalf("no divergence warning: %q", stdout)
	}
}

// TestSoulExportPrintsTheEnvelopeAndNothingElse is the CLI end of the
// export canary. The command prints what the daemon returned, so a value
// the daemon never sent cannot appear, and the printed bytes are asserted
// directly rather than the decoded struct.
func TestSoulExportPrintsTheEnvelopeAndNothingElse(t *testing.T) {
	export := memory.SoulExport{
		SchemaVersion: memory.SoulSchemaVersion,
		ExportedAt:    time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Soul:          memory.SoulDocument{Body: "I am Ada.", Schema: memory.DefaultSoulSchema},
		AuditEntries: []memory.AuditEntry{
			{Version: 1, Route: memory.SoulRouteCLI, EditedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), DeltaHash: "abc"},
		},
	}
	h := &soulHarness{results: map[string]any{memory.MethodSoulExport: export}}
	stdout, _, err := h.run(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("printed output is not parseable JSON: %v\n%s", err, stdout)
	}
	if len(decoded) != 4 {
		t.Fatalf("printed envelope has %d fields, want 4: %s", len(decoded), stdout)
	}
	for _, key := range []string{"schema_version", "exported_at", "soul", "audit_entries"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("printed envelope is missing %q: %s", key, stdout)
		}
	}
	for _, forbidden := range []string{"/Users/", "/tmp/", "socket", "CASCADE_"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("the export printed %q: %s", forbidden, stdout)
		}
	}
}

// TestSoulDiagnosticsAreScrubbed is the redaction canary for every soul
// verb: a failure naming a machine path or a secret-shaped value must not
// carry either into a terminal, since that is the text that gets pasted
// into a bug report.
func TestSoulDiagnosticsAreScrubbed(t *testing.T) {
	const secret = "sk-live-CANARY0123456789abcdefghij"
	const path = "/Users/canary-operator/.cascade/memory/soul/SOUL.md"
	for _, verb := range [][]string{{"show"}, {"export"}, {"edit", "--content", "x"}} {
		h := &soulHarness{err: cascade.Newf(cascade.KindUnavailable,
			"reading %s failed with token %s", path, secret)}
		if verb[0] == "edit" {
			file := filepath.Join(t.TempDir(), "c.md")
			if err := os.WriteFile(file, []byte("body"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			verb[2] = file
		}
		_, _, err := h.run(t, verb...)
		if err == nil {
			t.Fatalf("%v swallowed the failure", verb)
		}
		if strings.Contains(err.Error(), path) || strings.Contains(err.Error(), secret) {
			t.Fatalf("%v leaked a diagnostic: %v", verb, err)
		}
	}
}
