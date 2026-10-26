package conversation

// Purpose (this file): chat.resume's JSON-RPC wire boundary --
//   resumeParams/resumeResult and handleResume, which decode the wire
//   request, resolve the destination session's stored tier, call the wired
//   Resumer and encode its result. Split out of adapter.go under Art.10.3's
//   300-line cap, matching adapter_search.go's precedent for chat.search.
// Inputs: raw JSON params: REQUIRED "session_thread_id" (an existing
//   thread; absent or empty is refused before any store read), exactly one
//   of "topic_id" | "query", optional "budget" and "cwd".
// Outputs: resumeResult {thread_id, leading_context, tokens, trimmed[]},
//   or a taxonomy error (adapter_errors.go's identity mapping; no second
//   mapping table).
// Constraints: the TARGET tier is the destination thread's STORED marker,
//   never a caller claim; a session_thread_id naming no thread is refused
//   (KindNotFound) rather than read as the restricted default, because a
//   new session gets its thread from chat.create_thread first. IDEMPOTENT:
//   chat.resume performs no store write, so a retried or repeated call
//   returns the same block and never injects a second one.
// SPORT: internal.conversation.adapter/CHANGED (P1-E22-W5-S47-T2).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// chat.resume is owner-view: the owner resumes between any threads, a
// principal only between threads attributed to its own scope.
var _ = rpc.DeclareAccess(MethodResume, rpc.AccessOwnerView)

// ResumeHandler is the seam RegisterHandlers binds chat.resume onto.
// *Resumer satisfies it with no adapter; declared here so adapter.go
// depends on a narrow interface, matching JournalStore/ScrubPipeline/
// TopicObserver.
type ResumeHandler interface {
	Resume(ctx context.Context, req ResumeRequest) (ResumeResult, error)
}

// SetResume wires h into handleResume. Nil (the default after NewAdapter)
// means "no resume service composed": handleResume then refuses with
// ErrResumeServiceUnavailable (KindUnavailable).
func (a *Adapter) SetResume(h ResumeHandler) { a.resume = h }

// resumeParams is chat.resume's wire request shape.
type resumeParams struct {
	TopicID string `json:"topic_id,omitempty"`
	Query   string `json:"query,omitempty"`
	Budget  int    `json:"budget,omitempty"`
	// SessionThreadID names the existing thread this resume's block is
	// delivered INTO; its stored marker is the admission target tier.
	SessionThreadID string `json:"session_thread_id,omitempty"`
	// Cwd is forwarded to recall.what's scope resolution on the query path.
	Cwd string `json:"cwd,omitempty"`
}

// resumeResult is chat.resume's wire response shape. Trimmed holds one
// per-domain trim report line per budget trim; it is [] (never null) when
// nothing was trimmed.
type resumeResult struct {
	ThreadID       string   `json:"thread_id"`
	LeadingContext string   `json:"leading_context"`
	Tokens         int      `json:"tokens"`
	Trimmed        []string `json:"trimmed"`
}

// ErrResumeServiceUnavailable reports that chat.resume was dispatched
// before SetResume ran: a deliberate, typed refusal, never fabricated
// content.
var ErrResumeServiceUnavailable = cascade.New(cascade.KindUnavailable,
	"conversation: chat.resume: no resume service is configured on this adapter")

// handleResume is chat.resume.
func (a *Adapter) handleResume(ctx context.Context, raw json.RawMessage) (any, error) {
	if a.resume == nil {
		return nil, ErrResumeServiceUnavailable
	}
	p, err := decodeResumeParams(raw)
	if err != nil {
		return nil, err
	}
	if p.SessionThreadID == "" {
		return nil, ErrResumeMissingSessionThreadID // before any store read
	}
	admits, err := CallerThreadView(ctx, a.store, a.clock, "") // scope_attr.go: owner sees all, a principal its own scope
	if err != nil {
		return nil, err
	}
	if !admits(p.SessionThreadID) {
		return nil, errScopeDenied
	}
	intoTier, err := a.resolveIntoTier(ctx, p.SessionThreadID)
	if err != nil {
		return nil, err
	}
	// A field literal, not a type conversion: the wire carries a thread
	// id, the request carries the tier resolved from it.
	res, err := a.resume.Resume(ctx, ResumeRequest{
		TopicID: p.TopicID, Query: p.Query, Budget: p.Budget, IntoTier: intoTier, Cwd: p.Cwd, Admits: admits,
	})
	if err != nil {
		return nil, err
	}
	trimmed := res.TrimReport
	if trimmed == nil {
		trimmed = []string{}
	}
	return resumeResult{ThreadID: res.ThreadID, LeadingContext: res.Context, Tokens: res.TokensUsed, Trimmed: trimmed}, nil
}

// resolveIntoTier returns sessionThreadID's stored §5.16 tier, refusing an
// id that names no thread (errResumeSessionThreadNotFound).
func (a *Adapter) resolveIntoTier(ctx context.Context, sessionThreadID string) (provider.SensitivityTier, error) {
	_, ok, err := a.store.GetThread(ctx, sessionThreadID)
	if err != nil {
		return provider.SensitivityRestricted, err
	}
	if !ok {
		return provider.SensitivityRestricted, errResumeSessionThreadNotFound(sessionThreadID)
	}
	return a.store.ThreadPrivacy(ctx, sessionThreadID)
}

// decodeResumeParams decodes raw into resumeParams, DisallowUnknownFields
// matching this adapter's other handlers.
func decodeResumeParams(raw json.RawMessage) (resumeParams, error) {
	var p resumeParams
	if len(raw) == 0 {
		return p, ErrResumeInvalidRequest
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, ErrResumeInvalidRequest
	}
	return p, nil
}
