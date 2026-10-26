package conversation

// Purpose (this file): Resumer's free-text query path. It dispatches
//   V/S-47.T1's recall.what IN-PROCESS through the ResumeQuerier seam with
//   the query and the caller's cwd (NO domains parameter: WhatParams has
//   none), keeps the rows whose Domain is "conversation", and returns each
//   row's Path, the owning thread id, in rank order without repeats.
//   resume.go then resumes those threads exactly as the topic path does
//   (resume_topic.go), so the same source-versus-target rule governs both
//   paths and the block's tier is the join of the contributing threads.
// Inputs: a query string and the session's cwd.
// Outputs: thread ids that exist in the store, or a typed refusal.
// Constraints: CONVERSATION ONLY, filtered client-side. File and memory
//   rows are never composed into a resumed block or joined into its tier:
//   only Domain == "conversation" rows are considered. While
//   recall.what reports its conversation leg unavailable
//   (Errors["conversation"], until P1-E20-W5-S91-T3 restores the leg) the
//   query path answers ErrResumeConversationLegUnavailable (KindUnavailable)
//   even when other domains returned rows. No conversation row is
//   ErrResumeQueryNoResults (KindNotFound). A conversation row whose Path
//   is empty or names no stored thread refuses the whole resume with
//   nothing composed, never read as a default tier.
//   NEVER import internal/retrieval: this file speaks recall.what's wire
//   shape through a package-local mirror, as any JSON-RPC caller would.
// SPORT: internal.conversation.resume/ADDED (P1-E22-W5-S47-T2).

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recallWhatMethod is internal/retrieval.MethodWhat's value, duplicated as
// a literal because importing internal/retrieval would cycle.
const recallWhatMethod = "recall.what"

// recallWhatConversationDomain is internal/retrieval.DomainConversation.
const recallWhatConversationDomain = "conversation"

// recallWhatParamsWire is this path's recall.what request. Cwd is the one
// session signal that changes scope resolution server-side
// (internal/context/scope/resolver.go). There is no domains member: the
// conversation filter is applied to the response (topConversationPaths).
type recallWhatParamsWire struct {
	Query string `json:"query"`
	Cwd   string `json:"cwd,omitempty"`
}

// recallWhatResultWire mirrors the internal/retrieval.RecallWhatResult
// fields this path reads. For a conversation row, Path is the owning
// thread id.
type recallWhatResultWire struct {
	Rank   int    `json:"rank"`
	Domain string `json:"domain"`
	Path   string `json:"path,omitempty"`
}

// recallWhatResponseWire mirrors internal/retrieval.RecallWhatResponse:
// the fused rows plus one Errors entry per configured domain that failed.
type recallWhatResponseWire struct {
	Results []recallWhatResultWire `json:"results"`
	Errors  map[string]string      `json:"errors,omitempty"`
}

// resolveQuery returns the thread ids the query resolves to, best rank
// first. See this file's Constraints for every refusal.
func (r *Resumer) resolveQuery(ctx context.Context, query, cwd string, admits func(string) bool) ([]string, error) {
	if r.querier == nil {
		return nil, ErrResumeQuerierUnavailable
	}
	params, err := json.Marshal(recallWhatParamsWire{Query: query, Cwd: cwd})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "conversation: resume: marshal recall.what params")
	}
	raw, errObj := r.querier.Dispatch(ctx, &rpc.Request{Method: recallWhatMethod, Params: params})
	if errObj != nil {
		return nil, resumeErrorFromRPC(errObj)
	}
	resp, err := decodeRecallWhatResponse(raw)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "conversation: resume: decode recall.what response")
	}
	if _, down := resp.Errors[recallWhatConversationDomain]; down {
		return nil, ErrResumeConversationLegUnavailable
	}
	paths := topConversationPaths(resp)
	if admits != nil {
		// A hit outside the caller's view is dropped as if recall.what had
		// never returned it: it neither names the foreign thread in a
		// refusal nor denies the caller's own matching threads.
		visible := make([]string, 0, len(paths))
		for _, path := range paths {
			if path == "" || admits(path) {
				visible = append(visible, path)
			}
		}
		paths = visible
	}
	if len(paths) == 0 {
		return nil, ErrResumeQueryNoResults
	}
	for _, path := range paths {
		if path == "" {
			return nil, errResumeQueryUnresolvable(path)
		}
		if _, exists, err := r.store.GetThread(ctx, path); err != nil {
			return nil, err
		} else if !exists {
			return nil, errResumeQueryUnresolvable(path)
		}
	}
	return paths, nil
}

// topConversationPaths returns the Path of every row whose Domain is
// "conversation", ordered by Rank (best first, ties in response order), each
// thread once. Every other domain is ignored.
func topConversationPaths(resp recallWhatResponseWire) []string {
	rows := make([]recallWhatResultWire, 0, len(resp.Results))
	for _, res := range resp.Results {
		if res.Domain == recallWhatConversationDomain {
			rows = append(rows, res)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Rank < rows[j].Rank })
	seen := make(map[string]bool, len(rows))
	paths := make([]string, 0, len(rows))
	for _, row := range rows {
		if !seen[row.Path] {
			seen[row.Path] = true
			paths = append(paths, row.Path)
		}
	}
	return paths
}

// decodeRecallWhatResponse round-trips raw (the concrete response value
// Dispatch hands back in-process) through JSON into this file's own
// mirror -- the only way to read it without importing internal/retrieval,
// and structurally what an out-of-process client would decode.
func decodeRecallWhatResponse(raw any) (recallWhatResponseWire, error) {
	var out recallWhatResponseWire
	encoded, err := json.Marshal(raw)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return out, err
	}
	return out, nil
}

// resumeErrorFromRPC converts a recall.what dispatch's *rpc.ErrorObject
// into a typed cascade.Error, keeping its taxonomy Kind via the wire code
// and falling back to KindUnavailable for an unknown code. eo.Message is
// recall.what's own redacted message, the one every recall.what caller
// already sees.
func resumeErrorFromRPC(eo *rpc.ErrorObject) error {
	kind := cascade.KindUnavailable
	if k, ok := cascade.KindFromJSONRPCCode(eo.Code); ok {
		kind = k
	}
	return cascade.Newf(kind, "conversation: resume: recall.what: %s", eo.Message)
}
