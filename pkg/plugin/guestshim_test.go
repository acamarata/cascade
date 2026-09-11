// Purpose: guest-shim envelope encode/decode tests, including the error
//
//	paths R-14.50's contract requires: malformed envelope and unknown
//	method.
//
// SPORT: pkg/plugin guest-invoke-shim tests (ADD) — P1-E15-W4-S33-T1.
package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

func TestAgentProviderMethod_Valid(t *testing.T) {
	valid := []plugin.AgentProviderMethod{
		plugin.MethodChat, plugin.MethodEmbed, plugin.MethodCount,
		plugin.MethodStream, plugin.MethodCapabilities,
	}
	for _, m := range valid {
		if !m.Valid() {
			t.Errorf("%v.Valid() = false, want true", m)
		}
	}
	if plugin.AgentProviderMethod("bogus").Valid() {
		t.Error(`AgentProviderMethod("bogus").Valid() = true, want false`)
	}
	if plugin.AgentProviderMethod("").Valid() {
		t.Error(`AgentProviderMethod("").Valid() = true, want false`)
	}
}

func TestAgentProviderMethod_String(t *testing.T) {
	if got := plugin.MethodChat.String(); got != "chat" {
		t.Errorf("MethodChat.String() = %q, want %q", got, "chat")
	}
}

func TestGuestDispatcher_DispatchSuccess(t *testing.T) {
	d := plugin.NewGuestDispatcher()
	d.Register(plugin.MethodChat, func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"reply":"hi"}`), nil
	})

	envelope, err := json.Marshal(plugin.InvokeEnvelope{Method: "chat", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	out := d.Dispatch(context.Background(), envelope)
	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Error != nil {
		t.Fatalf("Dispatch returned an error: %+v", res.Error)
	}
	if string(res.Result) != `{"reply":"hi"}` {
		t.Fatalf("Dispatch result = %s, want %s", res.Result, `{"reply":"hi"}`)
	}
}

func TestGuestDispatcher_MalformedEnvelope(t *testing.T) {
	d := plugin.NewGuestDispatcher()
	out := d.Dispatch(context.Background(), []byte(`not json`))

	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Error == nil || res.Error.Code != "malformed-envelope" {
		t.Fatalf("Dispatch(malformed) error = %+v, want code malformed-envelope", res.Error)
	}
}

func TestGuestDispatcher_UnknownMethod(t *testing.T) {
	d := plugin.NewGuestDispatcher()
	envelope, err := json.Marshal(plugin.InvokeEnvelope{Method: "nonexistent", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	out := d.Dispatch(context.Background(), envelope)
	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Error == nil || res.Error.Code != "unknown-method" {
		t.Fatalf("Dispatch(unregistered method) error = %+v, want code unknown-method", res.Error)
	}
}

func TestGuestDispatcher_HandlerError(t *testing.T) {
	d := plugin.NewGuestDispatcher()
	d.Register(plugin.MethodCount, func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("handler exploded")
	})

	envelope, err := json.Marshal(plugin.InvokeEnvelope{Method: "count", Params: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	out := d.Dispatch(context.Background(), envelope)
	var res plugin.InvokeResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.Error == nil || res.Error.Code != "handler-error" {
		t.Fatalf("Dispatch(handler error) error = %+v, want code handler-error", res.Error)
	}
}

func TestInvokeError_Error(t *testing.T) {
	e := &plugin.InvokeError{Code: "unknown-method", Message: "no such method"}
	if got, want := e.Error(), "unknown-method: no such method"; got != want {
		t.Errorf("InvokeError.Error() = %q, want %q", got, want)
	}
}
