// Purpose: unit tests for `cascade memory consolidate` — the wiring proof
//
//	against the REAL root command, the flag-to-params mapping through the
//	injected call seam, and the rendered output for every shape the report
//	can take. Like memory_test.go this file imports neither "net" nor
//	"net/http", so it runs in the fast, no-network unit lane (Art.7.2).
//
// SPORT: cmd.cascade.cmd.memory.consolidate (ADD, P1-E07-W2-S13-T4).
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/memory"
)

// TestMemoryConsolidateResolvesOnTheRealRootCommand is the reachability
// proof: the verb must be on the tree main() executes, not merely on one a
// test built. Deleting the newMemoryConsolidateCmd line from memory.go
// makes this fail.
func TestMemoryConsolidateResolvesOnTheRealRootCommand(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"memory", "consolidate"})
	if err != nil {
		t.Fatalf("memory consolidate did not resolve on the real root: %v", err)
	}
	if cmd.Name() != "consolidate" || cmd.Parent() == nil || cmd.Parent().Name() != "memory" {
		t.Fatalf("resolved %q under %v, want consolidate under memory", cmd.Name(), cmd.Parent())
	}
	if cmd.RunE == nil {
		t.Fatal("memory consolidate resolved but has no RunE")
	}
	if cmd.Flags().Lookup("dry-run") == nil {
		t.Error("memory consolidate has no --dry-run flag, so there is no way to rehearse a destructive job")
	}
}

// TestMemoryConsolidateSendsTheRightCall proves the flag reaches the RPC
// params rather than being accepted and dropped — the failure that would
// turn a rehearsal into a real retirement.
func TestMemoryConsolidateSendsTheRightCall(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		h := &memoryHarness{result: memory.ConsolidationReport{}}
		args := []string{"consolidate"}
		if dryRun {
			args = append(args, "--dry-run")
		}
		if _, _, err := h.run(t, args...); err != nil {
			t.Fatalf("dry-run=%v: %v", dryRun, err)
		}
		if len(h.calls) != 1 || h.calls[0].Method != memory.MethodConsolidate {
			t.Fatalf("calls = %+v, want one %s", h.calls, memory.MethodConsolidate)
		}
		params, ok := h.calls[0].Params.(memory.ConsolidateParams)
		if !ok {
			t.Fatalf("params are %T, want memory.ConsolidateParams", h.calls[0].Params)
		}
		if params.DryRun != dryRun {
			t.Errorf("params.DryRun = %v, want %v", params.DryRun, dryRun)
		}
	}
}

// TestMemoryConsolidateOutput pins what the command SAYS for each report
// shape. A job that retires a user's records and prints only a count
// leaves them no way to tell which records went, so every case here checks
// the addresses are named.
func TestMemoryConsolidateOutput(t *testing.T) {
	for _, tc := range consolidateOutputCases() {
		t.Run(tc.name, func(t *testing.T) {
			h := &memoryHarness{result: tc.report}
			out, _, err := h.run(t, "consolidate")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output %q does not contain %q", out, want)
				}
			}
		})
	}
}

// consolidateOutputCase is one report shape and the phrases its rendering
// must carry.
type consolidateOutputCase struct {
	name   string
	report memory.ConsolidationReport
	want   []string
}

// consolidateOutputCases is the table TestMemoryConsolidateOutput drives.
// Split from the test under the function-length cap.
func consolidateOutputCases() []consolidateOutputCase {
	return []consolidateOutputCase{
		{
			name: "merged",
			report: memory.ConsolidationReport{
				Merged: 1, Retired: 2, Method: "exact-hash",
				Groups: []memory.ConsolidationGroup{{
					CanonicalID: "project/kept",
					MemberIDs:   []string{"project/gone-a", "project/gone-b"},
				}},
			},
			want: []string{"consolidated 1 group", "retiring 2 record",
				"project/kept <- project/gone-a, project/gone-b", "consolidations/ directory"},
		},
		{
			name:   "nothing to do",
			report: memory.ConsolidationReport{NoChange: true, Method: "exact-hash"},
			want:   []string{"nothing to consolidate"},
		},
		{
			name: "dry run",
			report: memory.ConsolidationReport{
				DryRun: true, Method: "exact-hash",
				Groups: []memory.ConsolidationGroup{{
					CanonicalID: "project/kept", MemberIDs: []string{"project/gone-a"},
				}},
			},
			want: []string{"dry run: would consolidate 1 group", "retiring 1 record", "project/kept <- project/gone-a"},
		},
		{
			name:   "skipped",
			report: memory.ConsolidationReport{Skipped: true, Method: "exact-hash"},
			want:   []string{"already running"},
		},
		{
			name: "unreadable",
			report: memory.ConsolidationReport{
				NoChange: true, Method: "exact-hash", Unreadable: []string{"project/broken"},
			},
			want: []string{"unreadable, left untouched: project/broken"},
		},
	}
}

// TestMemoryConsolidateDryRunNeverClaimsARetirement is the safety canary
// on the rendering: a rehearsal must never print the sentence that says
// records were removed.
func TestMemoryConsolidateDryRunNeverClaimsARetirement(t *testing.T) {
	report := memory.ConsolidationReport{
		DryRun: true, Retired: 0, Method: "exact-hash",
		Groups: []memory.ConsolidationGroup{{
			CanonicalID: "project/kept", MemberIDs: []string{"project/gone"},
		}},
	}
	h := &memoryHarness{result: report}
	out, _, err := h.run(t, "consolidate", "--dry-run")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out, "consolidated 1") || strings.Contains(out, "is recorded in") {
		t.Errorf("a rehearsal claimed work it did not do: %q", out)
	}
}

// TestMemoryConsolidateJSONIsTheReportShape proves --json emits the
// ConsolidationReport itself, so the human view and the machine view
// cannot drift apart.
func TestMemoryConsolidateJSONIsTheReportShape(t *testing.T) {
	report := memory.ConsolidationReport{Merged: 2, Retired: 3, Method: "exact-hash"}
	h := &memoryHarness{result: report}
	out, _, err := h.run(t, "consolidate", "--json")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("--json emitted invalid JSON: %q", out)
	}
	for _, want := range []string{`"merged":2`, `"retired":3`, `"exact-hash"`} {
		if !strings.Contains(strings.ReplaceAll(out, " ", ""), want) {
			t.Errorf("the JSON envelope does not carry %s: %s", want, out)
		}
	}
}
