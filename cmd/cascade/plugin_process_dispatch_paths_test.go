// Purpose: the generic process-verb dispatch edges that need no live
// plugin: the human renderer keeps a plugin result's numbers exact, a
// verb without an rpc_method refuses, a non-refusal launch error keeps its
// own Kind, and a non-JSON result is a typed internal error.
// SPORT: cmd/cascade:plugin-process-dispatch tests (ADD).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// renderCmd is a bare command whose output lands in the returned buffer.
func renderCmd(t *testing.T, jsonMode bool) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{Use: "verb"}
	cmd.Flags().Bool("json", jsonMode, "")
	cmd.Flags().Bool("no-color", true, "")
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	return cmd, buf
}

// TestProcessVerbHumanOutputKeepsLargeIntegers: an id past 2^53 must print
// exactly as the plugin sent it, not rounded through float64.
func TestProcessVerbHumanOutputKeepsLargeIntegers(t *testing.T) {
	cmd, buf := renderCmd(t, false)
	if err := renderProcessVerb(cmd, "p", []byte(`{"id":9007199254740993,"name":"x"}`)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "untrusted plugin output (p):") || !strings.Contains(out, "9007199254740993") {
		t.Fatalf("human output = %q, want the header and the exact id 9007199254740993", out)
	}
}

func TestProcessVerbHumanOutputEscapesControlCharacters(t *testing.T) {
	cmd, buf := renderCmd(t, false)
	if err := renderProcessVerb(cmd, "p", []byte(`{"name":"a\u001b[31mb"}`)); err != nil {
		t.Fatal(err)
	}
	if out := buf.String(); strings.ContainsRune(out, 0x1b) || !strings.Contains(out, `\u001b`) {
		t.Fatalf("human output = %q, want the escape byte kept escaped", out)
	}
}

func TestProcessVerbJSONOutputCarriesProvenance(t *testing.T) {
	cmd, buf := renderCmd(t, true)
	if err := renderProcessVerb(cmd, "p", []byte(`[1,2]`)); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data processVerbOutput `json:"data"`
	}
	out := buf.String()
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Data.Provenance != "untrusted-source" ||
		env.Data.Plugin != "p" || compactJSON(t, env.Data.Result) != "[1,2]" {
		t.Fatalf("json output = %q, want provenance and the raw result", out)
	}
}

// compactJSON returns raw without insignificant whitespace.
func compactJSON(t *testing.T, raw []byte) string {
	t.Helper()
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatalf("compact %q: %v", raw, err)
	}
	return b.String()
}

func TestProcessVerbRefusesNonJSONResult(t *testing.T) {
	for _, raw := range []string{"not json", `{"a":1} trailing`} {
		cmd, buf := renderCmd(t, false)
		err := renderProcessVerb(cmd, "p", []byte(raw))
		if !cascade.HasKind(err, cascade.KindInternal) || !strings.Contains(err.Error(), "not JSON") || buf.Len() != 0 {
			t.Fatalf("%q: err = %v, output %q; want a typed internal error and no output", raw, err, buf.String())
		}
	}
}

func TestProcessVerbWithoutRPCMethodRefuses(t *testing.T) {
	cmd, _ := renderCmd(t, false)
	err := dispatchProcessVerb(cmd, nil, processPluginCommand{PluginID: "p", Spec: plugin.CommandSpec{Name: "p-x"}})
	if !cascade.HasKind(err, cascade.KindUnsupported) || !strings.Contains(err.Error(), "no rpc_method") {
		t.Fatalf("err = %v, want KindUnsupported naming the missing rpc_method", err)
	}
}

// TestProcessVerbLaunchErrorKeepsItsKind: only NotFound and PolicyDenied
// become the add remedy; any other launch failure surfaces unchanged.
func TestProcessVerbLaunchErrorKeepsItsKind(t *testing.T) {
	prev := processVerbLauncher
	processVerbLauncher = func(context.Context, string) (*plugins.TrustedHandle, error) {
		return nil, cascade.New(cascade.KindInternal, "store unreadable")
	}
	t.Cleanup(func() { processVerbLauncher = prev })
	cmd, _ := renderCmd(t, false)
	err := dispatchProcessVerb(cmd, []string{"list"}, processPluginCommand{PluginID: "p", Spec: plugin.CommandSpec{Name: "p-x", RPCMethod: "p.cmd.x"}})
	if !cascade.HasKind(err, cascade.KindInternal) || strings.Contains(err.Error(), "cascade plugin add") {
		t.Fatalf("err = %v, want the launch error with its own Kind and no add remedy", err)
	}
}

func TestProcessVerbPolicyDeniedNamesAddRemedy(t *testing.T) {
	prev := processVerbLauncher
	processVerbLauncher = func(context.Context, string) (*plugins.TrustedHandle, error) {
		return nil, cascade.New(cascade.KindPolicyDenied, "digest mismatch")
	}
	t.Cleanup(func() { processVerbLauncher = prev })
	cmd, _ := renderCmd(t, false)
	err := dispatchProcessVerb(cmd, nil, processPluginCommand{PluginID: "p", Spec: plugin.CommandSpec{Name: "p-x", RPCMethod: "p.cmd.x"}})
	if !cascade.HasKind(err, cascade.KindUnavailable) || !strings.Contains(err.Error(), "`cascade plugin add p`") {
		t.Fatalf("err = %v, want KindUnavailable naming `cascade plugin add p`", err)
	}
}
