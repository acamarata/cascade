// Purpose: tests for `cascade fleet learn` -- the learn verbs' owner-only
//
//	declaration (TestLearnVerbsDeclared), the mount on the root tree, the
//	CASCADE_NO_INPUT=1 closed-stdin parity for all three commands
//	(TestFleetLearnNoInput), and explain/reset argument validation and
//	rendering. Daemon-side behaviour lives with the daemon handler.
//
// SPORT: cmd/cascade/fleet-learn-cli (ADD, P1-LRN-04).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// learnVerbs lists the three learn verbs this group declares.
var learnVerbs = []string{learnStatusMethod, learnExplainMethod, learnResetMethod}

// TestLearnVerbsDeclared proves learn.status, learn.explain and learn.reset
// are declared AccessOwnerOnly: a principal caller and a caller-less call
// are refused before the handler runs, and the owner CLI is admitted.
func TestLearnVerbsDeclared(t *testing.T) {
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	ran := map[string]int{}
	for _, method := range learnVerbs {
		m := method
		reg.Register(m, func(context.Context, json.RawMessage) (any, error) { ran[m]++; return struct{}{}, nil })
	}
	principalCtx := principal.WithCaller(t.Context(), principal.Caller{Principal: &principal.Principal{ID: "learn-test-principal"}})
	ownerCtx := principal.WithCaller(t.Context(), principal.Caller{OwnerCLI: true})
	for _, method := range learnVerbs {
		req := &rpc.Request{JSONRPC: "2.0", Method: method, Params: json.RawMessage(`{}`)}
		if _, eo := reg.Dispatch(principalCtx, req); eo == nil || ran[method] != 0 {
			t.Fatalf("%s: principal dispatch admitted (handler ran %d); the verb must be AccessOwnerOnly", method, ran[method])
		}
		if _, eo := reg.Dispatch(t.Context(), req); eo == nil || ran[method] != 0 {
			t.Fatalf("%s: caller-less dispatch admitted (handler ran %d); the verb must be AccessOwnerOnly", method, ran[method])
		}
		if _, eo := reg.Dispatch(ownerCtx, req); eo != nil || ran[method] != 1 {
			t.Fatalf("%s: owner dispatch = %v (handler ran %d), want admitted once", method, eo, ran[method])
		}
	}
}

// closedStdin returns a readable end whose writer is already closed, so any
// stdin read sees EOF immediately and can never block the test.
func closedStdin(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// TestFleetLearnMounted proves the three verbs resolve on the real root
// command tree under the fleet group.
func TestFleetLearnMounted(t *testing.T) {
	globalFlags = GlobalFlags{}
	root := newRootCmd()
	for _, name := range []string{"status", "explain", "reset"} {
		found, _, err := root.Find([]string{"fleet", "learn", name})
		if err != nil || found == nil || found.Name() != name {
			t.Fatalf("fleet learn %s is not mounted on the root command: found=%v err=%v", name, safeName(found), err)
		}
	}
}

// TestFleetLearnNoInput proves that with CASCADE_NO_INPUT=1 and a closed
// stdin, all three commands return their refusal without prompting or
// blocking: the no-daemon refusal for status/explain/reset, and the
// argument refusal for an unscoped reset.
func TestFleetLearnNoInput(t *testing.T) {
	t.Setenv("CASCADE_NO_INPUT", "1")
	cases := []struct {
		name string
		args []string
		kind cascade.Kind
		msg  string
	}{
		{"status", []string{"status"}, cascade.KindUnavailable, "cascade daemon run"},
		{"explain", []string{"explain", "global", "code"}, cascade.KindUnavailable, "cascade daemon run"},
		{"reset all", []string{"reset", "--all"}, cascade.KindUnavailable, "cascade daemon run"},
		{"reset unscoped", []string{"reset"}, cascade.KindInvalidInput, "unscoped reset"},
	}
	for _, tc := range cases {
		in := closedStdin(t)
		done := make(chan error, 1)
		go func() {
			cmd := newFleetLearnCmd(fleetSessionsDeps{})
			cmd.SetIn(in)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tc.args)
			done <- cmd.Execute()
		}()
		var err error
		select {
		case err = <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: command blocked under CASCADE_NO_INPUT=1 with closed stdin", tc.name)
		}
		k, _ := cascade.KindOf(err)
		if err == nil || k != tc.kind || !strings.Contains(err.Error(), tc.msg) {
			t.Fatalf("%s: err = %v (kind %v), want kind %v containing %q", tc.name, err, k, tc.kind, tc.msg)
		}
	}
}

