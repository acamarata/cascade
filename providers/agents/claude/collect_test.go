//go:build !windows

package claude

// Purpose: Collect over the real (scrubbed) result object: cost recorded as
//   a cli-observation estimate, session id and structured output; the
//   closed journal projection; the OutputFilter fail-closed path; the
//   bounded buffer; exit and error-result failures; model-verb refusals.
// SPORT: providers.agents.claude/ADD (P1-E30-W6-S61-T2).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestHeadlessCostRecordedAsEstimate(t *testing.T) {
	f := newFx(t)
	_, cr, err := run(t, f.driver(f.config()), f.spec())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if cr.Cost == nil || cr.Cost.USD != 0.0481948 || cr.Cost.Source != provider.CostSourceCLIObservation {
		t.Fatalf("Cost = %+v, want 0.0481948 as a cli-observation estimate", cr.Cost)
	}
	if cr.SessionID != "00000000-0000-4000-8000-000000000003" || cr.StructuredOutput != nil {
		t.Fatalf("SessionID %q StructuredOutput %s", cr.SessionID, cr.StructuredOutput)
	}
	f2 := newFx(t)
	f2.body = "printf '%s\\n' '" + resultLine(t, func(m map[string]any) {
		m["structured_output"] = map[string]any{"verdict": "pass"}
	}) + "'\n"
	_, cr, err = run(t, f2.driver(f2.config()), f2.spec())
	if err != nil || string(cr.StructuredOutput) != `{"verdict":"pass"}` {
		t.Fatalf("structured output %s, err %v", cr.StructuredOutput, err)
	}
}

func TestClaudeOutputFilterErrorWithholds(t *testing.T) {
	f := newFx(t)
	pidf := filepath.Join(f.dir, "pid")
	f.body = "echo $$ > " + pidf + "\necho before\necho SECRETLINE\nexec sleep 30\n"
	spec := f.spec()
	spec.Filter = func(b []byte) ([]byte, error) {
		if strings.Contains(string(b), "SECRETLINE") {
			return nil, errors.New("substitution failed")
		}
		return b, nil
	}
	d := f.driver(f.config())
	start := time.Now()
	res, cr, err := run(t, d, spec)
	if err != provider.ErrEgressSubstitutionFailed {
		t.Fatalf("Collect = %v, want the ErrEgressSubstitutionFailed sentinel", err)
	}
	if cr.Output != "" || cr.Artifacts != nil || state(t, d, res.JobID) != provider.AgentRunFailed {
		t.Fatalf("a failed-filter run leaked output %q / state %s", cr.Output, state(t, d, res.JobID))
	}
	if pid := readPid(t, pidf); alive(pid) || time.Since(start) > 10*time.Second {
		t.Fatalf("the child was not cancelled (alive %v after %s)", alive(pid), time.Since(start))
	}
}

func TestClaudeNoRawTranscriptPersisted(t *testing.T) {
	spec := func(f *fx) provider.AgentJobSpec { return f.spec() }
	f, d, res, ci := openJobChannelWith(t, spec, func(f *fx) {
		f.body = "echo 'model says: the prompt was hello' >&2\n" + f.body
	})
	if acks := sendFrames(t, ci.Socket, frameWith(t, ci, provider.CallbackPayload{Seq: 1, DedupKey: "r1", Kind: "stdout",
		ToolName: "Read", DurationMS: 12, Paths: []string{"a.go"}})); len(acks) != 1 || acks[0] != "ok" {
		t.Fatalf("tool frame acks %q", acks)
	}
	cr, err := finish(t, f, d, res.JobID)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if _, err := d.RawTranscript("any"); err != provider.ErrTranscriptPersistenceForbidden {
		t.Fatalf("RawTranscript = %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(cr.Output), &rows); err != nil || len(rows) != 2 {
		t.Fatalf("Output %s: %v", cr.Output, err)
	}
	want := []string{"data_class", "duration_ms", "event_kind", "exit_code", "paths", "protocol_version", "result_hash", "seq", "tool_name"}
	for _, row := range rows {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, want) {
			t.Fatalf("row keys %v, want exactly %v", keys, want)
		}
	}
	for _, leak := range []string{"hello", "model says", "scrubbed model output", "00000000-0000-4000-8000-000000000003"} {
		if strings.Contains(cr.Output, leak) {
			t.Fatalf("persisted output carries %q", leak)
		}
	}
	if rows[0]["result_hash"] != hashHex("<scrubbed model output>") || rows[1]["tool_name"] != "Read" {
		t.Fatalf("rows %v", rows)
	}
}

