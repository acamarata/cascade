package daemon

// Purpose (this file): the status.widget RPC entry-point tests that need no
// registry: the method reaches a real *rpc.Registry.Dispatch, a nil deps is
// a typed error, and the captured fixture decodes as the contract says (the
// fixture itself is written by cmd/cascade's
// TestStatusWidgetRowsFromProviderEvidence, the production registration;
// see testdata/README.md). The refresh and emit behaviour is in
// status_widget_refresh_test.go.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// fakeWidgetNodeSource is a controllable capacity.NodeSource — the real
// nodes.NewRecordStore RegisterStatusWidgetHandler wires needs an actual
// enrolled device (an ed25519 identity) to report a non-empty List, more
// machinery than this test's Compositor-tick trigger needs; swapping
// deps.nodeSrc (package-private, same-package test) for this fake is the
// direct way to make UpdateNodes observe a real before/after change.
type fakeWidgetNodeSource struct{ devices []nodes.DeviceRecord }

func (f *fakeWidgetNodeSource) List() ([]nodes.DeviceRecord, error) { return f.devices, nil }

func setupStatusWidget(t *testing.T, bus *events.Bus) (*rpc.Registry, *StatusWidgetDeps) {
	t.Helper()
	root := t.TempDir()
	paths := fakePaths{root: root}
	if err := os.MkdirAll(paths.DataDir(), 0o700); err != nil {
		t.Fatalf("MkdirAll(DataDir): %v", err)
	}
	clock := runtime.NewFixedClock(time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC))
	store := storetest.NewMemStore()
	registry := rpc.NewRegistry()
	deps, err := RegisterStatusWidgetHandler(context.Background(), registry, store, nil, clock, bus, paths, func() bool { return false })
	if err != nil {
		t.Fatalf("RegisterStatusWidgetHandler: %v", err)
	}
	if deps == nil {
		t.Fatal("RegisterStatusWidgetHandler returned a nil deps for a real store")
	}
	// Registered after t.TempDir()'s own cleanup (line above), so t.Cleanup's
	// LIFO order runs this FIRST: the jobs-domain cascade.db connection is
	// closed before TempDir tries to remove the directory it lives in.
	// Without this, RemoveAll fails on Windows (open-file delete refusal)
	// though it passes silently on POSIX, which unlinks an open file.
	t.Cleanup(func() { _ = deps.Close() })
	return registry, deps
}

func TestStatusWidgetRPC(t *testing.T) {
	registry, _ := setupStatusWidget(t, nil)
	if !registry.Registered(MethodStatusWidget) {
		t.Fatal("status.widget never reached the registry")
	}
	req := &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget, ID: json.RawMessage(`1`)}
	result, errObj := registry.Dispatch(context.Background(), req)
	if errObj != nil {
		t.Fatalf("Dispatch(%s) error = %+v", MethodStatusWidget, errObj)
	}
	snap, ok := result.(capacity.WidgetSnapshot)
	if !ok {
		t.Fatalf("result is %T, want capacity.WidgetSnapshot", result)
	}
	if snap.Rows == nil || snap.Nodes == nil || snap.Seq != 0 {
		t.Errorf("snapshot = %+v, want empty non-nil rows and nodes and seq 0 before any frame", snap)
	}
}

func TestStatusWidgetRPC_DaemonNotReady(t *testing.T) {
	registry := rpc.NewRegistry()
	registry.Register(MethodStatusWidget, statusWidgetHandler(nil))
	req := &rpc.Request{JSONRPC: "2.0", Method: MethodStatusWidget}
	_, errObj := registry.Dispatch(context.Background(), req)
	if errObj == nil {
		t.Fatal("expected a typed error for a nil deps, got none")
	}
}

// TestStatusWidgetRPCFixtureContract decodes the captured fixture
// (testdata/fixture_status_widget_rpc.json, written by cmd/cascade's
// TestStatusWidgetFixtureFromEvidencePath over the production registration)
// and holds it to the contract the Swift client is built against: the five
// states, the reset only on a five_hour window, nulls for absent sources,
// an opaque ref and "redacted" label for the email-named provider, and no
// address anywhere in the file.
func TestStatusWidgetRPCFixtureContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fixture_status_widget_rpc.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if strings.Contains(string(raw), "@") {
		t.Error("fixture carries an address: the email-named provider must appear only as ref-<12 hex> and redacted")
	}
	var fx struct {
		Result capacity.WidgetSnapshot `json:"result"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	snap := fx.Result
	if len(snap.Rows) != 5 || len(snap.Nodes) != 1 || snap.AttentionCount != 1 || snap.ActiveJobsCount == nil || *snap.ActiveJobsCount != 2 || len(snap.Projects) != 0 {
		t.Fatalf("fixture shape: %d rows %d nodes attention %d jobs %v, want 5, 1, 1, 2", len(snap.Rows), len(snap.Nodes), snap.AttentionCount, snap.ActiveJobsCount)
	}
	states := map[string]bool{}
	opaque := regexp.MustCompile(`^ref-[0-9a-f]{12}$`)
	for _, r := range snap.Rows {
		states[r.State] = true
		if r.SevenDay == nil || r.SevenDay.ResetsIn != nil || r.FiveHour == nil || r.ReauthRequired != (r.State == "auth-required") {
			t.Errorf("row %q breaks the window or reauth contract: %+v", r.Ref, r)
		}
		if opaque.MatchString(r.Ref) != (r.Label == "redacted") {
			t.Errorf("row ref %q label %q: an opaque ref and the redacted label go together", r.Ref, r.Label)
		}
		if r.State == "exhausted" && (r.FiveHour.ResetsIn == nil || *r.FiveHour.ResetsIn != 7200*time.Second) {
			t.Errorf("the exhausted row's five_hour = %+v, want resets_in 7200000000000", r.FiveHour)
		}
	}
	for _, want := range []string{"available", "auth-required", "exhausted", "unknown"} {
		if !states[want] {
			t.Errorf("fixture has no %s row", want)
		}
	}
}
