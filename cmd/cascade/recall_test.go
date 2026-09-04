// Purpose: unit tests for `cascade recall` — the wiring proof against the
//
//	REAL root command, the flag-to-params mapping through an injected
//	call seam, an end-to-end run of the REAL command against the REAL
//	rpc.Registry the daemon composition root builds, the rendered human
//	output, and the redaction canary over every diagnostic this command
//	can emit.
//
// This file and its harness deliberately import neither "net" nor
// "net/http" so they run in the fast, no-network unit lane (Art.7.2); the
// real socket is proven in
// internal/retrieval/recall/rpc_integration_test.go. The harness DOES
// import internal/rpc, which the cmd-rpc-server-boundary depguard rule
// permits in *_test.go files: driving the real registry in-process is the
// only way to prove the CLI and the daemon speak the same method names
// and the same result shape without a socket.
//
// SPORT: cmd.cascade.cmd.recall (ADD, per T-3 sport_updates).
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRecallResolvesOnTheRealRootCommand is the reachability proof.
//
// It resolves the verb through newRootCmd() — the SAME tree main()
// executes and the golden help fixture is rendered from — rather than
// through a locally constructed command. A subsystem that is built,
// tested and never mounted is this repository's most repeated defect, and
// a test that builds its own tree cannot see it. Deleting the
// mountRecallCmd line from root.go makes this fail.
func TestRecallResolvesOnTheRealRootCommand(t *testing.T) {
	cmd, _, err := newRootCmd().Find([]string{"recall"})
	if err != nil {
		t.Fatalf("recall did not resolve on the real root: %v", err)
	}
	if cmd.Name() != "recall" {
		t.Fatalf("resolved %q, want recall", cmd.Name())
	}
	if cmd.RunE == nil {
		t.Fatal("recall resolved but has no RunE")
	}
	for _, flag := range []string{"corpus", "scope", "k", "cite"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("recall has no --%s flag", flag)
		}
	}
}

func TestRecallSendsItsFlagsAsParams(t *testing.T) {
	h := &recallHarness{result: recall.QueryResult{}}
	if _, _, err := h.run(t, "a query", "--corpus", "handbook", "--corpus", "notes",
		"--scope", "project/cascade", "--k", "5", "--cite"); err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(h.calls) != 1 || h.calls[0].Method != recall.MethodQuery {
		t.Fatalf("calls = %+v, want one recall.query", h.calls)
	}
	params, ok := h.calls[0].Params.(recall.QueryParams)
	if !ok {
		t.Fatalf("params are %T, want recall.QueryParams", h.calls[0].Params)
	}
	if params.Query != "a query" || params.Scope != "project/cascade" || params.K != 5 || !params.Cite {
		t.Errorf("params = %+v", params)
	}
	if strings.Join(params.Corpus, ",") != "handbook,notes" {
		t.Errorf("--corpus did not accumulate: %v", params.Corpus)
	}
}

// TestRecallEndToEndThroughTheRealRegistry runs the REAL command against
// the REAL registry over a seeded index. It is the composition proof in
// both directions: the CLI's method name and params must be the ones the
// handler decodes, and the handler's result must be the shape the view
// renders.
func TestRecallEndToEndThroughTheRealRegistry(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t)}
	stdout, _, err := h.run(t, "reciprocal rank fusion", "--scope", "project/cascade", "--cite")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(stdout, "RANK") || !strings.Contains(stdout, "handbook/fusion.md") {
		t.Fatalf("stdout is not the expected table:\n%s", stdout)
	}
	if !strings.Contains(stdout, "[^1]:") {
		t.Errorf("--cite printed no citation block:\n%s", stdout)
	}
}

// TestRecallEndToEndScopeHolds is hard requirement one, asserted through
// the real command and the real registry: the unauthorized corpus holds
// the strongest match for the query, and nothing of it may appear in the
// results, in the citations, or in any diagnostic.
func TestRecallEndToEndScopeHolds(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t)}
	stdout, stderr, err := h.run(t, "reciprocal rank fusion",
		"--scope", "project/cascade", "--cite", "--json")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(stdout, "handbook/fusion.md") {
		t.Fatalf("the command returned nothing authorized, so the assertion would be vacuous:\n%s", stdout)
	}
	for _, forbidden := range []string{"quokka", "secrets.md", "journal", "c-secret"} {
		if strings.Contains(stdout, forbidden) || strings.Contains(stderr, forbidden) {
			t.Errorf("out-of-scope content reached the terminal (%q):\nstdout %s\nstderr %s",
				forbidden, stdout, stderr)
		}
	}
}

// TestRecallEndToEndRefusalNamesNoContent: asking for a corpus this scope
// may not read is a typed refusal whose text carries none of it.
func TestRecallEndToEndRefusalNamesNoContent(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t)}
	_, _, err := h.run(t, "reciprocal rank fusion", "--scope", "project/cascade", "--corpus", "journal")
	if err == nil {
		t.Fatal("an unreadable corpus must be a refusal")
	}
	if !cascade.HasKind(err, cascade.KindNotFound) {
		t.Errorf("error %v does not carry not-found", err)
	}
	for _, forbidden := range []string{"quokka", "secrets.md", "c-secret"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Errorf("the refusal carried %q: %v", forbidden, err)
		}
	}
}

