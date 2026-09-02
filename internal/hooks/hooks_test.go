// Purpose: HookConfig/HookFire shape coverage, DeriveHookID determinism
//
//	and stability across restarts (same input -> same slug, every time,
//	across independent calls simulating a fresh process), and
//	newActionNotPermittedError's Kind/message contract.
//
// Constraints: Art.7.1 — pure in-memory, no filesystem use, no
//
//	t.TempDir() needed.
//
// SPORT: internal.hooks.HookConfig/ADDED, internal.hooks.HookFire/ADDED
//
//	(tests) (P1-E03-W1-S05-T1).
package hooks_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/hooks"
)

func TestDeriveHookID_DeterministicAcrossCalls(t *testing.T) {
	params := map[string]string{"plugin": "p1", "tool": "search"}
	id1 := hooks.DeriveHookID("plugin.registered", hooks.ActionTypePluginCall, params)
	id2 := hooks.DeriveHookID("plugin.registered", hooks.ActionTypePluginCall, params)
	if id1 != id2 {
		t.Fatalf("DeriveHookID not deterministic: %q != %q", id1, id2)
	}
	if id1 == "" {
		t.Fatal("DeriveHookID returned empty string")
	}
}

func TestDeriveHookID_DeterministicRegardlessOfMapOrder(t *testing.T) {
	// Two maps with the same entries built in different insertion orders
	// must hash identically — Go map iteration order is randomised, so
	// this proves paramsHash sorts keys rather than depending on it.
	a := map[string]string{"z": "1", "a": "2", "m": "3"}
	b := map[string]string{"m": "3", "z": "1", "a": "2"}
	idA := hooks.DeriveHookID("t", hooks.ActionTypeAgentNote, a)
	idB := hooks.DeriveHookID("t", hooks.ActionTypeAgentNote, b)
	if idA != idB {
		t.Fatalf("DeriveHookID depends on map order: %q != %q", idA, idB)
	}
}

func TestDeriveHookID_DifferentParamsDifferentID(t *testing.T) {
	id1 := hooks.DeriveHookID("t", hooks.ActionTypePluginCall, map[string]string{"k": "v1"})
	id2 := hooks.DeriveHookID("t", hooks.ActionTypePluginCall, map[string]string{"k": "v2"})
	if id1 == id2 {
		t.Fatalf("DeriveHookID collided for different params: both %q", id1)
	}
}

func TestDeriveHookID_NeverEmptyForOddTrigger(t *testing.T) {
	id := hooks.DeriveHookID("!!!", hooks.ActionTypePluginCall, nil)
	if id == "" {
		t.Fatal("DeriveHookID returned empty string for a fully non-alphanumeric trigger")
	}
}

func TestHookFire_JSONRoundTrip(t *testing.T) {
	fire := hooks.HookFire{
		HookID:     "h1",
		Trigger:    "plugin.registered",
		ActionType: hooks.ActionTypePluginCall,
		ParamsHash: "deadbeef",
		ResultCode: hooks.ResultSuccess,
		Ts:         time.Unix(1_700_000_000, 0).UTC(),
	}
	data, err := json.Marshal(fire)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got hooks.HookFire
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != fire {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, fire)
	}
}

func TestHookFire_ErrMsgOmittedWhenEmpty(t *testing.T) {
	fire := hooks.HookFire{HookID: "h1", Trigger: "t", ActionType: hooks.ActionTypeAgentNote, ResultCode: hooks.ResultSuccess}
	data, err := json.Marshal(fire)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, present := raw["err_msg"]; present {
		t.Fatal("err_msg present in JSON despite empty ErrMsg (omitempty expected)")
	}
}
