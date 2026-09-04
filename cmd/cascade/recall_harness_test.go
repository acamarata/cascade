// Purpose: `cascade recall`'s test harness — the injected call seam, the
// in-process route through the REAL rpc.Registry, and the seeded index
// the end-to-end cases query. Split from recall_test.go under the
// 300-line file cap.
//
// WHAT IS REAL AND WHAT IS NOT. Real: the cobra command, internal/rpc's
// Registry and Dispatch, internal/retrieval/recall's service, catalog,
// handler and JSON shapes, and internal/retrieval's corpus model, scope
// filter, fusion and citations. ONE double: recallTestLeg below, which
// stands in for the FTS5 leg because F/S-10.T2 has not landed.
//
// Constraints: Art.7 — files under t.TempDir(), no network in this lane.
// SPORT: cmd.cascade.cmd.recall (ADD, per T-3 sport_updates).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recallHarness runs the command with an injected call seam.
type recallHarness struct {
	calls  []recordedCall
	result any
	err    error
	// dispatch, when set, answers the call through a real rpc.Registry
	// instead of returning a canned result.
	dispatch *rpc.Registry
}

func (h *recallHarness) deps(t *testing.T) recallDeps {
	t.Helper()
	root := t.TempDir()
	return recallDeps{
		Paths:   fakeMemoryPaths{root: root},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return nil },
		Call: func(ctx context.Context, _, method string, params, out any) error {
			h.calls = append(h.calls, recordedCall{Method: method, Params: params})
			if h.dispatch != nil {
				return h.dispatchCall(ctx, method, params, out)
			}
			if h.err != nil {
				return h.err
			}
			if h.result == nil {
				return nil
			}
			return reencode(h.result, out)
		},
	}
}

// dispatchCall routes one call through the real registry, marshalling the
// params and unmarshalling the result exactly as the SDK client does over
// a socket. This is what makes the test a wiring proof rather than a
// rendering proof: a method name the daemon does not register, or a
// result shape it does not produce, fails here.
func (h *recallHarness) dispatchCall(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	result, errObj := h.dispatch.Dispatch(ctx, &rpc.Request{
		JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Method: method, Params: raw,
	})
	if errObj != nil {
		kind, ok := cascade.KindFromJSONRPCCode(errObj.Code)
		if !ok {
			kind = cascade.KindInternal
		}
		return cascade.New(kind, errObj.Message)
	}
	return reencode(result, out)
}

// reencode marshals v and decodes it into out, the round trip a real
// response makes through the socket.
func reencode(v, out any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// run executes the command and returns its stdout, stderr and error.
func (h *recallHarness) run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newRecallCmd(h.deps(t))
	// The global flags recallWriter reads live on the root in production;
	// declare the same four here so the command under test sees exactly
	// the flag set it sees when mounted.
	flags := cmd.Flags()
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

// recallHandbook is the corpus the querying session is a member of.
var recallHandbook = corpus.Corpus{
	ID: "handbook", ScopeRef: "project/cascade",
	Privacy: corpus.PrivacyProject, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// recallJournal belongs to a scope the querying session is not in and is
// personal-tier besides. Nothing from it may reach the terminal.
var recallJournal = corpus.Corpus{
	ID: "journal", ScopeRef: "user/journal",
	Privacy: corpus.PrivacyPersonal, Visibility: corpus.VisibilityScopeLocal,
	Trust: corpus.TrustTrusted,
}

// recallFixtures is the indexed content. journal/secrets.md is
// deliberately the STRONGEST match for the query the end-to-end cases
// ask, so a scope assertion over the real command cannot pass by
// accident.
var recallFixtures = map[string]struct {
	corpusID string
	path     string
	body     string
}{
	"c-fusion": {"handbook", "handbook/fusion.md",
		"Reciprocal rank fusion merges the ranked lists each leg returns."},
	"c-recall": {"handbook", "handbook/recall.md",
		"A reciprocal rank fusion query returns cited chunks."},
	"c-secret": {"journal", "journal/secrets.md",
		"quokka reciprocal rank fusion reciprocal rank fusion quokka"},
}

// recallTestLeg is the ONE double: a term-counting stand-in for the FTS5
// leg (F/S-10.T2), reading only the chunks the scope filter resolves.
type recallTestLeg struct{}

func (recallTestLeg) Query(
	_ context.Context, f *fusion.ScopeFilter, text string, topK int,
) (rrf.RankedList, bool, error) {
	terms := strings.Fields(strings.ToLower(text))
	ids := make([]string, 0, len(recallFixtures))
	for id := range recallFixtures {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type scored struct {
		cand  rrf.Candidate
		score int
	}
	var hits []scored
	for _, id := range ids {
		record, ok := f.Resolve(id)
		if !ok {
			continue
		}
		body := strings.ToLower(recallFixtures[id].body)
		total := 0
		for _, term := range terms {
			total += strings.Count(body, term)
		}
		if total == 0 {
			continue
		}
		hits = append(hits, scored{score: total, cand: rrf.Candidate{
			ChunkID: id, Path: recallFixtures[id].path,
			CorpusID: record.CorpusID, Trust: record.Trust,
		}})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].cand.ChunkID < hits[j].cand.ChunkID
	})
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := rrf.RankedList{Strategy: rrf.StrategyFTS, Weight: rrf.NeutralWeight}
	for _, h := range hits {
		out.Hits = append(out.Hits, h.cand)
	}
	return out, true, nil
}

// recallTestRegistry writes a real catalog document under t.TempDir() and
// registers a real recall handler, over a real service, on a real
// registry — the same three constructor calls the daemon composition root
// makes in registerRecallHandler.
func recallTestRegistry(t *testing.T) *rpc.Registry {
	t.Helper()
	doc := recall.CatalogDoc{
		Version: recall.CatalogVersion,
		Corpora: []corpus.Corpus{recallHandbook, recallJournal},
	}
	ids := make([]string, 0, len(recallFixtures))
	for id := range recallFixtures {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		c := recallHandbook
		if recallFixtures[id].corpusID == recallJournal.ID {
			c = recallJournal
		}
		doc.Records = append(doc.Records, corpus.Record{
			ID: id, CorpusID: c.ID, ScopeRef: c.ScopeRef,
			Privacy: c.Privacy, Visibility: c.Visibility, Trust: c.Trust,
		})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal catalog: %v", err)
	}
	path := filepath.Join(t.TempDir(), recall.CatalogFileName)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	svc, err := recall.NewService(recall.NewFileCatalog(path), rrf.Params{}, recallTestLeg{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	registry := rpc.NewRegistry()
	recall.NewHandler(svc).Register(registry)
	return registry
}
