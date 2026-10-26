package conversation

// Purpose: unit tests for resume_query.go -- the recall.what dispatch
//   shape, the conversation-only, Path-keyed thread selection, and every
//   refusal (no querier, no conversation row, an unresolvable Path, a
//   dispatch error). The leg-down proof against the REAL recall.what
//   service lives in resume_query_recallwhat_test.go.
// SPORT: internal.conversation.resume/ADDED (tests) (P1-E22-W5-S47-T2).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeQuerier is a ResumeQuerier double: it records the last request and
// returns a pre-set result or error.
type fakeQuerier struct {
	gotMethod string
	gotParams json.RawMessage
	result    any
	errObj    *rpc.ErrorObject
}

func (f *fakeQuerier) Dispatch(_ context.Context, req *rpc.Request) (any, *rpc.ErrorObject) {
	f.gotMethod = req.Method
	f.gotParams = req.Params
	if f.errObj != nil {
		return nil, f.errObj
	}
	return f.result, nil
}

// queryResumer builds a Resumer over store whose querier answers resp.
func queryResumer(t *testing.T, store Store, resp recallWhatResponseWire) (*Resumer, *fakeQuerier) {
	t.Helper()
	fq := &fakeQuerier{result: resp}
	r, err := NewResumer(store, echoComposer{}, fq, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	return r, fq
}

// TestResolveQuery_DispatchShape proves the request: method recall.what and
// the caller's query and cwd.
func TestResolveQuery_DispatchShape(t *testing.T) {
	store := newResumeStore(t)
	thread := seedResumeThread(t, store, "keys are in the drawer")
	r, fq := queryResumer(t, store, recallWhatResponseWire{Results: []recallWhatResultWire{
		{Rank: 1, Domain: "conversation", Path: thread},
	}})
	if _, err := r.Resume(context.Background(), ResumeRequest{Query: "where are the keys", Cwd: "/repo/pkg"}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	var p recallWhatParamsWire
	if err := json.Unmarshal(fq.gotParams, &p); err != nil {
		t.Fatalf("unmarshal dispatched params: %v", err)
	}
	if fq.gotMethod != "recall.what" || p.Query != "where are the keys" || p.Cwd != "/repo/pkg" {
		t.Fatalf("dispatched %s %+v, want recall.what with the query and cwd", fq.gotMethod, p)
	}
}

// TestResumeQuerySendsNoDomainsParam: recall.what's WhatParams has no
// domains member, so the request carries exactly query and cwd; the
// conversation filter is applied to the response. Failing input: the old
// request that named domains=["conversation"].
func TestResumeQuerySendsNoDomainsParam(t *testing.T) {
	store := newResumeStore(t)
	thread := seedResumeThread(t, store, "keys are in the drawer")
	r, fq := queryResumer(t, store, recallWhatResponseWire{Results: []recallWhatResultWire{
		{Rank: 1, Domain: "conversation", Path: thread},
	}})
	if _, err := r.Resume(context.Background(), ResumeRequest{Query: "keys", Cwd: "/repo"}); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(fq.gotParams, &members); err != nil {
		t.Fatalf("unmarshal dispatched params: %v", err)
	}
	if len(members) != 2 || members["query"] == nil || members["cwd"] == nil {
		t.Fatalf("dispatched members = %s, want exactly query and cwd", fq.gotParams)
	}
}

// TestResumeQueryIgnoresNonConversationHits: file and memory rows are never
// composed or joined into the block's tier, even when they outrank the
// conversation rows; the conversation rows compose in rank order, each by
// its Path (the owning thread id), and the best-ranked one is the result's
// thread. Failing input: composing the rank-1 file row, or ordering the
// rank-5 conversation row first.
func TestResumeQueryIgnoresNonConversationHits(t *testing.T) {
	store := newResumeStore(t)
	top := seedResumeThread(t, store, "the top thread's turn")
	other := seedResumeThread(t, store, "a lower thread's turn")
	r, _ := queryResumer(t, store, recallWhatResponseWire{Results: []recallWhatResultWire{
		{Rank: 1, Domain: "file", Path: "notes/plan.md"},
		{Rank: 2, Domain: "memory", Path: "project/n1"},
		{Rank: 5, Domain: "conversation", Path: other},
		{Rank: 3, Domain: "conversation", Path: top},
	}})
	res, err := r.Resume(context.Background(), ResumeRequest{Query: "q", IntoTier: provider.SensitivityRestricted})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	first, second := strings.Index(res.Context, "the top thread's turn"), strings.Index(res.Context, "a lower thread's turn")
	if res.ThreadID != top || first < 0 || second < first || strings.Contains(res.Context, "plan.md") {
		t.Fatalf("Resume = %+v, want both conversation threads (top first) and no file or memory row", res)
	}
}

// TestResumeQueryUnresolvableThreadRefused: a conversation row whose Path
// names no thread refuses the whole resume (KindNotFound) with nothing
// composed or substituted, even when another row resolves. Failing input:
// skipping the ghost row and composing the rest.
func TestResumeQueryUnresolvableThreadRefused(t *testing.T) {
	store := newResumeStore(t)
	realPath := seedResumeThread(t, store, "a real thread")
	sub := &recordingSubst{}
	fq := &fakeQuerier{result: recallWhatResponseWire{Results: []recallWhatResultWire{
		{Rank: 1, Domain: "conversation", Path: realPath},
		{Rank: 2, Domain: "conversation", Path: "conversation:ghost"},
	}}}
	r, err := NewResumer(store, echoComposer{}, fq, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	_, err = r.Resume(context.Background(), ResumeRequest{Query: "q", IntoTier: provider.SensitivityRestricted})
	if !cascade.HasKind(err, cascade.KindNotFound) || !strings.Contains(err.Error(), "conversation:ghost") || sub.got != nil {
		t.Fatalf("Resume = %v (substituted %q), want KindNotFound naming the ghost thread and nothing composed", err, sub.got)
	}
}

// TestResumeQueryStrictestContributingTier: the block's source tier is the
// join of every contributing thread's tier, each read from its own marker
// (an absent marker is restricted). An internal thread beside a restricted
// one makes the block restricted: it transits the egress class at
// restricted, is refused into an internal session and admitted into a
// restricted one. Failing input: using the best-ranked thread's tier only.
func TestResumeQueryStrictestContributingTier(t *testing.T) {
	store := newResumeStore(t)
	ctx := context.Background()
	internal := seedResumeThreadAt(t, store, provider.SensitivityInternal, "internal thread")
	unmarked := seedLegacyUnmarkedThread(t, store, "unmarked thread")
	resp := recallWhatResponseWire{Results: []recallWhatResultWire{
		{Rank: 1, Domain: "conversation", Path: internal},
		{Rank: 2, Domain: "conversation", Path: unmarked},
	}}
	sub := &recordingSubst{}
	fq := &fakeQuerier{result: resp}
	r, err := NewResumer(store, echoComposer{}, fq, sub)
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := r.Resume(ctx, ResumeRequest{Query: "q", IntoTier: provider.SensitivityRestricted}); err != nil {
		t.Fatalf("Resume into restricted: %v", err)
	}
	if sub.gotTier != provider.SensitivityRestricted {
		t.Fatalf("block transited egress at %s, want restricted (the join of internal and unmarked)", sub.gotTier)
	}
	_, err = r.Resume(ctx, ResumeRequest{Query: "q", IntoTier: provider.SensitivityInternal})
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), unmarked) {
		t.Fatalf("Resume into internal = %v, want KindPolicyDenied naming the unmarked (restricted) thread", err)
	}
}

// TestResolveQuery_Refusals covers every typed refusal before composition.
func TestResolveQuery_Refusals(t *testing.T) {
	store := newResumeStore(t)
	cases := map[string]struct {
		resp recallWhatResponseWire
		kind cascade.Kind
	}{
		"no results":        {recallWhatResponseWire{}, cascade.KindNotFound},
		"file rows only":    {recallWhatResponseWire{Results: []recallWhatResultWire{{Rank: 1, Domain: "file", Path: "a.md"}}}, cascade.KindNotFound},
		"empty path":        {recallWhatResponseWire{Results: []recallWhatResultWire{{Rank: 1, Domain: "conversation"}}}, cascade.KindNotFound},
		"unresolvable path": {recallWhatResponseWire{Results: []recallWhatResultWire{{Rank: 1, Domain: "conversation", Path: "conversation:ghost"}}}, cascade.KindNotFound},
		"conversation leg down": {recallWhatResponseWire{
			Results: []recallWhatResultWire{{Rank: 1, Domain: "file", Path: "a.md"}},
			Errors:  map[string]string{"conversation": "unavailable"},
		}, cascade.KindUnavailable},
	}
	for name, c := range cases {
		r, _ := queryResumer(t, store, c.resp)
		_, err := r.Resume(context.Background(), ResumeRequest{Query: "q", IntoTier: provider.SensitivityLocalOnly})
		if !cascade.HasKind(err, c.kind) {
			t.Errorf("%s: err = %v, want %v", name, err, c.kind)
		}
	}
}

// TestResolveQuery_ResolvedThreadObeysTierRule: the query path applies the
// same source-versus-target rule as the topic path -- a local-only thread
// the query resolves is refused into a public session.
func TestResolveQuery_ResolvedThreadObeysTierRule(t *testing.T) {
	store := newResumeStore(t)
	thread := seedResumeThreadAt(t, store, provider.SensitivityLocalOnly, "local notes")
	r, _ := queryResumer(t, store, recallWhatResponseWire{Results: []recallWhatResultWire{
		{Rank: 1, Domain: "conversation", Path: thread},
	}})
	_, err := r.Resume(context.Background(), ResumeRequest{Query: "q", IntoTier: provider.SensitivityPublic})
	if !cascade.HasKind(err, cascade.KindPolicyDenied) || !strings.Contains(err.Error(), "local-only") {
		t.Fatalf("Resume = %v, want KindPolicyDenied naming local-only", err)
	}
}

// TestResolveQuery_NoQuerierConfigured: a build that never wired
// recall.what refuses the query path closed, never a nil call.
func TestResolveQuery_NoQuerierConfigured(t *testing.T) {
	r, err := NewResumer(newResumeStore(t), echoComposer{}, nil, &recordingSubst{})
	if err != nil {
		t.Fatalf("construct: %v", err)
	}
	if _, err := r.Resume(context.Background(), ResumeRequest{Query: "q"}); err != ErrResumeQuerierUnavailable {
		t.Fatalf("Resume(query, no querier) = %v, want ErrResumeQuerierUnavailable", err)
	}
}

// TestResolveQuery_DispatchErrorKinds: a recall.what refusal keeps its
// Kind over the wire; an unrecognised code falls back to KindUnavailable.
func TestResolveQuery_DispatchErrorKinds(t *testing.T) {
	cases := map[int]cascade.Kind{
		cascade.KindPermissionDenied.JSONRPCCode(): cascade.KindPermissionDenied,
		-32700: cascade.KindUnavailable,
	}
	for code, want := range cases {
		fq := &fakeQuerier{errObj: &rpc.ErrorObject{Code: code, Message: "recall.what: refused"}}
		r, err := NewResumer(newResumeStore(t), echoComposer{}, fq, &recordingSubst{})
		if err != nil {
			t.Fatalf("construct: %v", err)
		}
		if _, err := r.Resume(context.Background(), ResumeRequest{Query: "q"}); !cascade.HasKind(err, want) {
			t.Errorf("code %d: err = %v, want %v", code, err, want)
		}
	}
}

// TestDecodeRecallWhatResponse_GenericMap proves the decode reads a plain
// map shape too -- what an out-of-process JSON-RPC client would decode.
func TestDecodeRecallWhatResponse_GenericMap(t *testing.T) {
	raw := map[string]any{
		"results": []map[string]any{{"rank": 1, "domain": "conversation", "path": "thread-x"}},
		"errors":  map[string]string{"memory": "down"},
	}
	resp, err := decodeRecallWhatResponse(raw)
	if err != nil {
		t.Fatalf("decodeRecallWhatResponse: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Path != "thread-x" || resp.Errors["memory"] != "down" {
		t.Fatalf("decodeRecallWhatResponse = %+v", resp)
	}
}
