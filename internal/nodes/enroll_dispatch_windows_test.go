//go:build windows

// Purpose: the Windows mirror of enroll_dispatch_unix_test.go - proves
//   node.enroll dispatched through the REAL rpc.ElevationMiddleware is
//   refused before ever reaching EnrollNode, because
//   platformElevationRefusal preempts the attestation flow entirely on
//   this platform (internal/rpc/elevation_windows.go).

package nodes

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

func TestRegisterHandlers_Windows_DispatchRefusesElevation(t *testing.T) {
	h := newTestHarness(t)
	p := h.validPayload(t, TierController)
	rawArgs, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	reg := rpc.NewRegistry()
	clock := runtime.NewFixedClock(time.Unix(5000, 0))
	trust := rpc.MapTrustStore{}
	ledger := rpc.NewNonceLedger(clock)
	reg.Use(rpc.ElevationMiddleware(ledger, trust, clock))
	RegisterHandlers(reg, h.deps, nil)

	result, errObj := reg.Dispatch(context.Background(), &rpc.Request{Method: "node.enroll", Params: rawArgs})
	if errObj == nil {
		t.Fatal("expected ELEVATION_REQUIRED on Windows: no local elevation helper exists")
	}
	if result != nil {
		t.Fatalf("result = %+v, want nil on refusal", result)
	}
	if _, ok := result.(DeviceRecord); ok {
		t.Fatal("EnrollNode must never run: the platform refusal precedes it")
	}
}
