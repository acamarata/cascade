//go:build !windows

// Purpose: the owner daemon and the node channel hold disjoint verb sets.
// The owner registry composeDaemon builds through buildRPCServer serves no
// node-originated verb, the node-facing registry serves exactly those, and
// no owner verb is reachable over a node tunnel, with or without the owner
// client marker.
// Inputs: a composed daemon registry and a controller node channel over
// in-memory tunnel pipes.
// Outputs: test failures naming the leaked verb or the reachable owner verb.
// Constraints: no socket opens; the tunnel far end is the production
// NodeFacing handler.

package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
)

// nodeFacingVerbs is every node-originated verb. Each is served on the
// node-facing registry and never on the owner registry; a later
// node-originated verb is added here.
var nodeFacingVerbs = []string{
	"node.channel.hello",
	"node.heartbeat",
	"node.dispatch.claim",
	"node.dispatch.report",
	"node.dispatch.journal",
}

// controllerChannel wires the node channel on a controller whose wiring
// carries owner as its owner registry, and returns it with one tunnel's far
// end.
func controllerChannel(t *testing.T, owner *rpc.Registry) (*channelWire, *channelListener) {
	t.Helper()
	c := newChannelWire(t)
	c.w.Registry = owner
	pinInstallationSigner(t)
	writeTestDesignation(t, c.dataDir)
	c.enroll(t, testChannelRoute)
	c.wire(t)
	if c.w.Deps.NodeFacing == nil {
		t.Fatalf("the controller composed no node-facing registry: %+v", c.status())
	}
	return c, c.listener(t)
}

// TestOwnerRegistryHasNoNodeOriginatedVerbs: the owner registry lists none of
// nodeFacingVerbs and no AccessNode verb at all, even after the node channel
// is wired over the same wiring; the node-facing registry lists exactly
// nodeFacingVerbs.
func TestOwnerRegistryHasNoNodeOriginatedVerbs(t *testing.T) {
	owner := composeForTest(t).Registry
	c, _ := controllerChannel(t, owner)
	methods := owner.Methods()
	if len(methods) == 0 {
		t.Fatal("the composed owner registry lists no methods")
	}
	for _, m := range methods {
		if slices.Contains(nodeFacingVerbs, m) {
			t.Errorf("owner registry serves node-originated verb %s", m)
		}
		if access, ok := rpc.AccessOf(m); ok && access == rpc.AccessNode {
			t.Errorf("owner registry serves %s, declared AccessNode", m)
		}
	}
	want := slices.Clone(nodeFacingVerbs)
	slices.Sort(want)
	if got := c.w.Deps.NodeFacing.Registry().Methods(); !slices.Equal(got, want) {
		t.Fatalf("node-facing registry lists %v, want exactly %v", got, want)
	}
}

// TestOwnerMarkerOnChannelRefusedForEveryOwnerVerb: every production owner
// verb sent over a node tunnel is method-not-found without a header and HTTP
// 400 with the owner client marker, and no owner handler ever runs.
func TestOwnerMarkerOnChannelRefusedForEveryOwnerVerb(t *testing.T) {
	owner := composeForTest(t).Registry
	var calls atomic.Int64
	owner.Use(func(_ string, next rpc.HandlerFunc) rpc.HandlerFunc {
		return func(ctx context.Context, p json.RawMessage) (any, error) {
			calls.Add(1)
			return next(ctx, p)
		}
	})
	methods, open := owner.Methods(), 0
	for _, m := range methods {
		if access, ok := rpc.AccessOf(m); ok && access == rpc.AccessOpen {
			open++
		}
	}
	if len(methods) == 0 || open == 0 {
		t.Fatalf("owner methods %d, open %d; the sweep needs both above zero", len(methods), open)
	}
	_, l := controllerChannel(t, owner)
	for _, m := range methods {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + m + `","params":{}}`
		status, raw := l.exchange(t, "", body)
		assertChannelMethodNotFound(t, m, status, raw)
		status, raw = l.exchange(t, principal.HeaderOwnerCLI+": cli\r\n", body)
		if status != 400 || !strings.Contains(raw, "carries no caller identity header") {
			t.Errorf("%s with the owner marker over the channel: %d %s, want the node-facing 400", m, status, raw)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("owner handlers ran %d times for calls that arrived over a node tunnel", n)
	}
}

// assertChannelMethodNotFound checks a tunnel reply is HTTP 200 carrying the
// JSON-RPC method-not-found error.
func assertChannelMethodNotFound(t *testing.T, method string, status int, raw string) {
	t.Helper()
	var env struct {
		Error *rpc.ErrorObject `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil || status != 200 || env.Error == nil || env.Error.Code != -32601 {
		t.Errorf("%s over the channel: %d %s (%v), want method-not-found (-32601)", method, status, raw, err)
	}
}
