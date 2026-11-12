package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func fleetHealthTestEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CASCADE_HOME", home)
}

func fleetHealthTestSummary() FleetHealthSummaryDTO {
	executive, build, adversary := 0.12, 0.9, 0.75
	return FleetHealthSummaryDTO{Status: "source", Keys: [5]FleetHealthKeyDTO{
		{Key: "executive", State: "protected", Percentage: &executive},
		{Key: "build", State: "healthy", Percentage: &build},
		{Key: "bulk", State: "scarce"},
		{Key: "adversary", State: "abundant", Percentage: &adversary},
		{Key: "long_context", State: "unavailable"},
	}}
}

func TestEXE14LegacyAC6(t *testing.T) {
	fleetHealthTestEnv(t)
	want := `{"status":"ok","keys":[{"key":"executive","state":"protected","percentage":0.12},{"key":"build","state":"healthy","percentage":0.9},{"key":"bulk","state":"scarce"},{"key":"adversary","state":"abundant","percentage":0.75},{"key":"long_context","state":"unavailable"}]}`
	for _, params := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		registry, calls := NewRegistry(), 0
		summary := fleetHealthTestSummary()
		err := RegisterFleetHealthSummary(registry, FleetHealthDeps{Summary: func(context.Context) (FleetHealthSummaryDTO, error) {
			calls++
			return summary, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		result, refusal := registry.Dispatch(context.Background(), &Request{Method: FleetHealthSummaryMethod, Params: params})
		if refusal != nil || calls != 1 {
			t.Fatalf("refusal=%v calls=%d", refusal, calls)
		}
		got, ok := result.(FleetHealthSummaryDTO)
		if !ok || got.Keys[0].Percentage != summary.Keys[0].Percentage {
			t.Fatalf("DTO or percentage pointer changed: %#v", result)
		}
		raw, err := json.Marshal(result)
		if err != nil || string(raw) != want {
			t.Fatalf("result=%s err=%v; want %s", raw, err, want)
		}
	}
}

func TestFleetHealthSummaryRefusesSourceError(t *testing.T) {
	fleetHealthTestEnv(t)
	registry, calls := NewRegistry(), 0
	err := RegisterFleetHealthSummary(registry, FleetHealthDeps{Summary: func(context.Context) (FleetHealthSummaryDTO, error) {
		calls++
		return fleetHealthTestSummary(), errors.New("boom secret_ref=x")
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, refusal := registry.Dispatch(context.Background(), &Request{Method: FleetHealthSummaryMethod})
	if result != nil || refusal == nil || refusal.Code != cascade.KindUnavailable.JSONRPCCode() || calls != 1 {
		t.Fatalf("result=%v refusal=%v calls=%d", result, refusal, calls)
	}
	raw, err := json.Marshal(refusal)
	if err != nil || strings.Contains(string(raw), "boom") || strings.Contains(string(raw), "secret_ref") {
		t.Fatalf("source error leaked: %s err=%v", raw, err)
	}
}

func TestFleetHealthSummaryRefusesMalformedSource(t *testing.T) {
	fleetHealthTestEnv(t)
	cases := map[string]func(*FleetHealthSummaryDTO){
		"order": func(s *FleetHealthSummaryDTO) { s.Keys[0], s.Keys[1] = s.Keys[1], s.Keys[0] },
		"key":   func(s *FleetHealthSummaryDTO) { s.Keys[2].Key = "gpu" },
		"state": func(s *FleetHealthSummaryDTO) { s.Keys[3].State = "ok" },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			summary := fleetHealthTestSummary()
			corrupt(&summary)
			registry, calls := NewRegistry(), 0
			err := RegisterFleetHealthSummary(registry, FleetHealthDeps{Summary: func(context.Context) (FleetHealthSummaryDTO, error) {
				calls++
				return summary, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			result, refusal := registry.Dispatch(context.Background(), &Request{Method: FleetHealthSummaryMethod})
			if result != nil || refusal == nil || refusal.Code != cascade.KindInternal.JSONRPCCode() || calls != 1 {
				t.Fatalf("result=%v refusal=%v calls=%d", result, refusal, calls)
			}
		})
	}
}

func TestRegisterFleetHealthSummaryRefusesNil(t *testing.T) {
	fleetHealthTestEnv(t)
	calls := 0
	deps := FleetHealthDeps{Summary: func(context.Context) (FleetHealthSummaryDTO, error) {
		calls++
		return fleetHealthTestSummary(), nil
	}}
	registry := NewRegistry()
	for _, err := range []error{RegisterFleetHealthSummary(nil, deps), RegisterFleetHealthSummary(registry, FleetHealthDeps{})} {
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Fatalf("registration error=%v", err)
		}
	}
	if registry.Registered(FleetHealthSummaryMethod) {
		t.Fatal("nil dependency registered a handler")
	}
	if err := RegisterFleetHealthSummary(registry, deps); err != nil {
		t.Fatal(err)
	}
	result, refusal := dispatchJSON(t, registry, FleetHealthSummaryMethod, map[string]any{"x": 1})
	if result != nil || refusal == nil || refusal.Code != cascade.KindInvalidInput.JSONRPCCode() || calls != 0 {
		t.Fatalf("result=%s refusal=%v calls=%d", result, refusal, calls)
	}
}

func TestFleetHealthNoExecutiveImport(t *testing.T) {
	fleetHealthTestEnv(t)
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("rpc files=%v err=%v", files, err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"internal/executive", "internal/fleet/economics", "internal/fleet/topology", "internal/daemon"} {
				prefix := "github.com/acamarata/cascade/" + banned
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					t.Errorf("%s imports %s", name, path)
				}
			}
		}
	}
	if got := declaredAccess("fleet.health.summary"); got != AccessOwnerView {
		t.Fatalf("access=%v, want owner view", got)
	}
}
