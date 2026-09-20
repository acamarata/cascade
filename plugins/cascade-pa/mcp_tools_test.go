package cascadepa

// Purpose (this file): the two assertions that need this package rather
//   than the tools package — the MCP round trip against a REAL recorded
//   harness session (Art.2: MCP is an external protocol), and the fact
//   that a disabled plugin exposes no tools at all.
//
// WHY HERE AND NOT IN tools/. plugins/cascade-pa imports
//   plugins/cascade-pa/tools, so a test in tools/ cannot reach this
//   package's manifest without an import cycle. The ticket's own checks
//   name ./plugins/cascade-pa/..., which covers both.
//
// SPORT: plugins/cascade-pa:mcp-tools (TEST) — P1-E20-W5-S43-T4.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/plugins/cascade-pa/tools"
)

// mcpFrame is as much of an MCP frame as these assertions read.
type mcpFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
}

// toolCallParams is a tools/call request's params, as the client sends it.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      json.RawMessage `json:"_meta"`
}

// toolCallResult is a tools/call response, as the server sends it.
type toolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// readFrames loads one side of the recorded session.
func readFrames(t *testing.T, name string) []mcpFrame {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "mcp_fixtures", name))
	if err != nil {
		t.Fatalf("reading the recorded session: %v", err)
	}
	var out []mcpFrame
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var f mcpFrame
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("decoding a recorded frame %q: %v", line, err)
		}
		out = append(out, f)
	}
	return out
}

// recordingConversations answers the two calls the recorded session made,
// so the replay exercises the real dispatcher rather than a stub.
type recordingConversations struct{ threadID, turnID, at string }

func (r *recordingConversations) Append(_ context.Context, _ tools.AppendRequest) (tools.AppendResult, error) {
	return tools.AppendResult{ThreadID: r.threadID, TurnID: r.turnID, CreatedAt: r.at}, nil
}

func (r *recordingConversations) Thread(_ context.Context, id string) (tools.ThreadDetail, error) {
	return tools.ThreadDetail{ThreadID: id}, nil
}

func (r *recordingConversations) Search(_ context.Context, _ tools.SearchRequest) ([]tools.SearchResult, error) {
	return nil, tools.ErrSearchUnavailable
}

func (r *recordingConversations) Threads(_ context.Context) ([]tools.ThreadSummary, error) {
	return []tools.ThreadSummary{{ID: r.threadID, Title: r.threadID, UpdatedAt: r.at}}, nil
}

// TestCpaMCPRoundTrip replays the recorded harness session's tool calls.
//
// The ARGUMENTS come from the client's own bytes and the RESULT SHAPE from
// the server's own bytes — see testdata/mcp_fixtures/README.md for how the
// session was captured. Ids and timestamps are per-run, so this asserts the
// keys each result carries; TestCpaMCPRoundTripValuesAreConsistent below
// asserts the values that CAN be checked.
func TestCpaMCPRoundTrip(t *testing.T) {
	args, results := recordedCalls(t)
	svc := &recordingConversations{threadID: "thread-replay", turnID: "turn-replay", at: "2026-09-20T14:54:32Z"}
	d := tools.NewDispatcher(svc)
	calls := 0
	for name, raw := range args {
		if !strings.HasPrefix(name, "cascade_cpa_") {
			continue
		}
		calls++
		assertRecordedCallReplays(t, d, name, raw, results[name])
	}
	if calls == 0 {
		t.Fatal("the recorded session contains no cascade_cpa_* tool call; this test asserted nothing")
	}
	t.Logf("replayed %d recorded tool call(s)", calls)
}

// assertRecordedCallReplays runs one recorded call and compares its result's
// KEYS with the recorded one's.
func assertRecordedCallReplays(t *testing.T, d *tools.Dispatcher, name string, args json.RawMessage, recorded string) {
	t.Helper()
	if recorded == "" {
		t.Fatalf("%s: the recorded session has no result for this call", name)
	}
	got, err := d.Dispatch(context.Background(), name, args)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	wantKeys, gotKeys := jsonKeys(t, []byte(recorded)), jsonKeys(t, got)
	if len(wantKeys) != len(gotKeys) {
		t.Fatalf("%s: result keys = %v, the recorded session's were %v", name, gotKeys, wantKeys)
	}
	for k := range wantKeys {
		if _, ok := gotKeys[k]; !ok {
			t.Errorf("%s: result is missing %q, which the recorded session carried", name, k)
		}
	}
}

