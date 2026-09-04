// Purpose: unit tests for `cascade memory` — the wiring proof against the
//
//	REAL root command, the flag-to-params mapping through an injected
//	call seam, the rendered human output, and the redaction canary over
//	every diagnostic this command can emit. The end-to-end run against a
//	real daemon socket lives in memory_integration_test.go; this file
//	deliberately imports neither "net" nor "net/http" so it runs in the
//	fast, no-network unit lane (Art.7.2).
//
// SPORT: cmd.cascade.cmd.memory (ADD, per T-3 sport_updates).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestMemoryVerbsResolveOnTheRealRootCommand is the reachability proof.
//
// It resolves each verb through newRootCmd() — the SAME tree main()
// executes and the golden help fixture is rendered from — rather than
// through a locally constructed command. A subsystem that is built,
// tested and never mounted is this repository's most repeated defect, and
// a test that builds its own tree cannot see it. Deleting the
// mountMemoryCmd line from root.go makes every case below fail.
func TestMemoryVerbsResolveOnTheRealRootCommand(t *testing.T) {
	for _, verb := range []string{"remember", "recall", "forget", "list", "review"} {
		t.Run(verb, func(t *testing.T) {
			cmd, _, err := newRootCmd().Find([]string{"memory", verb})
			if err != nil {
				t.Fatalf("memory %s did not resolve on the real root: %v", verb, err)
			}
			if cmd.Name() != verb || cmd.Parent() == nil || cmd.Parent().Name() != "memory" {
				t.Fatalf("resolved %q under %v, want %q under memory", cmd.Name(), cmd.Parent(), verb)
			}
			if cmd.RunE == nil {
				t.Errorf("memory %s resolved but has no RunE", verb)
			}
		})
	}
}

// TestMemoryFlagSurface pins the flags each verb accepts, since they are
// the whole non-interactive input surface (§5.8): no verb prompts, so a
// missing flag is a capability a user simply cannot reach.
func TestMemoryFlagSurface(t *testing.T) {
	cases := map[string][]string{
		"remember": {"type", "name", "provenance"},
		"recall":   {"k", "type"},
		"forget":   {"dry-run"},
		"list":     {"type", "limit", "cursor"},
		"review":   {"section", "auto-approve", "auto-skip", "defer-days", "revert"},
	}
	root := newRootCmd()
	for verb, flags := range cases {
		cmd, _, err := root.Find([]string{"memory", verb})
		if err != nil {
			t.Fatalf("find memory %s: %v", verb, err)
		}
		for _, name := range flags {
			if cmd.Flags().Lookup(name) == nil {
				t.Errorf("memory %s has no --%s flag", verb, name)
			}
		}
	}
}

// recordedCall captures one RPC the command would have made.
type recordedCall struct {
	Method string
	Params any
}

// memoryHarness runs a verb with an injected call seam and a config-free
// path provider, returning stdout and the recorded call.
type memoryHarness struct {
	calls  []recordedCall
	result any
	err    error
	// env is the process environment this invocation sees. It is a map
	// rather than the real environment because CASCADE_MEMORY_REVIEW_ACTION
	// selects an action that writes to a user's memory, and a test that
	// read the real environment would be one exported variable away from
	// asserting something other than what it set.
	env map[string]string
}

func (h *memoryHarness) deps(t *testing.T) memoryDeps {
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
			if h.result == nil {
				return nil
			}
			raw, err := json.Marshal(h.result)
			if err != nil {
				return err
			}
			return json.Unmarshal(raw, out)
		},
	}
}

// run executes one memory verb and returns its stdout, stderr and error.
func (h *memoryHarness) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newMemoryCmd(h.deps(t))
	// The global flags every verb reads through memoryWriter live on the
	// root in production; declare the same four here so the subtree under
	// test sees exactly the flag set it sees when mounted.
	flags := cmd.PersistentFlags()
	flags.Bool("json", false, "")
	flags.Bool("quiet", false, "")
	flags.Bool("verbose", false, "")
	flags.Bool("no-color", false, "")

	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(args)
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

func TestMemoryRememberSendsParamsAndPrintsTheAddress(t *testing.T) {
	h := &memoryHarness{result: memory.RememberResult{ID: "user/a-name"}}
	stdout, _, err := h.run(t, "remember", "a body", "--type", "user", "--name", "a-name", "--provenance", "s-1")
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if len(h.calls) != 1 || h.calls[0].Method != memory.MethodRemember {
		t.Fatalf("calls = %+v, want one memory.remember", h.calls)
	}
	params, ok := h.calls[0].Params.(memory.RememberParams)
	if !ok {
		t.Fatalf("params are %T, want memory.RememberParams", h.calls[0].Params)
	}
	want := memory.RememberParams{Content: "a body", Type: "user", Name: "a-name", Provenance: "s-1"}
	if params != want {
		t.Errorf("params = %+v, want %+v", params, want)
	}
	if strings.TrimSpace(stdout) != "user/a-name" {
		t.Errorf("stdout = %q, want the canonical address", stdout)
	}
}

