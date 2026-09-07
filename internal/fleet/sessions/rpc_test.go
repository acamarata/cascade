// Purpose: RegisterHandlers/fleet.sessions.list and Client's required
//
//	tests: happy path over a real *rpc.Registry, an unknown filter field,
//	and Client.List's daemonless Windows tier-2 refusal.
//
// SPORT: internal.fleet.sessions.RegisterHandlers/ADDED,
//
//	internal.fleet.sessions.Client/ADDED (P1-E12-W3-S24-T3).
package sessions_test

import (
	"context"
	"encoding/json"
	"errors"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSessionsList_HappyPath(t *testing.T) {
	ctx := context.Background()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_000, 0))
	store := sessions.New(storetest.NewMemStore(), clock, nil)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1", Harness: "claude", State: "running"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s2", Harness: "codex", State: "running"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	registry := rpc.NewRegistry()
	sessions.RegisterHandlers(registry, store)

	req := &rpc.Request{JSONRPC: "2.0", Method: sessions.MethodList, Params: json.RawMessage(`{"harness":"claude"}`)}
	result, errObj := registry.Dispatch(ctx, req)
	if errObj != nil {
		t.Fatalf("Dispatch: %+v", errObj)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded struct {
		Sessions []sessions.SessionRecord `json:"sessions"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded.Sessions) != 1 || decoded.Sessions[0].SessionID != "s1" {
		t.Fatalf("fleet.sessions.list(harness=claude) = %+v, want [s1]", decoded.Sessions)
	}
}

func TestSessionsList_EmptyParams_ListsEverything(t *testing.T) {
	ctx := context.Background()
	store := sessions.New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)), nil)
	if err := store.Upsert(ctx, sessions.SessionRecord{SessionID: "s1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	registry := rpc.NewRegistry()
	sessions.RegisterHandlers(registry, store)

	req := &rpc.Request{JSONRPC: "2.0", Method: sessions.MethodList}
	result, errObj := registry.Dispatch(ctx, req)
	if errObj != nil {
		t.Fatalf("Dispatch with no params: %+v", errObj)
	}
	encoded, _ := json.Marshal(result)
	var decoded struct {
		Sessions []sessions.SessionRecord `json:"sessions"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded.Sessions) != 1 {
		t.Fatalf("fleet.sessions.list with no params = %+v, want all 1 session", decoded.Sessions)
	}
}

func TestSessionsList_UnknownFilterField(t *testing.T) {
	ctx := context.Background()
	store := sessions.New(storetest.NewMemStore(), testkit.NewFrozenClock(time.Unix(1_700_000_000, 0)), nil)

	registry := rpc.NewRegistry()
	sessions.RegisterHandlers(registry, store)

	req := &rpc.Request{JSONRPC: "2.0", Method: sessions.MethodList, Params: json.RawMessage(`{"cpu_cores":8}`)}
	_, errObj := registry.Dispatch(ctx, req)
	if errObj == nil {
		t.Fatal("Dispatch with unknown filter field = nil error, want a refusal")
	}
	if want := cascade.KindInvalidInput.JSONRPCCode(); errObj.Code != want {
		t.Fatalf("error code = %d, want %d (KindInvalidInput)", errObj.Code, want)
	}
	if !strings.Contains(errObj.Message, "cpu_cores") {
		t.Fatalf("error message = %q, want it to name the unknown field", errObj.Message)
	}
}

func TestSessionsListClient_HappyPath(t *testing.T) {
	caller := &recordingCaller{}
	client := sessions.NewClient(caller)
	if _, err := client.List(context.Background(), sessions.Filter{}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if !caller.called {
		t.Fatal("Client.List never called the RPCCaller")
	}
}

// failingCaller is an RPCCaller double returning a fixed error, used to
// prove Client.List propagates both a taxonomy error unchanged and a
// plain error wrapped as KindInternal.
type failingCaller struct{ err error }

func (f *failingCaller) Do(context.Context, string, any, any) error { return f.err }

func TestSessionsListClient_TaxonomyErrorPassesThrough(t *testing.T) {
	want := cascade.New(cascade.KindUnavailable, "boom")
	client := sessions.NewClient(&failingCaller{err: want})
	_, err := client.List(context.Background(), sessions.Filter{})
	if !errors.Is(err, want) {
		t.Fatalf("List error = %v, want it to carry KindUnavailable", err)
	}
}

func TestSessionsListClient_PlainErrorWrappedAsInternal(t *testing.T) {
	client := sessions.NewClient(&failingCaller{err: errors.New("transport exploded")})
	_, err := client.List(context.Background(), sessions.Filter{})
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("List error = %v, want KindInternal", err)
	}
}

// recordingCaller is a real-shape RPCCaller double whose Do decodes into
// the actual listResult wire shape via a JSON round trip, matching how a
// real transport would behave rather than relying on an internal type
// assertion.
type recordingCaller struct {
	called bool
}

func (r *recordingCaller) Do(_ context.Context, _ string, _, out any) error {
	r.called = true
	data, _ := json.Marshal(struct {
		Sessions []sessions.SessionRecord `json:"sessions"`
	}{})
	return json.Unmarshal(data, out)
}

// TestSessionsListClient_WindowsTier2Refusal proves Client.List refuses
// before ever calling the RPCCaller on Windows (tier-2: no daemon exists
// there at all). This ticket's files_scope names exactly rpc_test.go (no
// sibling rpc_windows_test.go), unlike internal/rpc/sse_windows_test.go's
// own dedicated `//go:build windows` file (R-14.131) — this test
// therefore self-skips off-Windows rather than living in a file this
// ticket has no standing to add; it DOES run for real on a Windows CI
// lane, where goruntime.GOOS == "windows" is true and Skip never fires.
func TestSessionsListClient_WindowsTier2Refusal(t *testing.T) {
	if goruntime.GOOS != "windows" {
		t.Skip("this refusal is GOOS-gated (rpc.go); only Windows CI actually exercises it")
	}
	caller := &recordingCaller{}
	client := sessions.NewClient(caller)
	_, err := client.List(context.Background(), sessions.Filter{})
	if !errors.Is(err, sessions.ErrWindowsTier2Unavailable) {
		t.Fatalf("List on windows tier-2 = %v, want ErrWindowsTier2Unavailable", err)
	}
	if caller.called {
		t.Fatal("Client.List must refuse before ever calling the RPCCaller on windows tier-2")
	}
}
