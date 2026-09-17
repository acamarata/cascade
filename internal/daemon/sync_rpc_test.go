package daemon

// Purpose (this file): proves RegisterSyncHandlers actually mounts the
//   four sync.* methods, and that it does so WITHOUT a store — the
//   divergence from this package's other registerXHandler functions, and
//   the one a future tidy-up would otherwise "fix" back into a silent
//   disappearance of `sync.status`.
// SPORT: internal/daemon sync.* (ADD tests) — P1-E17-W4-S38-T3.

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	syncpkg "github.com/acamarata/cascade/internal/sync"
)

// syncMethods is what a daemon must answer for after this registration.
var syncMethods = []string{
	syncpkg.MethodStatus, syncpkg.MethodRun,
	syncpkg.MethodConflictsList, syncpkg.MethodConflictsResolve,
}

// dispatchSync sends one real frame and reports whether the method was
// mounted at all, separately from whether the call succeeded.
func dispatchSync(t *testing.T, registry *rpc.Registry, method string) bool {
	t.Helper()
	req, errObj := rpc.Parse([]byte(`{"jsonrpc":"2.0","method":"` + method + `","params":{},"id":1}`))
	if errObj != nil {
		t.Fatalf("Parse(%s): %+v", method, errObj)
	}
	_, callErr := registry.Dispatch(context.Background(), req)
	if callErr == nil {
		return true
	}
	msg := strings.ToLower(callErr.Message)
	return !strings.Contains(msg, "method") || !strings.Contains(msg, "not")
}

// TestRegisterSyncHandlersMountsEveryMethod is the reachability proof: a
// verb that is never mounted is engine nobody can reach.
func TestRegisterSyncHandlersMountsEveryMethod(t *testing.T) {
	registry := rpc.NewRegistry()
	RegisterSyncHandlers(registry, nil, runtime.NewSystemClock(), syncpkg.Config{})
	for _, method := range syncMethods {
		if !dispatchSync(t, registry, method) {
			t.Errorf("%s is not mounted", method)
		}
	}
}

// TestSyncStatusAnswersWithNoStoreOpen is the divergence this file exists
// for. Every other registerXHandler here registers nothing when the store
// is nil; sync must not, because the conflict journal is readable without
// one and the positions report themselves as unread.
func TestSyncStatusAnswersWithNoStoreOpen(t *testing.T) {
	registry := rpc.NewRegistry()
	RegisterSyncHandlers(registry, nil, runtime.NewSystemClock(), syncpkg.Config{})
	req, errObj := rpc.Parse([]byte(`{"jsonrpc":"2.0","method":"` + syncpkg.MethodStatus + `","params":{},"id":1}`))
	if errObj != nil {
		t.Fatalf("Parse: %+v", errObj)
	}
	res, callErr := registry.Dispatch(context.Background(), req)
	if callErr != nil {
		t.Fatalf("sync.status with no store: %+v", callErr)
	}
	status, ok := res.(syncpkg.StatusResult)
	if !ok {
		t.Fatalf("sync.status returned %T, want a StatusResult", res)
	}
	if len(status.Domains) == 0 {
		t.Fatal("sync.status listed no domains")
	}
	for _, d := range status.Domains {
		if d.PositionKnown {
			t.Errorf("%s/%s reported a known position with no store open; "+
				"zero would read as \"never synced\"", d.Domain, d.Subkind)
		}
	}
}

// TestTheTransportGateDoesNotRefuse proves the daemon's gate lets an
// already-attested resolution through. A nil gate refuses by design, so a
// daemon wired with one would refuse every properly authorised request
// after the middleware had already verified it.
func TestTheTransportGateDoesNotRefuse(t *testing.T) {
	if err := (transportElevationGate{}).Authorize(context.Background(), syncpkg.ElevatedVerbResolve); err != nil {
		t.Fatalf("the transport gate refused an already-elevated verb: %v", err)
	}
}
