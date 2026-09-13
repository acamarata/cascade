package economics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/testkit"
)

type fixedResolver struct {
	id  string
	err error
}

func (f fixedResolver) ResolveProject(context.Context) (string, error) { return f.id, f.err }

func newRPCTestStore(t *testing.T, clock *testkit.FrozenClock) *SchedulerModeStore {
	t.Helper()
	db := newTestDB(t)
	return NewSchedulerModeStore(db, clock)
}

// TestFleetModeShowRPC is the named acceptance test (ARTICLE-2 REAL
// COUNTERPART): fleet.mode.show is exercised over a real
// *rpc.Registry.Dispatch call, the same production dispatch path the
// daemon's socket handler uses, never a bare call to the handler
// function.
func TestFleetModeShowRPC(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newRPCTestStore(t, clock)
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, store, fixedResolver{id: "proj-1"}, clock)

	params, _ := json.Marshal(map[string]string{"lifecycle_stage": "implement"})
	req := &rpc.Request{JSONRPC: "2.0", Method: MethodFleetModeShow, ID: json.RawMessage(`1`), Params: params}
	result, errObj := reg.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodFleetModeShow, errObj)
	}
	env, ok := result.(ModeEnvelope)
	if !ok {
		t.Fatalf("result type = %T, want ModeEnvelope", result)
	}
	if env.Version != "1" {
		t.Errorf("Version = %q, want 1", env.Version)
	}
	if env.Data.Mode != string(ModeBuild) || env.Data.Source != string(ModeSourceLifecycle) {
		t.Errorf("Data = %+v, want lifecycle-derived build", env.Data)
	}
	if env.Data.LifecycleDefault != string(ModeBuild) {
		t.Errorf("LifecycleDefault = %q, want build", env.Data.LifecycleDefault)
	}
}

func TestFleetModeSetRPC(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newRPCTestStore(t, clock)
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, store, fixedResolver{id: "proj-1"}, clock)

	params, _ := json.Marshal(map[string]string{"mode": "verify"})
	req := &rpc.Request{JSONRPC: "2.0", Method: MethodFleetModeSet, ID: json.RawMessage(`1`), Params: params}
	result, errObj := reg.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodFleetModeSet, errObj)
	}
	env, ok := result.(ModeEnvelope)
	if !ok {
		t.Fatalf("result type = %T, want ModeEnvelope", result)
	}
	if env.Data.Mode != string(ModeVerify) || env.Data.Source != string(ModeSourceExplicit) {
		t.Errorf("Data = %+v, want explicit verify", env.Data)
	}

	// A second Dispatch of fleet.mode.show now reports the explicit row.
	showReq := &rpc.Request{JSONRPC: "2.0", Method: MethodFleetModeShow, ID: json.RawMessage(`2`)}
	showResult, errObj := reg.Dispatch(context.Background(), showReq)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodFleetModeShow, errObj)
	}
	showEnv := showResult.(ModeEnvelope)
	if showEnv.Data.Mode != string(ModeVerify) {
		t.Errorf("fleet.mode.show after Set = %+v, want verify", showEnv.Data)
	}
}

func TestFleetModeEnvelopeVersion(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newRPCTestStore(t, clock)
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, store, fixedResolver{id: "proj-1"}, clock)
	if _, err := store.Set(context.Background(), "proj-1", ModeVerify, 0); err != nil {
		t.Fatalf("seed Set: %v", err)
	}

	req := &rpc.Request{JSONRPC: "2.0", Method: MethodFleetModeShow, ID: json.RawMessage(`1`)}
	result, errObj := reg.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch error = %+v", errObj)
	}
	env := result.(ModeEnvelope)
	if env.Version != modeEnvelopeVersion {
		t.Errorf("Version = %q, want %q", env.Version, modeEnvelopeVersion)
	}
	// Round-trip through JSON to prove the wire shape really carries
	// "version"/"data" keys, not just the Go field names.
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	if _, ok := generic["version"]; !ok {
		t.Error(`wire JSON missing "version" key`)
	}
	if _, ok := generic["data"]; !ok {
		t.Error(`wire JSON missing "data" key`)
	}
}

func TestFleetModeUnresolvedProject(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	store := newRPCTestStore(t, clock)
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, store, nil, clock)

	req := &rpc.Request{JSONRPC: "2.0", Method: MethodFleetModeShow, ID: json.RawMessage(`1`)}
	_, errObj := reg.Dispatch(context.Background(), req)
	if errObj == nil {
		t.Fatal("want an error for an omitted project_id with no resolver")
	}
}

func TestResolveProjectIDDirect(t *testing.T) {
	if _, err := resolveProjectID(context.Background(), "", nil); !errors.Is(err, ErrFleetModeUnresolvedProject) {
		t.Errorf("error = %v, want ErrFleetModeUnresolvedProject", err)
	}
	got, err := resolveProjectID(context.Background(), "explicit", fixedResolver{id: "ignored"})
	if err != nil || got != "explicit" {
		t.Errorf("resolveProjectID with explicit id = (%q, %v), want (explicit, nil)", got, err)
	}
}
