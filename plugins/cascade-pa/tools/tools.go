// Package tools implements cascade-pa's three agent-callable MCP tools:
// cascade_cpa_send, cascade_cpa_history and cascade_cpa_search.
//
// WHAT THE TOOLS ARE FOR. A connected harness session (any MCP client)
// reaches the operator's conversation store through these and nothing
// else: send a turn, read a thread or the thread list, search content.
// They are the same three questions `cascade chat` asks, answered through
// the same daemon methods, so an agent and a person see one conversation
// rather than two.
//
// THE SERVICE IS INJECTED. This package defines the Conversations seam and
// nothing that satisfies it: cascade-pa may not import internal/**
// (Art.10.2, R-14.69), and the only implementation lives at the
// composition root, over the daemon's chat.* JSON-RPC methods. A tool
// invoked before the host injects one REFUSES, naming what is missing —
// it does not answer from nothing.
//
// SENSITIVITY FAILS CLOSED. cascade_cpa_send takes an optional
// sensitivity; anything unset, unknown or misspelled resolves to
// restricted (06 §5.16). The one direction that cannot leak.
//
// SPORT: plugins/cascade-pa:mcp-tools (ADD) — P1-E20-W5-S43-T4.
package tools

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The three registered tool names (R-16.20's compact MCP profile).
const (
	// ToolSend appends one turn, starting a thread when none is named.
	ToolSend = "cascade_cpa_send"
	// ToolHistory reads a thread's turns, or the thread list.
	ToolHistory = "cascade_cpa_history"
	// ToolSearch finds turns whose content matches a query.
	ToolSearch = "cascade_cpa_search"
)

// Names returns the three tool names in registration order.
func Names() []string { return []string{ToolSend, ToolHistory, ToolSearch} }

// Description returns the one line a harness lists beside a tool name, or
// "" for a name this package does not serve.
//
// It lives here, beside Names and the dispatcher's own switch, so the
// plugin manifest is BUILT from this package rather than repeating its
// three constants. The list a harness sees and the list Dispatch routes
// then cannot drift apart — which they could, silently, while a test that
// compared two hand-written lists went on passing.
func Description(name string) string {
	switch name {
	case ToolSend:
		return "Record one conversation turn, starting a thread when none is named."
	case ToolHistory:
		return "Read a thread's turns, or list the threads when none is named."
	case ToolSearch:
		return "Find conversation turns whose content matches a query."
	default:
		return ""
	}
}

// Conversations is the daemon-side conversation service these tools call.
//
// It mirrors the three chat.* JSON-RPC methods and adds nothing: search is
// built on top of these reads (see cpa_search.go for why it is a scan at
// this point in the plan), so the seam stays the size of the surface it
// crosses.
type Conversations interface {
	// Append records one turn. An empty threadID starts a thread, and the
	// result names the one it started — minting is the SERVER's job
	// (R-14.285), so an implementation must return what the daemon said
	// rather than an id of its own.
	Append(ctx context.Context, req AppendRequest) (AppendResult, error)
	// Thread reads one thread's turns, oldest first.
	Thread(ctx context.Context, threadID string) (ThreadDetail, error)
	// Threads lists every thread.
	Threads(ctx context.Context) ([]ThreadSummary, error)
}

// AppendRequest is one turn to record.
type AppendRequest struct {
	// ThreadID continues a thread, or is empty to start one.
	ThreadID string
	// Role is who spoke: "user", "assistant", "system".
	Role string
	// Content is the turn's text.
	Content string
}

// AppendResult is what the daemon recorded.
type AppendResult struct {
	ThreadID  string
	TurnID    string
	CreatedAt string
}

// ThreadDetail is one thread's turns.
type ThreadDetail struct {
	ThreadID string
	Turns    []TurnRecord
}

// TurnRecord is one recorded turn.
type TurnRecord struct {
	TurnID    string
	Role      string
	Content   string
	CreatedAt string
	Seq       int64
}

// ThreadSummary is one row of the thread listing.
type ThreadSummary struct {
	ID        string
	Title     string
	UpdatedAt string
}

// ErrNoService is every tool's refusal when the host injected no
// conversation service.
//
// A refusal rather than an empty answer: "this process cannot reach the
// conversation store" and "you have no conversations" are different facts,
// and an agent handed the second when the first is true will tell the
// operator their history is gone.
var ErrNoService = cascade.New(cascade.KindUnavailable,
	"cascade-pa: no conversation service is wired into this process, so no tool can reach the "+
		"conversation store; the daemon composition root injects one at startup")

// Dispatcher routes a tool call to its handler.
type Dispatcher struct {
	svc Conversations
}

// NewDispatcher builds a dispatcher over svc. A nil svc is allowed and
// every call then returns ErrNoService — construction must not fail at
// plugin-registration time, which runs in init() before any host has had
// a chance to inject anything.
func NewDispatcher(svc Conversations) *Dispatcher { return &Dispatcher{svc: svc} }

// Dispatch invokes the named tool with raw JSON input and returns raw
// JSON output.
func (d *Dispatcher) Dispatch(ctx context.Context, name string, input []byte) ([]byte, error) {
	if d == nil || d.svc == nil {
		return nil, ErrNoService
	}
	switch name {
	case ToolSend:
		return d.send(ctx, input)
	case ToolHistory:
		return d.history(ctx, input)
	case ToolSearch:
		return d.search(ctx, input)
	default:
		return nil, cascade.Newf(cascade.KindNotFound, "cascade-pa: unknown tool %q", name)
	}
}