// TestRecallEmptyMatchIsNotAnError: a query that matched nothing says so
// and exits zero. Every other empty answer in this command is an error
// with a Kind, which is what makes the two distinguishable.
func TestRecallEmptyMatchIsNotAnError(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t)}
	stdout, _, err := h.run(t, "kumquat marmalade", "--scope", "project/cascade")
	if err != nil {
		t.Fatalf("an empty match must exit zero: %v", err)
	}
	if strings.TrimSpace(stdout) != "no results" {
		t.Errorf("stdout = %q, want a plain statement that nothing matched", stdout)
	}
}

func TestRecallEmptyQueryIsRefusedWithItsExitCode(t *testing.T) {
	h := &recallHarness{dispatch: recallTestRegistry(t)}
	_, _, err := h.run(t, "   ", "--scope", "project/cascade")
	if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("err = %v, want invalid-input", err)
	}
}

// TestRecallRequiresExactlyOneQuery: the argument count is validated
// through usageArgs, so a missing query exits invalid-input rather than
// internal.
func TestRecallRequiresExactlyOneQuery(t *testing.T) {
	h := &recallHarness{}
	for _, args := range [][]string{{}, {"one", "two"}} {
		_, _, err := h.run(t, args...)
		if err == nil || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("args %v: err = %v, want invalid-input", args, err)
		}
	}
}

// TestRecallJSONIsTheVersionedEnvelope pins the --json contract: the
// envelope's data is the RPC result's own shape, not a second rendering.
func TestRecallJSONIsTheVersionedEnvelope(t *testing.T) {
	h := &recallHarness{result: recall.QueryResult{
		Query:   "q",
		Results: []recall.Result{{Rank: 1, ChunkID: "c1", Path: "a/b.md", CorpusID: "handbook", Score: 0.5}},
		Legs:    []string{"fts5"},
	}}
	stdout, _, err := h.run(t, "q", "--json")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var envelope struct {
		Data recall.QueryResult `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	if len(envelope.Data.Results) != 1 || envelope.Data.Results[0].Path != "a/b.md" {
		t.Errorf("envelope data = %+v", envelope.Data)
	}
}

// TestRecallDiagnosticsAreScrubbed is the redaction canary: a failure
// naming a machine path or a secret-shaped value must not reach a
// terminal with either in it.
func TestRecallDiagnosticsAreScrubbed(t *testing.T) {
	h := &recallHarness{err: cascade.New(cascade.KindUnavailable,
		"recall: cannot read /Users/someone/.cascade/data/retrieval/catalog.json "+
			"(token sk-abcdefghijklmnopqrstuvwxyz012345)")}
	_, _, err := h.run(t, "q")
	if err == nil {
		t.Fatal("want the injected failure")
	}
	if strings.Contains(err.Error(), "/Users/someone") ||
		strings.Contains(err.Error(), "sk-abcdefghijklmnopqrstuvwxyz012345") {
		t.Errorf("the diagnostic was not scrubbed: %v", err)
	}
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("scrubbing changed the Kind, so it would change the exit code: %v", err)
	}
}

// TestRecallWithheldIsCountedNeverDescribed: the human output may say how
// many rows were withheld and nothing else about them.
func TestRecallWithheldIsCountedNeverDescribed(t *testing.T) {
	h := &recallHarness{result: recall.QueryResult{
		Results:  []recall.Result{{Rank: 1, ChunkID: "c1", Path: "a/b.md"}},
		Withheld: 2,
	}}
	stdout, _, err := h.run(t, "q")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(stdout, "2 result(s) withheld") {
		t.Errorf("stdout does not report the withheld count:\n%s", stdout)
	}
}

// TestRecallSourceFallsBackToTheChunkID: a result with no path is still
// identifiable, and a control character in a path never reaches the
// terminal as one.
func TestRecallSourceRendering(t *testing.T) {
	long := strings.Repeat("a/", 60) + "deep.md"
	h := &recallHarness{result: recall.QueryResult{Results: []recall.Result{
		{Rank: 1, ChunkID: "chunk-1"},
		{Rank: 2, ChunkID: "chunk-2", Path: "a/\x1b[31mred\x07.md"},
		{Rank: 3, ChunkID: "chunk-3", Path: long},
	}}}
	stdout, _, err := h.run(t, "q")
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !strings.Contains(stdout, "chunk-1") {
		t.Errorf("a pathless result was not identified by its chunk id:\n%s", stdout)
	}
	if strings.ContainsAny(stdout, "\x1b\x07") {
		t.Errorf("a control character reached the terminal:\n%q", stdout)
	}
	if strings.Contains(stdout, long) {
		t.Errorf("an over-long path was not truncated:\n%s", stdout)
	}
}