func TestCollectFailureVerdicts(t *testing.T) {
	cases := map[string]struct{ body, msg string }{
		"nonzero exit":     {"exit 7\n", "exited with status 7"},
		"error result":     {"printf '%s\\n' '" + `{"type":"result","subtype":"error_max_turns","is_error":true,"session_id":"s","total_cost_usd":0,"num_turns":1,"usage":{}}` + "'\n", "error result"},
		"two objects":      {"echo '{}{}'\n", "not one result object"},
		"no output":        {"exit 0\n", "not one result object"},
		"buffer overflow":  {"exec yes\n", "buffer overflow"},
		"wrong result key": {"echo '{\"type\":\"assistant\"}'\n", "revision 1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFx(t)
			f.body = c.body
			d := f.driver(f.config())
			res, cr, err := run(t, d, f.spec())
			if err == nil || !strings.Contains(err.Error(), c.msg) || cr.Output != "" {
				t.Fatalf("Collect = %v (output %q), want %q", err, cr.Output, c.msg)
			}
			if state(t, d, res.JobID) != provider.AgentRunFailed {
				t.Fatalf("state %s, want failed", state(t, d, res.JobID))
			}
		})
	}
}

func TestAcceptedJobArtifacts(t *testing.T) {
	f := newFx(t)
	f.body = "echo x > made.txt\n" + f.body
	d := f.driver(f.config())
	res, cr, err := run(t, d, f.spec())
	if err != nil || !reflect.DeepEqual(cr.Artifacts, []string{"made.txt"}) {
		t.Fatalf("Artifacts %q (err %v): the driver's own .claude files must be excluded", cr.Artifacts, err)
	}
	if state(t, d, res.JobID) != provider.AgentRunAccepted {
		t.Fatalf("clean exit reads %s, want accepted (R-14.316)", state(t, d, res.JobID))
	}
	live, err := d.Artifacts(context.Background(), res.JobID)
	if err != nil || !reflect.DeepEqual(live, []string{"made.txt"}) {
		t.Fatalf("live Artifacts %q, %v", live, err)
	}
	if exists(filepath.Join(f.root, ".claude", provider.CallbackFileName)) {
		t.Fatal("the callback token file outlived the job")
	}
}

func TestModelVerbsAndApprovalsRefuse(t *testing.T) {
	f := newFx(t)
	d := f.driver(f.config())
	ctx := context.Background()
	if _, err := d.Chat(ctx, provider.ChatRequest{}); err != errUnsupportedModelOp {
		t.Fatal("Chat")
	}
	if _, err := d.Embed(ctx, provider.ModelEmbedRequest{}); err != errUnsupportedModelOp {
		t.Fatal("Embed")
	}
	if _, err := d.Count(ctx, provider.CountRequest{}); err != errUnsupportedModelOp {
		t.Fatal("Count")
	}
	if err := d.Stream(ctx, provider.ChatRequest{}, nil); err != errUnsupportedModelOp {
		t.Fatal("Stream")
	}
	if _, open := <-d.ApprovalRequests(); open {
		t.Fatal("ApprovalRequests is open")
	}
	if err := d.ResolveApproval("x", "y"); err == nil {
		t.Fatal("ResolveApproval resolved a request that was never raised")
	}
}

// FuzzClaudeResultDecode drives the driver-local result decoder (seeds in
// testdata/fuzz/FuzzClaudeResultDecode/ plus the committed capture): it
// never panics, accepts only one well-formed revision-1 object that is not
// an error result, and decodes the same input the same way every time.
func FuzzClaudeResultDecode(f *testing.F) {
	capture, err := os.ReadFile(filepath.Join("testdata", "protocol", "1", "result.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(capture)
	f.Fuzz(func(t *testing.T, line []byte) {
		res, err := parseResult(line)
		again, err2 := parseResult(line)
		if (err == nil) != (err2 == nil) || !reflect.DeepEqual(res, again) {
			t.Fatalf("decoding %q is not deterministic", line)
		}
		if err != nil {
			return
		}
		if !json.Valid(line) || res.Type != "result" || res.Subtype == "" || res.SessionID == "" ||
			res.CostUSD == nil || res.NumTurns == nil || res.IsError == nil || *res.IsError || len(res.Usage) == 0 {
			t.Fatalf("parseResult accepted %q as %+v", line, res)
		}
	})
}