// jsonKeys returns the top-level keys of a JSON object.
func jsonKeys(t *testing.T, raw []byte) map[string]struct{} {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decoding %q: %v", raw, err)
	}
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// TestCpaMCPRoundTripValuesAreConsistent reads the recorded VALUES, not
// just the recorded keys.
//
// Independent review found that TestCpaMCPRoundTrip compares key sets, so
// emptying the fixture fails it but hand-editing a value does not. These
// two relations hold between frames of a genuine session and would have to
// be forged together to survive an edit: the sensitivity the server
// reported is the fail-closed resolution of the one the client asked for,
// and the thread the send named is the thread the listing then returned.
func TestCpaMCPRoundTripValuesAreConsistent(t *testing.T) {
	args, results := recordedCalls(t)
	var sent struct {
		ThreadID    string `json:"thread_id"`
		Sensitivity string `json:"sensitivity"`
	}
	sendText, ok := results[tools.ToolSend]
	if !ok {
		t.Fatal("the recorded session has no cascade_cpa_send result")
	}
	if err := json.Unmarshal([]byte(sendText), &sent); err != nil {
		t.Fatalf("decoding the recorded send result: %v", err)
	}

	var asked struct {
		Sensitivity string `json:"sensitivity"`
	}
	if err := json.Unmarshal(args[tools.ToolSend], &asked); err != nil {
		t.Fatalf("decoding the recorded send arguments: %v", err)
	}
	if want := tools.ResolveSensitivity(asked.Sensitivity); sent.Sensitivity != want {
		t.Errorf("recorded sensitivity = %q for a request asking %q; fail-closed resolution gives %q",
			sent.Sensitivity, asked.Sensitivity, want)
	}
	if sent.ThreadID == "" {
		t.Fatal("the recorded send named no thread; the server is supposed to mint one")
	}
	historyText, ok := results[tools.ToolHistory]
	if !ok {
		t.Fatal("the recorded session has no cascade_cpa_history result")
	}
	if !strings.Contains(historyText, sent.ThreadID) {
		t.Errorf("the thread the send minted (%s) does not appear in the recorded listing %s",
			sent.ThreadID, historyText)
	}
}

// recordedCalls pairs each recorded tools/call with its result text.
func recordedCalls(t *testing.T) (map[string]json.RawMessage, map[string]string) {
	t.Helper()
	in, out := readFrames(t, "claude-code-2.1.273-in.jsonl"), readFrames(t, "claude-code-2.1.273-out.jsonl")
	byID := map[int]string{}
	for _, f := range out {
		if f.ID == nil || f.Result == nil {
			continue
		}
		var r toolCallResult
		if err := json.Unmarshal(f.Result, &r); err == nil && len(r.Content) > 0 {
			byID[*f.ID] = r.Content[0].Text
		}
	}
	args, results := map[string]json.RawMessage{}, map[string]string{}
	for _, f := range in {
		if f.Method != "tools/call" || f.ID == nil {
			continue
		}
		var p toolCallParams
		if err := json.Unmarshal(f.Params, &p); err != nil {
			t.Fatalf("decoding a recorded tools/call: %v", err)
		}
		args[p.Name] = p.Arguments
		if text, ok := byID[*f.ID]; ok {
			results[p.Name] = text
		}
	}
	return args, results
}

// TestCpaMCPRoundTripUsesARealClientsFrames guards the fixture itself. A
// recording replaced by a hand-written one would make the test above pass
// while proving nothing, which is exactly the failure R-14.246 records.
func TestCpaMCPRoundTripUsesARealClientsFrames(t *testing.T) {
	in := readFrames(t, "claude-code-2.1.273-in.jsonl")
	if len(in) == 0 {
		t.Fatal("the recorded session is empty")
	}
	if in[0].Method != "initialize" {
		t.Errorf("first frame is %q, want initialize — a real client sends it first and only (R-14.246)", in[0].Method)
	}
	var sawToolUseID bool
	for _, f := range in {
		if f.Method == "tools/call" && strings.Contains(string(f.Params), "claudecode/toolUseId") {
			sawToolUseID = true
		}
		if f.JSONRPC != "2.0" {
			t.Errorf("frame %q has jsonrpc %q, want 2.0", f.Method, f.JSONRPC)
		}
	}
	// A _meta field no specification describes and no hand-written fixture
	// would think to include: its presence is what makes this a recording.
	if !sawToolUseID {
		t.Error("no tools/call carries claudecode/toolUseId; this fixture may not be a real capture")
	}
}

// TestCpaToolsPluginAbsent asserts the tools are declared in exactly one
// place, so a disabled plugin exposes none of them.
//
// The MCP surface is generated from this manifest (cmd/cascade/mcp.go walks
// the enabled plugins' Provides.Tools). A tool that DispatchTool could
// service but the manifest does not name is absent from every harness; a
// tool the manifest names is present in all of them. This pins both halves.
func TestCpaToolsPluginAbsent(t *testing.T) {
	m := manifest()
	declared := make(map[string]struct{}, len(m.Provides.Tools))
	for _, tool := range m.Provides.Tools {
		declared[tool.Name] = struct{}{}
		if tool.Description == "" {
			t.Errorf("tool %q has no description; a harness would list it blank", tool.Name)
		}
	}
	for _, name := range tools.Names() {
		if _, ok := declared[name]; !ok {
			t.Errorf("%s is dispatchable but absent from the manifest, so no harness can see it", name)
		}
	}
	if len(declared) != len(tools.Names()) {
		t.Errorf("manifest declares %d tools, the dispatcher serves %d; the two lists must agree",
			len(declared), len(tools.Names()))
	}
	// The manifest is built from tools.Names, so the two agree by
	// construction — which makes the check above weak on its own. This is
	// the one that can still fail: a name added to Names with no
	// Description ships a tool a harness lists blank.
	if got := tools.Description("cascade_cpa_not_a_tool"); got != "" {
		t.Errorf("Description of an unknown tool = %q, want empty", got)
	}
	// Disabled means absent: with no manifest there is nothing to register,
	// and the dispatcher underneath refuses rather than answering from
	// nothing. Both are what "absent when the plugin is disabled" means.
	empty := tools.NewDispatcher(nil)
	for _, name := range tools.Names() {
		if _, err := empty.Dispatch(context.Background(), name, []byte(`{}`)); err == nil {
			t.Errorf("%s answered with no service wired; it must refuse", name)
		}
	}
}
