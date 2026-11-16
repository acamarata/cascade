package coretools_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// delegateParamsShape mirrors the conductor.delegate params type.
type delegateParamsShape struct {
	Intent        string `json:"intent"`
	TaskClass     string `json:"task_class,omitempty"`
	MinQuality    string `json:"min_quality,omitempty"`
	Deadline      string `json:"deadline,omitempty"`
	Mutation      bool   `json:"mutation"`
	JobID         string `json:"job_id,omitempty"`
	WaitTimeoutMS int64  `json:"wait_timeout_ms,omitempty"`
}

// delegateWaitShape mirrors the conductor.delegate_wait params type.
type delegateWaitShape struct {
	JobID     string `json:"job_id"`
	TimeoutMS int64  `json:"timeout_ms"`
}

func delegateSpecByName(t *testing.T, name string) coretools.Spec {
	t.Helper()
	for _, s := range coretools.Specs() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s is not in Specs()", name)
	return coretools.Spec{}
}

func jsonFieldNames(v any) []string {
	rt := reflect.TypeOf(v)
	var names []string
	for i := 0; i < rt.NumField(); i++ {
		names = append(names, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
	}
	sort.Strings(names)
	return names
}

// TestDelegateToolSpecMatchesParams verifies schema fields and additionalProperties false.
func TestDelegateToolSpecMatchesParams(t *testing.T) {
	cases := []struct {
		name     string
		method   string
		mutating bool
		params   any
		required []string
	}{
		{"cascade_conductor_delegate", "conductor.delegate", true, delegateParamsShape{}, []string{"intent"}},
		{"cascade_conductor_delegate_wait", "conductor.delegate_wait", false, delegateWaitShape{}, []string{"job_id"}},
	}
	for _, c := range cases {
		spec := delegateSpecByName(t, c.name)
		if spec.Method != c.method || spec.Capability != coretools.CapabilityAgentsDelegate || spec.Mutating != c.mutating {
			t.Errorf("%s: method=%q capability=%q mutating=%v", c.name, spec.Method, spec.Capability, spec.Mutating)
		}
		if spec.Schema["additionalProperties"] != false {
			t.Errorf("%s: additionalProperties is %v, want false", c.name, spec.Schema["additionalProperties"])
		}
		props, _ := spec.Schema["properties"].(map[string]any)
		var got []string
		for k := range props {
			got = append(got, k)
		}
		sort.Strings(got)
		if want := jsonFieldNames(c.params); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: schema properties %v, params fields %v", c.name, got, want)
		}
		if !reflect.DeepEqual(spec.Schema["required"], c.required) {
			t.Errorf("%s: required = %v, want %v", c.name, spec.Schema["required"], c.required)
		}
	}
}

// delegateTable is a method table serving both delegate verbs through fn.
func delegateTable(fn rpc.HandlerFunc) *rpc.Registry {
	reg := rpc.NewRegistry()
	reg.Register("conductor.delegate", fn)
	reg.Register("conductor.delegate_wait", fn)
	return reg
}

// TestDelegateToolRegistration exercises the production registration path.
func TestDelegateToolRegistration(t *testing.T) {
	ok := func(context.Context, json.RawMessage) (any, error) { return map[string]string{}, nil }
	var names []string
	for _, reg := range coretools.Registrations(delegateTable(ok)) {
		if reg.RequiredCapability == coretools.CapabilityAgentsDelegate {
			names = append(names, reg.Tool.Name)
		}
	}
	sort.Strings(names)
	want := []string{"cascade_conductor_delegate", "cascade_conductor_delegate_wait"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("registered delegate tools = %v, want %v", names, want)
	}
	if got := coretools.Registrations(rpc.NewRegistry()); len(got) != 0 {
		t.Fatalf("an empty method table registered %d tools", len(got))
	}
}

