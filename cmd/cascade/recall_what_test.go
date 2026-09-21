// Purpose: unit tests for `cascade recall what` — reachability on the
// real root command, the query-to-params mapping (positional query only,
// cwd auto-supplied, no --scope flag — 07-CLI-COMMAND-TREE §recall
// ratifies none beyond the query), an end-to-end run against the REAL
// rpc.Registry/RecallWhatHandler pipeline, and the view's rendering
// (table, --json, withheld vs. truncated wording, domain-unavailable
// summary). This file's own registry double stands in for the domain
// legs and the scope/egress seams (fake pass-throughs): the actual
// security enforcement those seams provide is proven at the
// internal/retrieval level (recallwhat_test.go/recallwhat_redact_test.go)
// against the real egress.Engine; this file's job is proving the CLI, the
// wire method name and the rendered shape agree with what the daemon
// composition root builds, not re-proving the policy.
//
// SPORT: cmd.cascade.cmd.recall.what (ADD, P1-E22-W5-S47-T1).
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/retrieval"
	"github.com/acamarata/cascade/internal/retrieval/citations"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
)

// TestRecallWhatResolvesOnTheRealRootCommand is the reachability proof,
// mirroring TestRecallResolvesOnTheRealRootCommand: a subsystem built,
// tested, and never mounted is this repository's most repeated defect.
func TestRecallWhatResolvesOnTheRealRootCommand(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"recall", "what"})
	if err != nil {
		t.Fatalf("recall what did not resolve on the real root: %v", err)
	}
	if cmd.Name() != "what" {
		t.Fatalf("resolved %q, want what", cmd.Name())
	}
	if cmd.RunE == nil {
		t.Fatal("recall what resolved but has no RunE")
	}
	// 07-CLI-COMMAND-TREE §recall ratifies no flag on `what` beyond the
	// positional query; assert none of the bare `recall` flags leaked in.
	for _, flag := range []string{"corpus", "scope", "k", "cite"} {
		if cmd.Flags().Lookup(flag) != nil {
			t.Errorf("recall what unexpectedly has --%s (no ratified flag beyond the query)", flag)
		}
	}
}

// fakeWhatFilesLeg/fakeWhatConvLeg/fakeWhatMemoryLeg/fakeWhatScope/
// fakeWhatEgress are minimal pass-through doubles for this file's wiring
// proof only (see file header).
type fakeWhatFilesLeg struct{ resp recall.Response }

func (f fakeWhatFilesLeg) Query(context.Context, recall.Request) (recall.Response, error) {
	return f.resp, nil
}

type fakeWhatScope struct{}

func (fakeWhatScope) Resolve(context.Context, scope.ResolveInput) (scope.SessionScope, error) {
	return scope.SessionScope{Kind: scope.ScopeKindSession, Project: "proj1"}, nil
}

// testCLIEgress passes content through unchanged: this file's job is
// proving CLI wiring, not re-proving the substitution/exclusion policy
// (see file header) — that is proven against the real egress.Engine at
// the internal/retrieval level.
type testCLIEgress struct{}

func (testCLIEgress) InterceptClass(_ context.Context, _ egress.EgressClass, _ egress.SensitivityTier, content []byte) ([]byte, error) {
	return content, nil
}

// recallWhatTestRegistry builds a real rpc.Registry with recall.what
// bound to a real retrieval.RecallWhatService — the same constructor
// chain cmd/cascade/daemon_unix_recall_what.go's composition root uses —
// over a files-only leg answering one fixed hit, so the CLI's rendering
// has something real to render.
func recallWhatTestRegistry(t *testing.T) *rpc.Registry {
	t.Helper()
	files := fakeWhatFilesLeg{resp: recall.Response{
		Results: []recall.Result{{ChunkID: "c1", Path: "handbook/fusion.md", CorpusID: "handbook", Trust: corpus.TrustTrusted, Score: 0.9}},
		Citations: []citations.Citation{
			{ChunkID: "c1", Path: "handbook/fusion.md", CorpusID: "handbook", Trust: corpus.TrustTrusted, Rank: 1, Score: 0.9},
		},
	}}
	svc, err := retrieval.NewRecallWhatService(files, nil, nil, rrf.Params{}, nil, fakeWhatScope{}, testCLIEgress{})
	if err != nil {
		t.Fatalf("NewRecallWhatService: %v", err)
	}
	registry := rpc.NewRegistry()
	retrieval.NewRecallWhatHandler(svc).Register(registry)
	return registry
}

// TestRecallWhatSendsQueryAndCwdOnly proves the params-mapping contract:
// no --scope, no tier, just the positional query and the auto-supplied
// cwd (D5.1: the caller asserts nothing about scope).
func TestRecallWhatSendsQueryAndCwdOnly(t *testing.T) {
	h := &recallHarness{result: retrieval.WhatResult{}}
	if _, _, err := h.run(t, "what", "a query"); err != nil {
		t.Fatalf("recall what: %v", err)
	}
	if len(h.calls) != 1 || h.calls[0].Method != retrieval.MethodWhat {
		t.Fatalf("calls = %+v, want one recall.what", h.calls)
	}
	params, ok := h.calls[0].Params.(retrieval.WhatParams)
	if !ok {
		t.Fatalf("params are %T, want retrieval.WhatParams", h.calls[0].Params)
	}
	if params.Query != "a query" {
		t.Errorf("Query = %q, want %q", params.Query, "a query")
	}
	if params.Scope != "" {
		t.Errorf("Scope = %q, want empty (server-resolved, never CLI-asserted)", params.Scope)
	}
	if params.Cwd == "" {
		t.Error("Cwd was not populated from Getwd")
	}
}

