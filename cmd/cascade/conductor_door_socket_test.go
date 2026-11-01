//go:build !windows

// Purpose: the in-process conductor door has no socket face. The composed
//
//	daemon's registry serves conductor.execute but no conductor.embed or any
//	other embedding verb, so no client can declare an embedding tier; the
//	same composition binds the process door to its Executor.
//
// SPORT: cmd/cascade/daemon:conductor-door-inprocess (tests, P1-CPA-10).
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/rpc"
)

func TestConductorEmbedNotOnSocket(t *testing.T) {
	deps := policyDeps(t, "")
	var reg *rpc.Registry
	_, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { reg = r })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	if reg == nil {
		t.Fatal("composeDaemon handed back no registry")
	}
	methods := reg.Methods()
	served := false
	for _, m := range methods {
		if m == daemon.ConductorExecuteMethod {
			served = true
		}
		if m == "conductor.embed" || (strings.HasPrefix(m, "conductor.") && strings.Contains(m, "embed")) ||
			strings.Contains(m, "door") {
			t.Errorf("the daemon socket serves %q; the conductor door is in-process only", m)
		}
	}
	if !served {
		t.Fatalf("registry of %d methods lacks %s: the method list is not the composed daemon's", len(methods), daemon.ConductorExecuteMethod)
	}
	if err := daemon.ProcessConductorDoor().Ready(); err != nil {
		t.Errorf("after composition the process door is not bound to the daemon's Executor: %v", err)
	}
}
