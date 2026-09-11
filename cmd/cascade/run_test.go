// Purpose: unit tests for `cascade run`'s pure logic - flag validation,
//
//	wire-params assembly, non-interactive/TTY rendering, and the
//	streamResult SSE loop - none of which touch a real socket or the
//	real environment (Art.7.2's no-network-unit-lane gate: this file
//	imports neither "net" nor "net/http").
//
// SPORT: cmd/cascade/run (ADD, P1-E11-W3-S23-T1/T3 sport_updates).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func execRunHelp(t *testing.T, args ...string) string {
	t.Helper()
	globalFlags = GlobalFlags{}
	root := newRootCmd() // "run" is not mounted on the real root yet (see run.go's header note)
	root.AddCommand(newRunCmd(runDeps{}))
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %v: %v", args, err)
	}
	return buf.String()
}

func TestRunCmd_TaskValidation(t *testing.T) {
	for _, class := range runTaskClasses {
		if err := validateTaskClass(class); err != nil {
			t.Errorf("validateTaskClass(%q) = %v, want nil", class, err)
		}
	}
	if err := validateTaskClass(""); err == nil {
		t.Error("validateTaskClass(\"\") = nil, want an error (--task is required)")
	}
	err := validateTaskClass("not-a-class")
	if err == nil {
		t.Fatal("validateTaskClass(\"not-a-class\") = nil, want an error")
	}
	for _, class := range runTaskClasses {
		if !strings.Contains(err.Error(), class) {
			t.Errorf("error %q does not list valid class %q", err, class)
		}
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("error kind = %v, want KindInvalidInput", err)
	}
}

func TestRunCmd_DryRunParam(t *testing.T) {
	flags := runFlags{Task: "chat", DryRun: true, Input: []string{"user:hi"}}
	params, err := buildRunParams(flags, strings.NewReader(""))
	if err != nil {
		t.Fatalf("buildRunParams: %v", err)
	}
	if !params.DryRun {
		t.Fatal("assembled request params: dry_run = false, want true")
	}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if !strings.Contains(string(body), `"dry_run":true`) {
		t.Fatalf("marshaled params %s do not carry \"dry_run\":true", body)
	}
}

func TestRunCmd_RequireMapping(t *testing.T) {
	req, err := buildRequirements(map[string]string{"reasoning": "high", "context": "8000", "structured": "true"})
	if err != nil {
		t.Fatalf("buildRequirements: %v", err)
	}
	if req.Reasoning != "high" || req.Context != 8000 || !req.Structured {
		t.Fatalf("buildRequirements = %+v, want {high 8000 true}", req)
	}
	if _, err := buildRequirements(map[string]string{"bogus": "x"}); err == nil {
		t.Fatal("buildRequirements with an unknown key succeeded, want a refusal")
	}
}