// assertDelegateToolError accepts exactly one shape: a tools/call result
// with isError true and one text block that decodes as {"kind", "message"}
// with kind == wantKind, never a JSON-RPC error. It returns that text.
func assertDelegateToolError(t *testing.T, frame *mcp.Response, wantKind string) string {
	t.Helper()
	if frame.Error != nil {
		t.Fatalf("want an isError tool result, got JSON-RPC error %v", frame.Error)
	}
	raw, err := json.Marshal(frame.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || !result.IsError ||
		len(result.Content) != 1 || result.Content[0].Type != "text" {
		t.Fatalf("want one text block with isError true, got %s (%v)", raw, err)
	}
	var typed struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	text := result.Content[0].Text
	if err := json.Unmarshal([]byte(text), &typed); err != nil {
		t.Fatalf("tool error text is not a typed object: %q (%v)", text, err)
	}
	if typed.Kind != wantKind || typed.Message == "" {
		t.Fatalf("typed error = %+v, want kind %q and a message", typed, wantKind)
	}
	return text
}

func callDelegateTool(t *testing.T, fn rpc.HandlerFunc, tool string) *mcp.Response {
	t.Helper()
	regs := coretools.Registrations(delegateTable(fn))
	allow := grantingFilter{coretools.CapabilityAgentsDelegate: true}
	srv := mcp.NewServer(mcp.NewToolRegistry(noPlugins, allow, regs...))
	params := `{"name":"` + tool + `","arguments":{"intent":"list files","job_id":"j1"}}`
	return srv.Dispatch(context.Background(), &mcp.Frame{
		JSONRPC: "2.0", Method: "tools/call", ID: json.RawMessage("1"), Params: json.RawMessage(params),
	})
}

// TestDelegateToolResponseShape: {job_id, status: dispatched} or a typed
// error carried as an isError tool result.
func TestDelegateToolResponseShape(t *testing.T) {
	success := func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"job_id": "j1", "status": "dispatched"}, nil
	}
	typed := func(context.Context, json.RawMessage) (any, error) {
		return nil, cascade.New(cascade.KindUnavailable, "the daemon is not running")
	}
	plain := func(context.Context, json.RawMessage) (any, error) {
		return nil, fmt.Errorf("dial unix /secret/path.sock: connection refused")
	}
	for _, tool := range []string{"cascade_conductor_delegate", "cascade_conductor_delegate_wait"} {
		frame := callDelegateTool(t, success, tool)
		if frame.Error != nil {
			t.Fatalf("%s: successful call refused: %v", tool, frame.Error)
		}
		raw, _ := json.Marshal(frame.Result)
		if !strings.Contains(string(raw), `job_id`) || !strings.Contains(string(raw), `dispatched`) {
			t.Errorf("%s: result lacks job_id/status: %s", tool, raw)
		}
		assertDelegateToolError(t, callDelegateTool(t, typed, tool), "unavailable")
		text := assertDelegateToolError(t, callDelegateTool(t, plain, tool), "internal")
		if strings.Contains(text, "secret") {
			t.Errorf("%s: raw Go error text leaked: %s", tool, text)
		}
	}
}

// TestDelegateToolMatchesRealCCFixture replays the captured in.jsonl through
// the delegate tool's production registration and asserts the tools/call
// answer equals the last frame of out.jsonl. capture.json must mark the capture needs:daemon,
// which is why ReplayAll leaves it to this test.
func TestDelegateToolMatchesRealCCFixture(t *testing.T) {
	dir := filepath.Join("..", "testdata", "goldens", "claude-code", "delegate")
	var meta struct {
		Client string `json:"client"`
		Needs  string `json:"needs"`
	}
	if err := json.Unmarshal(readFixture(t, filepath.Join(dir, "capture.json")), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Client != "claude-code" || meta.Needs != "daemon" {
		t.Fatalf("capture.json = %+v, want client claude-code needing a daemon", meta)
	}
	dispatched := func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"job_id": "j_test", "status": "dispatched"}, nil
	}
	srv := mcp.NewServer(mcp.NewToolRegistry(noPlugins, grantingFilter{coretools.CapabilityAgentsDelegate: true},
		coretools.Registrations(delegateTable(dispatched))...))
	var last *mcp.Response
	for _, line := range bytes.Split(bytes.TrimSpace(readFixture(t, filepath.Join(dir, "in.jsonl"))), []byte("\n")) {
		frame, perr := mcp.ParseFrame(line)
		if perr != nil {
			t.Fatalf("captured frame does not parse: %v", perr)
		}
		if resp := srv.Dispatch(context.Background(), frame); resp != nil {
			last = resp
		}
	}
	got, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	recorded := bytes.Split(bytes.TrimSpace(readFixture(t, filepath.Join(dir, "out.jsonl"))), []byte("\n"))
	if want := recorded[len(recorded)-1]; canonical(t, got) != canonical(t, want) {
		t.Errorf("delegate answer = %s, the capture recorded %s", got, want)
	}
}

// readFixture reads one capture file.
func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// canonical re-encodes a JSON frame with sorted keys.
func canonical(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
