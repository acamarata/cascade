package supervision

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// registryCaller adapts a real *rpc.Registry to this package's RPCCaller
// seam, round-tripping through the SAME Dispatch path a real daemon
// request takes (marshal params, Registry.Dispatch, unmarshal result) —
// Art.2's "exercised via the real Go client SDK, not hand-crafted JSON
// payloads": Client (client.go) is the real SDK under test here, and
// this adapter is its transport, not a shortcut around it.
type registryCaller struct {
	reg *rpc.Registry
	id  int
}

func (c *registryCaller) Do(ctx context.Context, method string, params, out any) error {
	c.id++
	rawParams, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req := &rpc.Request{JSONRPC: "2.0", Method: method, Params: rawParams, ID: json.RawMessage(`1`)}
	result, errObj := c.reg.Dispatch(ctx, req)
	if errObj != nil {
		if kind, ok := cascade.KindFromJSONRPCCode(errObj.Code); ok {
			return cascade.New(kind, errObj.Message)
		}
		return cascade.Newf(cascade.KindInternal, "rpc error %d: %s", errObj.Code, errObj.Message)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func newTestRegistry(t *testing.T) (*rpc.Registry, *Store) {
	t.Helper()
	store := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	reg := rpc.NewRegistry()
	RegisterHandlers(reg, store, nil)
	return reg, store
}

// TestFleetAttentionRPC drives fleet.attention.list/get/ack through the
// real Client SDK over a real *rpc.Registry: empty queue -> empty list;
// push one item -> list returns it; ack -> no longer in the default list.
func TestFleetAttentionRPC(t *testing.T) {
	ctx := context.Background()
	reg, store := newTestRegistry(t)
	client := NewClient(&registryCaller{reg: reg})
	own := sessionScope("sess-1")

	empty, err := client.List(ctx, own, nil, false, nil, false)
	if err != nil {
		t.Fatalf("List (empty queue): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("List (empty queue) = %v, want empty", empty)
	}

	pushed, err := store.Push(ctx, AttentionItem{Kind: KindStall, SourceRef: "s1", ScopeRef: own})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	afterPush, err := client.List(ctx, own, nil, false, nil, false)
	if err != nil {
		t.Fatalf("List (after push): %v", err)
	}
	if len(afterPush) != 1 || afterPush[0].ID != pushed.ID {
		t.Fatalf("List (after push) = %+v, want exactly the pushed item", afterPush)
	}

	got, err := client.Get(ctx, pushed.ID, own, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != pushed.ID {
		t.Fatalf("Get() = %+v, want id=%s", got, pushed.ID)
	}

	acked, err := client.Ack(ctx, pushed.ID)
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if !acked.Acked() {
		t.Fatalf("Ack() result not acked: %+v", acked)
	}

	afterAck, err := client.List(ctx, own, nil, false, nil, false)
	if err != nil {
		t.Fatalf("List (after ack): %v", err)
	}
	if len(afterAck) != 0 {
		t.Fatalf("List (after ack) = %v, want empty (acked items excluded from default view)", afterAck)
	}
}

func TestFleetAttentionRPCListRequiresOwnScope(t *testing.T) {
	ctx := context.Background()
	reg, _ := newTestRegistry(t)
	handler := fleetAttentionListHandler(NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0), nil)
	reg.Register("test.attention.list.raw", handler)
	_, errObj := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: "test.attention.list.raw", Params: json.RawMessage(`{}`), ID: json.RawMessage(`1`)})
	if errObj == nil {
		t.Fatal("Dispatch with no own_scope = nil error, want a refusal")
	}
}

func TestFleetAttentionRPCUnknownFieldRejected(t *testing.T) {
	ctx := context.Background()
	reg, _ := newTestRegistry(t)
	_, errObj := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: MethodAck, Params: json.RawMessage(`{"id":"x","bogus":1}`), ID: json.RawMessage(`1`)})
	if errObj == nil {
		t.Fatal("Dispatch with an unknown field = nil error, want a refusal")
	}
}