func TestRunCmd_NonInteractiveJSON(t *testing.T) {
	resp := provider.ModelResponse{JobID: "job-1", Output: "hello", Usage: provider.Usage{InputTokens: 3, OutputTokens: 5}}
	view := toRunResultView(resp)
	if view.Legs == nil {
		t.Fatal("single-dispatch view.Legs is nil, want a non-nil empty slice")
	}
	if len(view.Legs) != 0 {
		t.Fatalf("single-dispatch view.Legs = %v, want empty", view.Legs)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if !strings.Contains(string(body), `"legs":[]`) {
		t.Fatalf("envelope %s does not carry \"legs\":[] for a single dispatch", body)
	}
	if !strings.Contains(string(body), `"job_id":"job-1"`) || !strings.Contains(string(body), `"output":"hello"`) {
		t.Fatalf("envelope %s missing job_id/output", body)
	}
}

func TestRunCmd_FanOutLegsRendered(t *testing.T) {
	resp := provider.ModelResponse{
		JobID:  "parent",
		Output: "", // R-21.214: parent output always empty
		Legs: []provider.ModelResponse{
			{JobID: "leg-0", Output: "a"},
			{JobID: "leg-1", Output: "b"},
			{JobID: "leg-2", Output: "c"},
		},
	}

	// Non-interactive envelope: a 3-element legs array, parent output
	// still empty.
	view := toRunResultView(resp)
	if len(view.Legs) != 3 {
		t.Fatalf("len(view.Legs) = %d, want 3", len(view.Legs))
	}
	if view.Output != "" {
		t.Fatalf("parent view.Output = %q, want empty (R-21.214: never assembled by concatenating legs)", view.Output)
	}
	for i, want := range []string{"a", "b", "c"} {
		if view.Legs[i].Output != want {
			t.Errorf("view.Legs[%d].Output = %q, want %q (index order)", i, view.Legs[i].Output, want)
		}
	}

	// TTY path: each leg's output printed in index order, separated by a
	// blank line, parent output never concatenated in.
	var out, errOut bytes.Buffer
	w := output.New(&out, &errOut, false, false, false, false)
	if err := renderRunResult(w, resp); err != nil {
		t.Fatalf("renderRunResult: %v", err)
	}
	if got := out.String(); got != "a\n\nb\n\nc\n" {
		t.Fatalf("TTY rendering = %q, want %q", got, "a\n\nb\n\nc\n")
	}
}

func TestRunCmd_StreamResult(t *testing.T) {
	body := "id: 1\ndata: {\"kind\":\"delta\"}\n\nid: 2\ndata: {\"kind\":\"done\"}\n\n"
	var out bytes.Buffer
	if err := streamResult(context.Background(), &out, strings.NewReader(body)); err != nil {
		t.Fatalf("streamResult: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `{"kind":"delta"}`) || !strings.Contains(got, `{"kind":"done"}`) {
		t.Fatalf("streamResult output = %q, want both payloads written", got)
	}
}

func TestRunCmd_StreamResult_MalformedLinesIgnored(t *testing.T) {
	body := "not-sse-at-all\n: keep-alive\ndata:\ndata: ok\n"
	var out bytes.Buffer
	if err := streamResult(context.Background(), &out, strings.NewReader(body)); err != nil {
		t.Fatalf("streamResult: %v", err)
	}
	if got := out.String(); got != "ok\n" {
		t.Fatalf("streamResult output = %q, want only the one real payload", got)
	}
}

func TestRunCmd_StreamResult_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	err := streamResult(ctx, &out, strings.NewReader("data: x\n"))
	if err == nil {
		t.Fatal("streamResult with an already-cancelled context succeeded, want an error")
	}
}

func TestRunCmd_StreamFlagVisible(t *testing.T) {
	help := execRunHelp(t, "run", "--help")
	if !strings.Contains(help, "--stream") {
		t.Fatalf("`cascade run --help` does not list --stream:\n%s", help)
	}
	if strings.Contains(help, "--fan-out") {
		t.Fatalf("`cascade run --help` lists --fan-out, want it MarkHidden:\n%s", help)
	}
}

func TestRunCmd_HelpListsFlags(t *testing.T) {
	help := execRunHelp(t, "run", "--help")
	for _, flag := range []string{"--task", "--require", "--sensitivity", "--dry-run"} {
		if !strings.Contains(help, flag) {
			t.Errorf("`cascade run --help` does not list %s:\n%s", flag, help)
		}
	}
}

func TestRunCmd_AppearsInRootHelp(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	root.AddCommand(newRunCmd(runDeps{}))
	buf := &bytes.Buffer{}
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute --help: %v", err)
	}
	if !strings.Contains(buf.String(), "run") {
		t.Fatalf("`cascade --help` does not list run:\n%s", buf.String())
	}
}

func TestRunCmd_OnlyModelDoor(t *testing.T) {
	// 07 note 9: run is the sole location for model-dispatch flags. No
	// other cobra command in cmd/cascade declares --task/--require/
	// --sensitivity/--fan-out/--stream.
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	runCmd := newRunCmd(runDeps{})
	root.AddCommand(runCmd)

	// CONTRACT DEVIATION (recorded, not papered over): the ticket's literal
	// wording names --task and --stream among the flags no OTHER command
	// may declare, but `cascade context scope show --task` (an unrelated
	// active-task-id attachment) and `cascade fleet journal replay
	// --stream`/`cascade journal replay --stream` (unrelated NDJSON log
	// streaming) already exist in the tree, pre-dating this ticket, for
	// meanings that have nothing to do with model dispatch. Asserting the
	// literal text would fail against real, unrelated, already-shipped
	// commands rather than catch a genuine second model door. --require,
	// --sensitivity and --fan-out have no such pre-existing collision and
	// are the flags this check can assert without a false positive; see
	// the journal for both sides quoted.
	modelFlags := []string{"require", "sensitivity", "fan-out"}
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd != runCmd {
			for _, name := range modelFlags {
				if cmd.Flags().Lookup(name) != nil {
					t.Errorf("command %q declares model-dispatch flag --%s, want it declared only on run", cmd.CommandPath(), name)
				}
			}
		}
		for _, child := range cmd.Commands() {
			walk(child)
		}
	}
	walk(root)
}
