package coretools_test

// Purpose: the CI tool declarations are honest about what the methods
//   behind them accept: the mutating flag follows the capability, and
//   every schema property is a parameter the real ci.run / ci.status
//   handler accepts while an invented one is refused.
// Inputs: the live Specs() and the real internal/ci handlers over a temp
//   HOME.
// Constraints: no step is ever run -- ci.run is pointed at a directory that
//   is not a checkout, so it refuses after decoding its params.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

func ciSpec(t *testing.T, name string) coretools.Spec {
	t.Helper()
	for _, s := range coretools.Specs() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("%s is not registered", name)
	return coretools.Spec{}
}

func TestCISpecsMutatingFlags(t *testing.T) {
	run, status := ciSpec(t, "cascade_ci_run"), ciSpec(t, "cascade_ci_status")
	if !run.Mutating || run.Capability != "ci.run" || run.Method != "ci.run" {
		t.Errorf("cascade_ci_run = {mutating %v, capability %q, method %q}, want {true, ci.run, ci.run}",
			run.Mutating, run.Capability, run.Method)
	}
	if status.Mutating || status.Capability != "ci.read" || status.Method != "ci.status" {
		t.Errorf("cascade_ci_status = {mutating %v, capability %q, method %q}, want {false, ci.read, ci.status}",
			status.Mutating, status.Capability, status.Method)
	}
	for _, s := range coretools.Specs() {
		if s.Capability == coretools.CapabilityCIRun && !s.Mutating {
			t.Errorf("%q needs the run capability but is not marked mutating", s.Name)
		}
		if s.Capability == coretools.CapabilityCIRead && s.Mutating {
			t.Errorf("%q needs only the read capability but is marked mutating", s.Name)
		}
	}
}

// ciHandlers binds the real handlers; ci.run is pointed at a directory with
// no .git so it can only refuse, never run.
func ciHandlers(t *testing.T) *rpc.Registry {
	t.Helper()
	home, notRepo := t.TempDir(), t.TempDir()
	getenv := func(k string) string {
		if k == "CASCADE_HOME" {
			return home
		}
		return ""
	}
	paths, err := runtime.NewPathProvider(getenv, func() (string, error) { return home, nil })
	if err != nil {
		t.Fatal(err)
	}
	reg := rpc.NewRegistry()
	ci.RegisterMCPMethods(reg, ci.CmdDeps{
		Paths: paths, Getenv: getenv, Clock: runtime.NewFixedClock(time.Unix(1_700_000_000, 0)),
	}, func() (string, error) { return notRepo, nil })
	return reg
}

// dispatchMessage returns the refusal message, or "" when the call succeeded.
func dispatchMessage(reg *rpc.Registry, method, params string) string {
	_, eo := reg.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: method, Params: json.RawMessage(params)})
	if eo == nil {
		return ""
	}
	return eo.Message
}

func TestCISpecsSchemasMatchParams(t *testing.T) {
	reg := ciHandlers(t)
	sample := map[string]string{"boolean": "true", "integer": "5"}
	for _, tc := range []struct {
		tool   string
		props  []string
		method string
	}{
		{"cascade_ci_run", []string{"build_only", "lint_only", "test_only"}, "ci.run"},
		{"cascade_ci_status", []string{"limit"}, "ci.status"},
	} {
		spec := ciSpec(t, tc.tool)
		props, _ := spec.Schema["properties"].(map[string]any)
		var got []string
		for name, raw := range props {
			got = append(got, name)
			typ, _ := raw.(map[string]any)["type"].(string)
			value, ok := sample[typ]
			if !ok {
				t.Fatalf("%s.%s has type %q: the test has no sample value for it", tc.tool, name, typ)
			}
			if msg := dispatchMessage(reg, tc.method, `{"`+name+`":`+value+`}`); strings.Contains(msg, "unknown field") {
				t.Errorf("%s declares %q but %s refuses it: %s", tc.tool, name, tc.method, msg)
			}
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(tc.props, ",") {
			t.Errorf("%s properties = %v, want %v", tc.tool, got, tc.props)
		}
		if spec.Schema["additionalProperties"] != false {
			t.Errorf("%s permits additional properties", tc.tool)
		}
		if required, _ := spec.Schema["required"].([]string); len(required) != 0 {
			t.Errorf("%s requires %v; every parameter is optional", tc.tool, required)
		}
		if msg := dispatchMessage(reg, tc.method, `{"invented_property":true}`); !strings.Contains(msg, "unknown field") {
			t.Errorf("%s accepted a property its schema does not declare: %q", tc.method, msg)
		}
		for _, banned := range []string{"repo", "path", "cwd", "dir", "command", "cmd", "args"} {
			if _, present := props[banned]; present {
				t.Errorf("%s declares a %q property: the caller must not choose where or what runs", tc.tool, banned)
			}
		}
	}
}
