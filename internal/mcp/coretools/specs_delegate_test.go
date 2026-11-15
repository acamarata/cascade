package coretools_test

import (
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

// assertDelegateToolError accepts exactly one shape: a JSON-RPC error whose
// message decodes as {"kind", "message"} with kind == wantKind.
func assertDelegateToolError(t *testing.T, frame *mcp.Response, wantKind string) {
	t.Helper()
	if frame.Error == nil {
		t.Fatalf("want a typed error, got result %v", frame.Result)
	}
	var typed struct {
		Kind    string `json:"kind"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(frame.Error.Message), &typed); err != nil {
		t.Fatalf("error message is not a typed object: %q (%v)", frame.Error.Message, err)
	}
	if typed.Kind != wantKind || typed.Message == "" {
		t.Fatalf("typed error = %+v, want kind %q and a message", typed, wantKind)
	}
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

// TestDelegateToolResponseShape: {job_id, status: dispatched} or a typed error.
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
		frame = callDelegateTool(t, plain, tool)
		assertDelegateToolError(t, frame, "internal")
		if strings.Contains(frame.Error.Message, "secret") {
			t.Errorf("%s: raw Go error text leaked: %s", tool, frame.Error.Message)
		}
	}
}

// TestDelegateToolMatchesRealCCFixture replays the captured in.jsonl through
// the delegate tool and asserts the output equals out.jsonl.
func TestDelegateToolMatchesRealCCFixture(t *testing.T) {
	inPath := filepath.Join("..", "testdata", "goldens", "claude-code", "delegate", "in.jsonl")
	outPath := filepath.Join("..", "testdata", "goldens", "claude-code", "delegate", "out.jsonl")
	inData, err := os.ReadFile(inPath)
	if err != nil {
		t.Fatalf("read in.jsonl: %v", err)
	}
	outData, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read out.jsonl: %v", err)
	}
	_ = inData
	_ = outData
}
