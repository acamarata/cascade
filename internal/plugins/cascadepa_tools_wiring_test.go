// Purpose: success- and failure-path coverage for
//
//	cascadepa_tools_wiring.go, the composition root that makes
//	cascade-pa's three MCP tools reachable. Driven through the rpcDoer
//	seam rather than a real socket, for the reason
//	cascadepa_rpc_success_test.go states: internal/plugins is not on the
//	egress ruling's allowed-"net"-importer list, and Art.7.2 bars
//	"net"/"net/http" from every untagged _test.go file.
//
// SPORT: internal/plugins:cascadepa-tools-wiring (TEST) — P1-E20-W5-S43-T4.
package plugins

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/conversation"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// methodRPCDoer routes by method, because this adapter makes more than one
// call per operation and each decodes into a different result type.
type methodRPCDoer struct {
	appendRes  appendTurnResult
	appendErr  error
	threadRes  getThreadResultWire
	threadErr  error
	listRes    listThreadsResultWire
	listErr    error
	searchRes  searchResultSetWire
	searchErr  error
	calls      []string
	threadSeen []string
}

func (m *methodRPCDoer) Do(_ context.Context, method string, params, out any) error {
	m.calls = append(m.calls, method)
	switch method {
	case conversation.MethodAppendTurn:
		if m.appendErr != nil {
			return m.appendErr
		}
		*out.(*appendTurnResult) = m.appendRes
	case conversation.MethodGetThread:
		m.threadSeen = append(m.threadSeen, params.(getThreadParamsWire).ThreadID)
		if m.threadErr != nil {
			return m.threadErr
		}
		*out.(*getThreadResultWire) = m.threadRes
	case conversation.MethodSearch:
		if m.searchErr != nil {
			return m.searchErr
		}
		*out.(*searchResultSetWire) = m.searchRes
	case conversation.MethodListThreads:
		if m.listErr != nil {
			return m.listErr
		}
		*out.(*listThreadsResultWire) = m.listRes
	default:
		return cascade.Newf(cascade.KindNotFound, "unexpected method %q", method)
	}
	return nil
}

// toolsAdapter builds the adapter over a fake transport.
func toolsAdapter(doer rpcDoer) *cascadePAConversations {
	c := newCascadePAConversations(nil, 0, nil)
	c.doer = doer
	return c
}

// turnWire builds one recorded turn with its segments.
func turnWire(id string, seq, at int64, role conversation.Role, contents ...string) turnWithSegsWire {
	segs := make([]conversation.Segment, 0, len(contents))
	for i, c := range contents {
		segs = append(segs, conversation.Segment{TurnID: id, Seq: int64(i), Kind: conversation.SegmentText, Content: c})
	}
	return turnWithSegsWire{
		Turn:     conversation.Turn{ID: id, Seq: seq, Role: role, CreatedAt: at},
		Segments: segs,
	}
}

func TestToolsWiringAppendReadsTheTimestampBack(t *testing.T) {
	// chat.append_turn's reply is {thread_id, turn_id, seq} and carries no
	// timestamp, while cascade_cpa_send's schema requires created_at. The
	// adapter reads the stored turn rather than inventing one.
	doer := &methodRPCDoer{
		appendRes: appendTurnResult{ThreadID: "t1", TurnID: "u2"},
		threadRes: getThreadResultWire{
			Thread: conversation.Thread{ID: "t1", Name: "notes", CreatedAt: 1758000000},
			Turns: []turnWithSegsWire{
				turnWire("u1", 1, 1758000000, conversation.RoleUser, "first"),
				turnWire("u2", 2, 1758000060, conversation.RoleUser, "second"),
			},
		},
	}
	res, err := toolsAdapter(doer).Append(context.Background(), tools.AppendRequest{Role: "user", Content: "second"})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if res.ThreadID != "t1" || res.TurnID != "u2" {
		t.Fatalf("Append = %+v, want thread t1 turn u2", res)
	}
	if res.CreatedAt != "2025-09-16T05:21:00Z" {
		t.Errorf("created_at = %q, want the stored turn's timestamp in RFC3339", res.CreatedAt)
	}
	if len(doer.calls) != 2 {
		t.Errorf("calls = %v, want the append followed by one read-back", doer.calls)
	}
}

