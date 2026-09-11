package main

import (
	"encoding/json"
	"testing"
)

// TestDispatchKnownMethods confirms the stub's dispatch table produces a
// non-error result for each of the seven ABI v1 methods the wazero and
// process spike adapters exercise, and that HostStorage's "set" branch
// echoes the request value.
func TestDispatchKnownMethods(t *testing.T) {
	methods := []string{
		"HostHTTP", "HostStorage", "HostLog", "HostStream",
		"HostSecretRef", "HostEventEmit", "HostToolRegister",
	}
	for _, m := range methods {
		env := envelope{Method: m, Payload: json.RawMessage(`{}`)}
		r := dispatch(env)
		if r.Error != "" {
			t.Errorf("dispatch(%s) unexpected error: %s", m, r.Error)
		}
	}
}

func TestDispatchUnknownMethod(t *testing.T) {
	r := dispatch(envelope{Method: "NoSuchMethod", Payload: json.RawMessage(`{}`)})
	if r.Error == "" {
		t.Fatalf("dispatch(unknown method) should return an error result")
	}
}

func TestHandleStorageSetEchoesValue(t *testing.T) {
	r := handleStorage(json.RawMessage(`{"op":"set","key":"k","value":"v"}`))
	if r.Error != "" {
		t.Fatalf("handleStorage: %s", r.Error)
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(r.Payload, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Value != "v" {
		t.Fatalf("handleStorage(set) value = %q, want %q", out.Value, "v")
	}
}