// TestRecallWhatEndToEndThroughTheRealRegistry drives the REAL command
// against the REAL rpc.Registry/RecallWhatHandler/RecallWhatService
// pipeline (acceptance criterion 1: "returns a unified ranked list...").
func TestRecallWhatEndToEndThroughTheRealRegistry(t *testing.T) {
	h := &recallHarness{dispatch: recallWhatTestRegistry(t)}
	stdout, _, err := h.run(t, "what", "reciprocal rank fusion")
	if err != nil {
		t.Fatalf("recall what: %v", err)
	}
	if !strings.Contains(stdout, "RANK") || !strings.Contains(stdout, "DOMAIN") {
		t.Fatalf("stdout is not the expected table:\n%s", stdout)
	}
	if !strings.Contains(stdout, "file") || !strings.Contains(stdout, "fusion.md") {
		t.Fatalf("stdout does not show the files-domain row:\n%s", stdout)
	}
}

// TestRecallWhatJSONIsTheVersionedEnvelope pins the --json contract for
// `what`, the same way TestRecallJSONIsTheVersionedEnvelope does for the
// bare command.
func TestRecallWhatJSONIsTheVersionedEnvelope(t *testing.T) {
	h := &recallHarness{result: retrieval.WhatResult{
		Query:   "q",
		Results: []retrieval.RecallWhatResult{{Rank: 1, Domain: "file", ID: "file:c1", Path: "a/b.md", Score: 0.5}},
		Legs:    []string{"file"},
	}}
	stdout, _, err := h.run(t, "what", "q", "--json")
	if err != nil {
		t.Fatalf("recall what: %v", err)
	}
	var envelope struct {
		Data retrieval.WhatResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	if len(envelope.Data.Results) != 1 || envelope.Data.Results[0].Path != "a/b.md" {
		t.Errorf("envelope data = %+v", envelope.Data)
	}
}

// TestRecallWhatWithheldAndTruncatedUseDifferentWords is fix item 4's CLI
// proof: a k-cap overflow and a privacy/authorization withholding are
// never described with the same words.
func TestRecallWhatWithheldAndTruncatedUseDifferentWords(t *testing.T) {
	h := &recallHarness{result: retrieval.WhatResult{
		Results:   []retrieval.RecallWhatResult{{Rank: 1, Domain: "file", ID: "file:c1"}},
		Withheld:  2,
		Truncated: 3,
	}}
	stdout, _, err := h.run(t, "what", "q")
	if err != nil {
		t.Fatalf("recall what: %v", err)
	}
	if !strings.Contains(stdout, "2 result(s) withheld: excluded by scope or privacy policy") {
		t.Errorf("stdout does not report the withheld count with the privacy wording:\n%s", stdout)
	}
	if !strings.Contains(stdout, "3 further result(s) not shown (result cap)") {
		t.Errorf("stdout does not report the truncated count with its own wording:\n%s", stdout)
	}
}

// TestRecallWhatEmptyMatchIsNotAnError mirrors TestRecallEmptyMatchIsNotAnError.
func TestRecallWhatEmptyMatchIsNotAnError(t *testing.T) {
	h := &recallHarness{result: retrieval.WhatResult{}}
	stdout, _, err := h.run(t, "what", "kumquat marmalade")
	if err != nil {
		t.Fatalf("an empty match must exit zero: %v", err)
	}
	if strings.TrimSpace(stdout) != "no results" {
		t.Errorf("stdout = %q, want a plain statement that nothing matched", stdout)
	}
}

// TestRecallWhatDomainErrorsSummarized proves the CLI table drops
// per-domain error detail to a count (D4), while --json keeps it.
func TestRecallWhatDomainErrorsSummarized(t *testing.T) {
	h := &recallHarness{result: retrieval.WhatResult{
		Results: []retrieval.RecallWhatResult{{Rank: 1, Domain: "memory", ID: "memory:a"}},
		Errors:  map[string]string{"file": "files index unreadable"},
	}}
	stdout, _, err := h.run(t, "what", "q")
	if err != nil {
		t.Fatalf("recall what: %v", err)
	}
	if strings.Contains(stdout, "files index unreadable") {
		t.Errorf("the human table leaked per-domain error detail:\n%s", stdout)
	}
	if !strings.Contains(stdout, "1 domain(s) unavailable") {
		t.Errorf("stdout does not summarize the domain error count:\n%s", stdout)
	}
}

func TestRecallWhatSourceFallsBackToID(t *testing.T) {
	h := &recallHarness{result: retrieval.WhatResult{Results: []retrieval.RecallWhatResult{
		{Rank: 1, Domain: "memory", ID: "memory:project/a"},
	}}}
	stdout, _, err := h.run(t, "what", "q")
	if err != nil {
		t.Fatalf("recall what: %v", err)
	}
	if !strings.Contains(stdout, "memory:project/a") {
		t.Errorf("a pathless result was not identified by its id:\n%s", stdout)
	}
}
