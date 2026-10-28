package plugins

// Purpose (this file): the plugin.intent handler's wire contract: the
//   plugin's bytes come back unchanged under "result", bad params and a
//   missing dispatcher refuse before any plugin runs, a non-JSON plugin
//   result is refused, and dispatcher refusals pass through typed.
// SPORT: internal/plugins plugin.intent RPC (TEST) — P1-PLG-07.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func TestIntentRPCHandlerWrapsResultUnchanged(t *testing.T) {
	h := &countingIntentHandlers{out: []byte(`{"Resumed":false}`)}
	handler := IntentRPCHandler(NewIntentDispatcher(registryOf(intentReg("alpha", true, h, testIntent))))
	got, err := handler(context.Background(), json.RawMessage(`{"intent_name":"demo.install","input":{"intent":"x"}}`))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"result":{"Resumed":false}}` {
		t.Fatalf("wire result = %s", raw)
	}
	if h.lastInput.Load() != `{"intent":"x"}` || h.calls.Load() != 1 {
		t.Fatalf("plugin saw input %v (%d calls)", h.lastInput.Load(), h.calls.Load())
	}
}

func TestIntentRPCHandlerRefusals(t *testing.T) {
	h := &countingIntentHandlers{out: []byte(`not json`)}
	d := NewIntentDispatcher(registryOf(intentReg("alpha", true, h, testIntent)))
	cases := []struct {
		name    string
		d       *IntentDispatcher
		raw     string
		kind    cascade.Kind
		frag    string
		reaches bool
	}{
		{"nil dispatcher", nil, `{"intent_name":"demo.install"}`, cascade.KindUnavailable, "no intent dispatcher", false},
		{"unknown field", d, `{"intent_name":"demo.install","extra":1}`, cascade.KindInvalidInput, "decode params", false},
		{"malformed", d, `[`, cascade.KindInvalidInput, "decode params", false},
		{"unknown intent", d, `{"intent_name":"nope"}`, cascade.KindNotFound, "no enabled plugin declares it", false},
		{"non-JSON result", d, `{"intent_name":"demo.install","input":{}}`, cascade.KindInternal, "not JSON", true},
	}
	for _, tc := range cases {
		before := h.calls.Load()
		_, err := IntentRPCHandler(tc.d)(context.Background(), json.RawMessage(tc.raw))
		requireKindMsg(t, err, tc.kind, tc.frag)
		if reached := h.calls.Load() > before; reached != tc.reaches {
			t.Fatalf("%s: plugin reached = %v, want %v", tc.name, reached, tc.reaches)
		}
	}
}
