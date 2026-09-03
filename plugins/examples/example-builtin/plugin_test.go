// Purpose: real behavioral coverage for example-builtin — the toy
//
//	first-party plugin other plugin authors browsing this repo will copy
//	as a template. Exercises compile-time registration into
//	plugin.Builtins(), the manifest shape, and all three dispatch
//	surfaces (tool/intent/command) with both success and error paths.
//
// Inputs: none (pure, stateless handlers; no filesystem/network I/O).
// Outputs: pass/fail via testing.T; no artifacts produced.
// Constraints: package-internal test (has access to unexported manifest(),
//
//	handlers{}, and the toolName/intentName/commandName consts) so it can
//	assert on the exact registered shape, not just black-box behavior.
//
// SPORT: plugins/examples/example-builtin (TEST) — coverage remediation.
package examplebuiltin

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// TestRegistration verifies that importing this package (which triggers
// init() -> plugin.RegisterBuiltin) makes example-builtin discoverable via
// plugin.Builtins() with the exact manifest this package declares. Builtins
// freezes the process-global registry on first call, so this also acts as
// the de-facto entry point every other builtin-plugin test in the binary
// observes — asserting the frozen snapshot contains our entry is the
// correct way to test "did init() register me" without reaching into
// unexported registry internals.
func TestRegistration(t *testing.T) {
	regs := plugin.Builtins()

	var found *plugin.BuiltinRegistration
	for i := range regs {
		if regs[i].Manifest.ID == pluginID {
			found = &regs[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("plugin.Builtins() does not contain %q; init() did not register it", pluginID)
	}

	want := manifest()
	got := found.Manifest
	if got.ID != want.ID {
		t.Errorf("Manifest.ID = %q, want %q", got.ID, want.ID)
	}
	if got.Name != want.Name {
		t.Errorf("Manifest.Name = %q, want %q", got.Name, want.Name)
	}
	if got.Schema != plugin.SchemaVersion {
		t.Errorf("Manifest.Schema = %q, want %q", got.Schema, plugin.SchemaVersion)
	}
	if got.Runtime != plugin.RuntimeBuiltin {
		t.Errorf("Manifest.Runtime = %q, want %q", got.Runtime, plugin.RuntimeBuiltin)
	}
	if len(got.Provides.Tools) != 1 || got.Provides.Tools[0].Name != toolName {
		t.Errorf("Provides.Tools = %+v, want one tool named %q", got.Provides.Tools, toolName)
	}
	if len(got.Provides.Intents) != 1 || got.Provides.Intents[0].Name != intentName {
		t.Errorf("Provides.Intents = %+v, want one intent named %q", got.Provides.Intents, intentName)
	}
	if len(got.Provides.Commands) != 1 || got.Provides.Commands[0].Name != commandName {
		t.Errorf("Provides.Commands = %+v, want one command named %q", got.Provides.Commands, commandName)
	}

	if len(found.Grants) != 1 || found.Grants[0] != "read" {
		t.Errorf("Grants = %v, want [\"read\"]", found.Grants)
	}

	// Compile-time contract: handlers{} must satisfy BuiltinHandlers (this
	// is also enforced implicitly by RegisterBuiltin's signature, but an
	// explicit assertion documents the contract for anyone copying this
	// package as a template).
	var _ plugin.BuiltinHandlers = handlers{}
}

// TestManifestShape checks the manifest() constructor directly, independent
// of registration, so a future change to init()'s call site can't hide a
// manifest regression.
func TestManifestShape(t *testing.T) {
	m := manifest()
	if m.Version != "0.1.0" {
		t.Errorf("Version = %q, want %q", m.Version, "0.1.0")
	}
	if m.HostVersion != ">=0.1.0" {
		t.Errorf("HostVersion = %q, want %q", m.HostVersion, ">=0.1.0")
	}
	if m.Provides.Tools[0].Description == "" {
		t.Error("Tools[0].Description is empty")
	}
	if m.Provides.Intents[0].Description == "" {
		t.Error("Intents[0].Description is empty")
	}
	if m.Provides.Commands[0].Description == "" {
		t.Error("Commands[0].Description is empty")
	}
}

// TestDispatchTool covers greet-tool's success path (named input, empty
// input) and its unknown-name error path.
func TestDispatchTool(t *testing.T) {
	h := handlers{}
	ctx := context.Background()

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "named input", input: "Aisha", want: "Hello, Aisha!"},
		{name: "empty input defaults to world", input: "", want: "Hello, world!"},
		{name: "whitespace-only input defaults to world", input: "   ", want: "Hello, world!"},
		{name: "input with surrounding whitespace is trimmed", input: "  Yusuf  ", want: "Hello, Yusuf!"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := h.DispatchTool(ctx, toolName, []byte(tc.input))
			if err != nil {
				t.Fatalf("DispatchTool(%q) unexpected error: %v", tc.input, err)
			}
			if string(out) != tc.want {
				t.Errorf("DispatchTool(%q) = %q, want %q", tc.input, string(out), tc.want)
			}
		})
	}

	t.Run("unknown tool name errors", func(t *testing.T) {
		out, err := h.DispatchTool(ctx, "not-a-real-tool", []byte("x"))
		if err == nil {
			t.Fatal("DispatchTool with unknown name: got nil error, want non-nil")
		}
		if out != nil {
			t.Errorf("DispatchTool with unknown name: got output %q, want nil", out)
		}
		if !strings.Contains(err.Error(), "not-a-real-tool") {
			t.Errorf("error %q does not mention the offending name", err.Error())
		}
	})
}