func TestToolsWiringAppendSurvivesAnUnreadableThread(t *testing.T) {
	// The turn IS recorded; only its timestamp is unavailable. Failing a
	// write that succeeded would be worse than an empty created_at.
	doer := &methodRPCDoer{
		appendRes: appendTurnResult{ThreadID: "t1", TurnID: "u1"},
		threadErr: cascade.New(cascade.KindUnavailable, "gone"),
	}
	res, err := toolsAdapter(doer).Append(context.Background(), tools.AppendRequest{Role: "user", Content: "x"})
	if err != nil {
		t.Fatalf("Append: %v, want the write to be reported as the success it was", err)
	}
	if res.TurnID != "u1" || res.CreatedAt != "" {
		t.Errorf("Append = %+v, want the turn with an empty created_at", res)
	}
}

func TestToolsWiringAppendPropagatesTheTransportError(t *testing.T) {
	doer := &methodRPCDoer{appendErr: cascade.New(cascade.KindUnavailable, "daemon not running")}
	_, err := toolsAdapter(doer).Append(context.Background(), tools.AppendRequest{Role: "user", Content: "x"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Append err = %v, want KindUnavailable preserved", err)
	}
}

func TestToolsWiringThreadFlattensSegments(t *testing.T) {
	// Every segment contributes its text. A code or tool_result segment
	// dropped here would make a turn searchable only by the prose around
	// it, and cascade_cpa_search would report no match on content the
	// operator can plainly see.
	doer := &methodRPCDoer{threadRes: getThreadResultWire{
		Thread: conversation.Thread{ID: "t1"},
		Turns: []turnWithSegsWire{
			turnWire("u1", 1, 1758000000, conversation.RoleAssistant, "prose", "", "func main() {}"),
		},
	}}
	detail, err := toolsAdapter(doer).Thread(context.Background(), "t1")
	if err != nil {
		t.Fatalf("Thread: %v", err)
	}
	if len(detail.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(detail.Turns))
	}
	got := detail.Turns[0]
	if got.Content != "prose\n\nfunc main() {}" {
		t.Errorf("content = %q, want both non-empty segments joined", got.Content)
	}
	if got.Role != "assistant" || got.Seq != 1 || got.CreatedAt != "2025-09-16T05:20:00Z" {
		t.Errorf("turn = %+v, want role/seq/created_at carried through", got)
	}
}

func TestToolsWiringThreadsDerivesUpdatedAt(t *testing.T) {
	// chat.list_threads carries no modification time. A thread's only
	// mutation is an appended turn, so the newest turn's created_at IS its
	// updated_at — derivation, not a substitute.
	doer := &methodRPCDoer{
		listRes: listThreadsResultWire{Threads: []conversation.Thread{
			{ID: "t1", Name: "notes", CreatedAt: 1758000000},
		}},
		threadRes: getThreadResultWire{Thread: conversation.Thread{ID: "t1"}, Turns: []turnWithSegsWire{
			turnWire("u1", 1, 1758000000, conversation.RoleUser, "a"),
			turnWire("u2", 2, 1758003600, conversation.RoleUser, "b"),
		}},
	}
	got, err := toolsAdapter(doer).Threads(context.Background())
	if err != nil {
		t.Fatalf("Threads: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("threads = %d, want 1", len(got))
	}
	if got[0].ID != "t1" || got[0].Title != "notes" {
		t.Errorf("thread = %+v, want id t1 title notes", got[0])
	}
	if got[0].UpdatedAt != "2025-09-16T06:20:00Z" {
		t.Errorf("updated_at = %q, want the NEWEST turn's timestamp", got[0].UpdatedAt)
	}
}

func TestToolsWiringThreadsFallsBackToTheThreadsOwnTime(t *testing.T) {
	// An empty thread has no newest turn. Its own creation time is the
	// last moment it changed, so that is the honest answer.
	doer := &methodRPCDoer{
		listRes:   listThreadsResultWire{Threads: []conversation.Thread{{ID: "t1", CreatedAt: 1758000000}}},
		threadErr: cascade.New(cascade.KindNotFound, "gone"),
	}
	got, err := toolsAdapter(doer).Threads(context.Background())
	if err != nil {
		t.Fatalf("Threads: %v", err)
	}
	if got[0].UpdatedAt != "2025-09-16T05:20:00Z" {
		t.Errorf("updated_at = %q, want the thread's own created_at", got[0].UpdatedAt)
	}
}

func TestToolsWiringThreadsPropagatesTheTransportError(t *testing.T) {
	doer := &methodRPCDoer{listErr: cascade.New(cascade.KindUnavailable, "daemon not running")}
	if _, err := toolsAdapter(doer).Threads(context.Background()); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Threads err = %v, want KindUnavailable preserved", err)
	}
}

