//go:build !windows

// Purpose: plugin.intent is served by the composed daemon registry, owner
// only: a session principal and an anonymous caller are refused before the
// handler runs, and the owner reaches the real dispatcher.
// SPORT: cmd/cascade daemon registrations (plugin.intent; TEST) — P1-PLG-07.
package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestPluginIntentMountedOnDaemon(t *testing.T) {
	t.Setenv("USERPROFILE", t.TempDir())
	c := composeForTest(t)
	if !slices.Contains(c.Registry.Methods(), "plugin.intent") {
		t.Fatal("plugin.intent is not in the composed daemon registry")
	}
	c.Registry.SetAccessPolicy(nil)
	params := json.RawMessage(`{"intent_name":"no.such.intent","input":{}}`)
	session := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{
		ID: "s", Kind: principal.KindSession, Scope: "project:test"}})
	for name, ctx := range map[string]context.Context{"session": session, "anonymous": context.Background()} {
		_, eo := c.Registry.Dispatch(ctx, &rpc.Request{Method: "plugin.intent", Params: params})
		if eo == nil || eo.Message != "caller not permitted" {
			t.Fatalf("%s caller: error = %+v, want the owner-only refusal", name, eo)
		}
	}
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	_, eo := c.Registry.Dispatch(owner, &rpc.Request{Method: "plugin.intent", Params: params})
	if eo == nil || eo.Message == "caller not permitted" {
		t.Fatalf("owner: error = %+v, want the dispatcher's not-found refusal", eo)
	}
	if eo.Code != cascade.RPCCodeNotFound || !strings.Contains(eo.Message, "no enabled plugin declares it") {
		t.Fatalf("owner: error = %+v, want the dispatcher's KindNotFound refusal", eo)
	}
}
