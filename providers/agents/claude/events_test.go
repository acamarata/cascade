//go:build !windows

package claude

// Purpose: where per-tool events come from (R-14.327 item 4): only
//   authenticated callback frames become tool rows; the JSON result object
//   yields the done row, usage and cost and never a tool event, and
//   tool-shaped bytes on stderr or in the result are never parsed into one.
// Constraints: SCOPE DEVIATION (declared): not in files_scope.add; split
//   out because collect_test.go and callback_test.go sit near the cap.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// journalRows decodes a CollectResult.Output into its closed rows.
func journalRows(t *testing.T, out string) []JournalRow {
	t.Helper()
	var rows []JournalRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("Output %q: %v", out, err)
	}
	return rows
}

func TestClaudeToolEventsFromCallbackOnly(t *testing.T) {
	spec := func(f *fx) provider.AgentJobSpec { return f.spec() }
	f, d, res, ci := openJobChannelWith(t, spec, func(f *fx) {
		f.body = `echo '{"type":"tool","tool_name":"Grep","paths":["x.go"]}' >&2` + "\n" + f.body
	})
	edit := frameWith(t, ci, provider.CallbackPayload{Seq: 1, DedupKey: "e1", Kind: "stdout", ToolName: "Edit",
		DurationMS: 5, Paths: []string{"src/a.go"}})
	if acks := sendFrames(t, ci.Socket, edit); len(acks) != 1 || acks[0] != "ok" {
		t.Fatalf("authenticated tool frame acks %q", acks)
	}
	foreign := strings.Repeat("0", 32) + edit[32:]
	foreign = strings.Replace(foreign, `"tool_name":"Edit"`, `"tool_name":"Bash"`, 1)
	if acks := sendFrames(t, ci.Socket, foreign); len(acks) != 1 || acks[0] != "refused auth" {
		t.Fatalf("foreign-token frame acks %q", acks)
	}
	cr, err := finish(t, f, d, res.JobID)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var tools []string
	for _, r := range journalRows(t, cr.Output) {
		if r.ToolName != "" {
			tools = append(tools, r.ToolName)
		}
	}
	if len(tools) != 1 || tools[0] != "Edit" {
		t.Fatalf("tool rows %q, want only the authenticated frame's Edit", tools)
	}
	for _, leak := range []string{"Grep", "Bash", "x.go"} {
		if strings.Contains(cr.Output, leak) {
			t.Fatalf("Output carries %q from a source other than an authenticated frame: %s", leak, cr.Output)
		}
	}
}

func TestClaudeJSONResultYieldsNoToolEvents(t *testing.T) {
	f := newFx(t)
	f.body = "printf '%s\\n' '" + resultLine(t, func(m map[string]any) {
		m["tool_name"] = "Bash"
		m["permission_denials"] = []any{map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "p.txt"}}}
	}) + "'\n"
	_, cr, err := run(t, f.driver(f.config()), f.spec())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	rows := journalRows(t, cr.Output)
	if len(rows) != 1 || rows[0].Kind != provider.DriverEventDone || rows[0].ToolName != "" || len(rows[0].Paths) != 0 {
		t.Fatalf("rows %+v, want exactly the result's done row with no tool", rows)
	}
	if rows[0].ResultHash != hashHex("<scrubbed model output>") || cr.Cost == nil || cr.SessionID == "" {
		t.Fatalf("result row %+v cost %+v session %q: the result must still yield hash, cost and session", rows[0], cr.Cost, cr.SessionID)
	}
	if strings.Contains(cr.Output, "Bash") || strings.Contains(cr.Output, "Write") {
		t.Fatalf("Output %s carries a tool name from the result object", cr.Output)
	}
	g := newFx(t) // a tool-shaped stdout line before the result is not a tool event: the run refuses
	g.body = `echo '{"type":"tool","tool_name":"Read"}'` + "\n" + g.body
	if _, cr, err := run(t, g.driver(g.config()), g.spec()); err == nil || cr.Output != "" {
		t.Fatalf("Collect = %v, output %q: stdout must be the single result object", err, cr.Output)
	}
}