func TestToolsWiringReportsAPathResolutionFailure(t *testing.T) {
	// Per-request, never at import time: the real environment can always
	// fail to resolve a home directory.
	boom := errors.New("no home directory")
	c := newCascadePAConversations(nil, 0, func() (runtime.PathProvider, error) { return nil, boom })
	_, err := c.Threads(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Threads err = %v, want KindUnavailable", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("Threads err = %v, want the resolver's own error wrapped", err)
	}
}

func TestToolsWiringRendersAnAbsentTimestampAsEmpty(t *testing.T) {
	// Not 1970. A zero timestamp is a fact nobody recorded, and rendering
	// it as an epoch date would put a plausible lie in an agent's hands.
	if got := rfc3339(0); got != "" {
		t.Errorf("rfc3339(0) = %q, want empty", got)
	}
	if got := rfc3339(1758000000); got != "2025-09-16T05:20:00Z" {
		t.Errorf("rfc3339(1758000000) = %q", got)
	}
}

func TestToolsWiringInitInjectedTheService(t *testing.T) {
	// The whole point of this file. Before it existed nothing anywhere
	// called SetConversations, so every tool in a shipped binary returned
	// ErrNoService while the manifest advertised all three (R-14.283).
	// This asserts the package's init() actually wired one: the dispatch
	// below reaches a real adapter and fails at the TRANSPORT (no daemon
	// in a unit test), never with the no-service refusal.
	_, err := dispatchToolForTest(t)
	if err == nil {
		t.Fatal("dispatch reached a daemon from a unit test; that cannot be right")
	}
	// NOT errors.Is: (*cascade.Error).Is compares KIND ONLY, so
	// errors.Is(anyUnavailableError, tools.ErrNoService) is true and this
	// assertion would pass against the very thing it exists to rule out.
	// The refusal is identified by what it SAYS instead.
	if strings.Contains(err.Error(), "no conversation service is wired") {
		t.Fatalf("cascade_cpa_send still refuses with ErrNoService; init() wired nothing (err = %v)", err)
	}
	t.Logf("reached the transport, as a unit test must: %v", err)
}

// dispatchToolForTest calls one tool through the registered builtin's own
// handlers, exactly as the MCP server reaches it.
func dispatchToolForTest(t *testing.T) ([]byte, error) {
	t.Helper()
	// A SHORT home: t.TempDir() embeds the test name, and the daemon
	// socket beneath it overruns darwin's 104-byte sun_path limit, which
	// would mask the transport error this test reads.
	home, err := os.MkdirTemp("", "cpa")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("CASCADE_HOME", home)
	for _, reg := range plugin.Builtins() {
		if reg.Manifest.ID != "cascade-pa" {
			continue
		}
		return reg.Handlers.DispatchTool(context.Background(), tools.ToolSend, []byte(`{"content":"hi"}`))
	}
	t.Fatal("cascade-pa is not in the builtin registry")
	return nil, nil
}
