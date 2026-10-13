// Purpose: proves bridge.send's owner-only access class at the daemon's real
//
//	registry policy: a caller bearing a VALID minted principal (a session
//	token that resolves) is refused KindPermissionDenied and makes zero
//	transport calls, as is a request with no verified caller at all; the
//	owner's own caller, over the same mount, still delivers exactly one send,
//	so the refusal is the access class and not a broken fixture.
//
// SPORT: cmd/cascade plugin_rpc_bridge_access/TEST (ADD) — P1-CPA-01.
package main

import (
	"context"
	"encoding/json"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// dispatchAs calls bridge.send under ctx's caller identity.
func dispatchAs(ctx context.Context, t *testing.T, registry *rpc.Registry) (any, *rpc.ErrorObject) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	params, err := json.Marshal(plugins.BridgeSendParams{
		ThreadID: enabledBridgeThread, Content: "hello owner", Sensitivity: "internal",
	})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return registry.Dispatch(ctx, &rpc.Request{Method: plugins.MethodBridgeSend, Params: params})
}

func TestBridgeSendRefusesPrincipal(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows tier-2 refuses the bridge at its platform gate before bridge.send is armed")
	}
	env := newEnabledBridgeEnv(t)
	registry, _ := mountEnabledBridge(t, env)
	clock := runtime.NewFixedClock(time.Unix(100, 0))
	minter := principal.NewMinter(clock)
	token, err := minter.Mint(principal.Principal{ID: "test", Kind: principal.KindSession, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	registry.SetAccessPolicy(func(tok string) (principal.Principal, error) { return minter.Resolve(tok, clock.Now()) })
	resolved, err := minter.Resolve(token, clock.Now())
	if err != nil {
		t.Fatalf("a freshly minted token must resolve: %v", err)
	}
	denied := cascade.KindPermissionDenied.String()

	cases := map[string]context.Context{
		"valid principal":    principal.WithCaller(context.Background(), principal.Caller{Principal: &resolved}),
		"no verified caller": context.Background(),
	}
	for name, ctx := range cases {
		_, errObj := dispatchAs(ctx, t, registry)
		if errObj == nil {
			t.Fatalf("%s: bridge.send was dispatched, want KindPermissionDenied", name)
		}
		data, _ := errObj.Data.(map[string]string)
		if data["kind"] != denied {
			t.Fatalf("%s: error = %+v, want kind %s", name, errObj, denied)
		}
		if n := env.transport.sendCount(); n != 0 {
			t.Fatalf("%s: %d transport call(s) before the owner called, want 0", name, n)
		}
	}

	res, errObj := dispatchAs(principal.WithCaller(context.Background(), principal.Caller{}), t, registry)
	if errObj != nil {
		t.Fatalf("owner call refused: %+v", errObj)
	}
	if res == nil || env.transport.sendCount() != 1 {
		t.Fatalf("owner call: result %v, %d transport call(s), want exactly 1", res, env.transport.sendCount())
	}
}