// TestDispatchIntent mirrors TestDispatchTool for the intent surface,
// including that it resolves identically to the tool for the same input
// (the doc comment's stated contract) and rejects an unknown intent name.
func TestDispatchIntent(t *testing.T) {
	h := handlers{}
	ctx := context.Background()

	out, err := h.DispatchIntent(ctx, intentName, []byte("Maryam"))
	if err != nil {
		t.Fatalf("DispatchIntent unexpected error: %v", err)
	}
	if want := "Hello, Maryam!"; string(out) != want {
		t.Errorf("DispatchIntent = %q, want %q", string(out), want)
	}

	toolOut, err := h.DispatchTool(ctx, toolName, []byte("Maryam"))
	if err != nil {
		t.Fatalf("DispatchTool unexpected error: %v", err)
	}
	if string(out) != string(toolOut) {
		t.Errorf("DispatchIntent output %q differs from DispatchTool output %q for identical input", out, toolOut)
	}

	t.Run("unknown intent name errors", func(t *testing.T) {
		out, err := h.DispatchIntent(ctx, "not-a-real-intent", []byte("x"))
		if err == nil {
			t.Fatal("DispatchIntent with unknown name: got nil error, want non-nil")
		}
		if out != nil {
			t.Errorf("DispatchIntent with unknown name: got output %q, want nil", out)
		}
		if !strings.Contains(err.Error(), "not-a-real-intent") {
			t.Errorf("error %q does not mention the offending name", err.Error())
		}
	})
}

// TestRunCommand covers the greet command: it prints to stdout (captured by
// redirecting os.Stdout, restored via defer/t.Cleanup — no filesystem
// writes outside process-standard streams, no t.TempDir needed since
// nothing touches disk), handles a present/absent args[0], and rejects an
// unknown command name without printing anything.
func TestRunCommand(t *testing.T) {
	h := handlers{}
	ctx := context.Background()

	t.Run("with name argument", func(t *testing.T) {
		out := captureStdout(t, func() error {
			return h.RunCommand(ctx, commandName, []string{"Bilal"})
		})
		if want := "Hello, Bilal!\n"; out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	t.Run("with no arguments defaults to world", func(t *testing.T) {
		out := captureStdout(t, func() error {
			return h.RunCommand(ctx, commandName, nil)
		})
		if want := "Hello, world!\n"; out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	t.Run("extra arguments beyond args[0] are ignored", func(t *testing.T) {
		out := captureStdout(t, func() error {
			return h.RunCommand(ctx, commandName, []string{"Zaynab", "ignored", "also-ignored"})
		})
		if want := "Hello, Zaynab!\n"; out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
	})

	t.Run("unknown command name errors and prints nothing", func(t *testing.T) {
		var callErr error
		out := captureStdout(t, func() error {
			callErr = h.RunCommand(ctx, "not-a-real-command", []string{"x"})
			return callErr
		})
		if callErr == nil {
			t.Fatal("RunCommand with unknown name: got nil error, want non-nil")
		}
		if !strings.Contains(callErr.Error(), "not-a-real-command") {
			t.Errorf("error %q does not mention the offending name", callErr.Error())
		}
		if out != "" {
			t.Errorf("stdout = %q, want empty (no output on error)", out)
		}
	})
}

// TestGreet unit-tests the shared formatting helper directly across its
// trim/default edge cases.
func TestGreet(t *testing.T) {
	tests := map[string]string{
		"Umar":       "Hello, Umar!",
		"":           "Hello, world!",
		"   ":        "Hello, world!",
		" Khadijah ": "Hello, Khadijah!",
		"\t\nAli\n":  "Hello, Ali!",
	}
	for input, want := range tests {
		if got := greet(input); got != want {
			t.Errorf("greet(%q) = %q, want %q", input, got, want)
		}
	}
}

// captureStdout redirects os.Stdout for the duration of fn, returning
// everything fn wrote to it. fn's own error is asserted by the caller, not
// swallowed here. Restoration is unconditional via t.Cleanup so a failing
// fn never leaves os.Stdout redirected for later tests.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = orig
	})

	fnErr := fn()

	if err := w.Close(); err != nil {
		t.Fatalf("closing pipe writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("reading captured stdout: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("closing pipe reader: %v", err)
	}
	os.Stdout = orig

	_ = fnErr // caller inspects fnErr itself; captured here only to run fn
	return buf.String()
}