// TestFleetLearnReset_Validation proves the reset flag combinations are
// checked before any dial: an unscoped reset, --all with a filter, and a
// malformed --scope are refused as invalid input; a valid filter or --all
// reaches the dial and refuses only for the missing daemon.
func TestFleetLearnReset_Validation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		kind cascade.Kind
	}{
		{"unscoped", []string{}, cascade.KindInvalidInput},
		{"all with scope", []string{"--all", "--scope", "global"}, cascade.KindInvalidInput},
		{"bad scope", []string{"--scope", "bogus:thing"}, cascade.KindInvalidInput},
		{"tier filter", []string{"--tier", "T1"}, cascade.KindUnavailable},
		{"all", []string{"--all"}, cascade.KindUnavailable},
	}
	for _, tc := range cases {
		cmd := newFleetLearnResetCmd(fleetSessionsDeps{})
		cmd.SetArgs(tc.args)
		err := cmd.Execute()
		if k, _ := cascade.KindOf(err); err == nil || k != tc.kind {
			t.Fatalf("reset %s: err = %v, want kind %v", tc.name, err, tc.kind)
		}
	}
}

// TestFleetLearnExplain_ArgsAndScope proves explain takes exactly two
// positional arguments and refuses a malformed scope key before dialing.
func TestFleetLearnExplain_ArgsAndScope(t *testing.T) {
	for _, args := range [][]string{{"global"}, {"global", "code", "extra"}} {
		cmd := newFleetLearnExplainCmd(fleetSessionsDeps{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("fleet learn explain %v: want an argument error, got nil", args)
		}
	}
	cmd := newFleetLearnExplainCmd(fleetSessionsDeps{})
	cmd.SetArgs([]string{"bogus:thing", "code"})
	if k, _ := cascade.KindOf(cmd.Execute()); k != cascade.KindInvalidInput {
		t.Fatalf("explain with a malformed scope key: kind %v, want %v", k, cascade.KindInvalidInput)
	}
}

// TestFleetLearnExplain_Rendering proves the explain summary names the
// missing estimates explicitly and renders present ones with their fields.
func TestFleetLearnExplain_Rendering(t *testing.T) {
	empty := learnExplainResult{Threshold: 0.25}.String()
	for _, want := range []string{"scores: none", "tier0: none", "best_lower: none", "threshold=0.250 jump_fires=false"} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty explain = %q, want it to contain %q", empty, want)
		}
	}
	full := learnExplainResult{
		Scores:    []learnScoreRow{{Tier: "T1", Score: 0.5, Alpha: 2, Beta: 2, Observations: 4, FallbackLevel: "repo"}},
		Tier0:     &learnExplainEstimate{Tier: "T0", QueueWait: time.Second, PRework: 0.1, ReworkCyclesEst: 2},
		BestLower: &learnExplainEstimate{Tier: "T1"},
		Threshold: 0.5,
		JumpFires: true,
	}.String()
	for _, want := range []string{"score tier=T1 score=0.500", "tier0 tier=T0 queue_wait=1s", "best_lower tier=T1", "jump_fires=true"} {
		if !strings.Contains(full, want) {
			t.Errorf("full explain = %q, want it to contain %q", full, want)
		}
	}
}

// TestFleetLearnReset_Rendering proves the reset outcome line.
func TestFleetLearnReset_Rendering(t *testing.T) {
	if got := (learnResetResult{RowsDeleted: 3, Changed: true}).String(); got != "deleted 3 rows changed=true" {
		t.Fatalf("reset render = %q", got)
	}
}
