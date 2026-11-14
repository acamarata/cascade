//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

// TestLearnHandlersWiredInDaemon proves the learn-status registration
// mounts learn.status, learn.explain and learn.reset on the composed
// daemon registry, and that the mounted handlers are the real ones: an
// owner learn.status call answers with priors rows read from the daemon's
// own database, and a reset without a scope is refused.
func TestLearnHandlersWiredInDaemon(t *testing.T) {
	var found bool
	for _, r := range daemonRegistrations {
		if r.Name == "learn-status" {
			found = r.Phase == phaseFleet && r.Wire != nil
		}
	}
	if !found {
		t.Fatal("no learn-status registration in phaseFleet")
	}
	c := composeForTest(t)
	for _, m := range []string{"learn.status", "learn.explain", "learn.reset"} {
		if !c.Registry.Registered(m) {
			t.Errorf("composed registry does not serve %s", m)
		}
	}
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	res, eo := c.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: "learn.status", Params: json.RawMessage(`{}`)})
	if eo != nil {
		t.Fatalf("learn.status: %v", eo)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Scores []learnScoreRow `json:"scores"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Scores) == 0 || out.Scores[0].FallbackLevel != "priors" {
		t.Fatalf("learn.status on an empty database = %s, want priors rows", raw)
	}
	_, eo = c.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: "learn.reset", Params: json.RawMessage(`{}`)})
	if eo == nil {
		t.Fatal("learn.reset without scope or all was served; want an invalid-input refusal")
	}
}