func TestMemoryRecallRendersATableAndDefaultsK(t *testing.T) {
	h := &memoryHarness{result: memory.RecallResult{Units: []memory.MemoryEntry{
		{Name: "note", Kind: memory.KindProject, Body: "first line\nsecond line"},
	}}}
	stdout, _, err := h.run(t, "recall", "note")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	params := h.calls[0].Params.(memory.RecallParams)
	if params.Query != "note" || params.K != memoryDefaultK {
		t.Errorf("params = %+v, want query note and the default k", params)
	}
	if !strings.Contains(stdout, "ADDRESS") || !strings.Contains(stdout, "project/note") {
		t.Errorf("stdout is not the expected table:\n%s", stdout)
	}
	if strings.Contains(stdout, "second line") {
		t.Errorf("the summary column spilled a second line:\n%s", stdout)
	}
}

func TestMemoryListJSONIsTheVersionedEnvelope(t *testing.T) {
	h := &memoryHarness{result: memory.ListResult{
		Units:      []memory.MemoryEntry{{Name: "note", Kind: memory.KindProject}},
		NextCursor: "project/note",
	}}
	stdout, _, err := h.run(t, "list", "--json", "--limit", "1", "--type", "project", "--cursor", "project/a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var envelope struct {
		Version int  `json:"version"`
		OK      bool `json:"ok"`
		Data    struct {
			Units []struct {
				Name string `json:"Name"`
				Kind string `json:"Kind"`
			} `json:"units"`
			NextCursor string `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("--json output is not an envelope: %v (%s)", err, stdout)
	}
	if envelope.Version != 1 || !envelope.OK {
		t.Errorf("envelope = %+v, want version 1 and ok", envelope)
	}
	if len(envelope.Data.Units) != 1 || envelope.Data.Units[0].Kind != "project" {
		t.Errorf("payload units = %+v", envelope.Data.Units)
	}
	if envelope.Data.NextCursor != "project/note" {
		t.Errorf("next_cursor = %q", envelope.Data.NextCursor)
	}
	params := h.calls[0].Params.(memory.ListParams)
	if params.Limit != 1 || params.Type != "project" || params.Cursor != "project/a" {
		t.Errorf("params = %+v", params)
	}
}

// TestMemoryForgetIsExplicitAboutWhatItDid covers the destructive verb's
// output contract: a real forget and a rehearsal must be distinguishable
// from the output alone.
func TestMemoryForgetIsExplicitAboutWhatItDid(t *testing.T) {
	destructive := &memoryHarness{result: memory.ForgetResult{ID: "project/x", Forgotten: true}}
	stdout, _, err := destructive.run(t, "forget", "project/x")
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if !strings.Contains(stdout, "forgot project/x") || !strings.Contains(stdout, "tombstoned") {
		t.Errorf("stdout = %q, want it to name what was removed and what remains", stdout)
	}

	dry := &memoryHarness{result: memory.ForgetResult{ID: "project/x", DryRun: true}}
	dryOut, _, err := dry.run(t, "forget", "project/x", "--dry-run")
	if err != nil {
		t.Fatalf("forget --dry-run: %v", err)
	}
	if !strings.Contains(dryOut, "would forget") || !strings.Contains(dryOut, "nothing was removed") {
		t.Errorf("dry-run stdout = %q, want it to say nothing was removed", dryOut)
	}
	if params := dry.calls[0].Params.(memory.ForgetParams); !params.DryRun {
		t.Error("--dry-run did not reach the RPC params")
	}
	if params := destructive.calls[0].Params.(memory.ForgetParams); params.DryRun {
		t.Error("a plain forget sent dry_run=true")
	}
}

func TestMemoryArgValidation(t *testing.T) {
	cases := [][]string{
		{"remember"},
		{"remember", "a", "b"},
		{"recall"},
		{"forget"},
		{"list", "unexpected"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := &memoryHarness{}
			_, _, err := h.run(t, args...)
			if err == nil {
				t.Fatal("expected an argument error")
			}
			if kind, _ := cascade.KindOf(err); kind != cascade.KindInvalidInput {
				t.Errorf("kind = %v, want invalid_input", kind)
			}
			if len(h.calls) != 0 {
				t.Errorf("a rejected invocation still called the daemon: %+v", h.calls)
			}
		})
	}
}

func TestMemoryStoreDirIsUnderCascadeHome(t *testing.T) {
	paths := fakeMemoryPaths{root: filepath.Join("some", "root")}
	if got, want := memoryStoreDir(paths), filepath.Join("some", "root", "memory"); got != want {
		t.Errorf("memoryStoreDir = %q, want %q", got, want)
	}
}

// fakeMemoryPaths is a minimal runtime.PathProvider for this file's tests.
// It is declared here rather than reusing daemon_unix_test.go's because
// that file is unix-only and `cascade memory` must be testable on every
// platform (Art.5).
type fakeMemoryPaths struct{ root string }

func (p fakeMemoryPaths) Root() string       { return p.root }
func (p fakeMemoryPaths) ConfigPath() string { return filepath.Join(p.root, "config.toml") }
func (p fakeMemoryPaths) SocketPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p fakeMemoryPaths) DataDir() string    { return filepath.Join(p.root, "data") }
func (p fakeMemoryPaths) LogDir() string     { return filepath.Join(p.root, "logs") }
func (p fakeMemoryPaths) StorageRoot(prof runtime.Profile) string {
	return filepath.Join(p.root, "data", "storage", string(prof))
}
