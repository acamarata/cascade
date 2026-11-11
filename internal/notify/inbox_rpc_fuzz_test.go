package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// FuzzInboxRPCParams exercises the registered inbox handlers through dispatch.
// Params may be arbitrary bytes; handler failures must retain a frozen kind.
func FuzzInboxRPCParams(f *testing.F) {
	for _, raw := range []string{
		``, `null`, `[]`, `{`, `{} {}`, `{"id":5}`, `{"id":""}`,
		`{"id":"bad id"}`, `{"id":"x\u0000"}`, `{"id":"x","offset":-1}`,
		`{"all":true}`, `{"all":true,"scope":"other"}`,
		`{"id":"n1","all":true}`, `{"id":"n1","offset":1}`,
		`{"id":"n1","offset":9223372036854775807}`,
		`{"id":"n1","offset":"conflict"}`, `{"unread":{}}`,
		`{"id":"n1"}`, `{"id":"unknown"}`,
		`{"id":"` + strings.Repeat("x", 257) + `"}`,
	} {
		f.Add([]byte(raw))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		reg, ctx := inboxFuzzRegistry(t)
		for _, method := range []string{"inbox.list", "inbox.read", "inbox.ack"} {
			got, err := reg.Dispatch(ctx, &rpc.Request{Method: method, Params: data})
			if err == nil {
				assertInboxFuzzResult(t, method, got)
				if method == "inbox.ack" {
					assertInboxFuzzWireKind(ctx, t, reg, method, string(data), cascade.KindConflict)
				}
			}
		}
	})
}

// inboxFuzzRegistry creates fresh state and checks errors before wire conversion.
func inboxFuzzRegistry(t *testing.T) (*rpc.Registry, context.Context) {
	t.Helper()
	svc, ctx := inboxRPCFixture(t)
	n := addressedFixture("n1", "p")
	n.Payload = []byte("inbox fuzz body")
	recordInternal(svc, n)
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	if err := RegisterInboxRPC(reg, svc); err != nil {
		t.Fatal(err)
	}
	reg.Use(func(method string, next rpc.HandlerFunc) rpc.HandlerFunc {
		return func(ctx context.Context, raw json.RawMessage) (any, error) {
			result, err := next(ctx, raw)
			if err != nil {
				assertInboxFuzzError(t, method, err)
			}
			return result, err
		}
	})
	return reg, ctx
}

func assertInboxFuzzError(t *testing.T, method string, err error) {
	t.Helper()
	kind, ok := cascade.KindOf(err)
	if !ok || !kind.Valid() {
		t.Fatalf("%s returned an untyped error: %v", method, err)
	}
	sentinels := map[cascade.Kind]error{
		cascade.KindNotFound: ErrNotificationUnknown,
		cascade.KindConflict: ErrNotificationAlreadyAcked,
	}
	if want, ok := sentinels[kind]; ok {
		assertInboxSentinel(t, err, want)
		return
	}
	if kind != cascade.KindInvalidInput && kind != cascade.KindPermissionDenied {
		t.Fatalf("%s returned unexpected error: %v", method, err)
	}
}

func assertInboxFuzzResult(t *testing.T, method string, got any) {
	t.Helper()
	switch method {
	case "inbox.list":
		out, ok := got.(InboxListResult)
		if !ok || out.Total != 1 || len(out.Items) != 1 || out.Items[0].ID != "n1" {
			t.Fatalf("list did not reach the populated inbox: %#v", got)
		}
	case "inbox.read":
		out, ok := got.(InboxReadResult)
		if !ok || out.Envelope.ID != "n1" || out.TotalBytes != len("inbox fuzz body") {
			t.Fatalf("read did not reach the stored body: %#v", got)
		}
	case "inbox.ack":
		out, ok := got.(InboxAckResult)
		if !ok || !out.Acked || out.ID != "n1" {
			t.Fatalf("ack did not reach the stored item: %#v", got)
		}
	}
}

func TestInboxRPCParamsTypedErrors(t *testing.T) {
	reg, ctx := inboxFuzzRegistry(t)
	for method, raw := range map[string]string{
		"inbox.list": `{}`, "inbox.read": `{"id":"n1","offset":0}`, "inbox.ack": `{"id":"n1"}`,
	} {
		got, err := reg.Dispatch(ctx, &rpc.Request{Method: method, Params: json.RawMessage(raw)})
		if err != nil {
			t.Fatalf("valid %s: %v", method, err)
		}
		assertInboxFuzzResult(t, method, got)
	}
	for _, method := range []string{"inbox.list", "inbox.read", "inbox.ack"} {
		for _, raw := range []string{
			`null`, `[]`, `{`, `{} {}`, `{"scope":"other"}`,
			`{"id":5}`, `{"unread":{},"id":"n1"}`,
			`{"all":true,"id":"n1"}`, `{"id":"n1","offset":"bad"}`,
		} {
			assertInboxFuzzWireKind(ctx, t, reg, method, raw, cascade.KindInvalidInput)
		}
	}
	for _, method := range []string{"inbox.read", "inbox.ack"} {
		for _, id := range []string{"", "bad id", "bad\x00id", "bad\r\nid", strings.Repeat("x", 257)} {
			raw, err := json.Marshal(InboxAckParams{ID: id})
			if err != nil {
				t.Fatal(err)
			}
			assertInboxFuzzWireKind(ctx, t, reg, method, string(raw), cascade.KindInvalidInput)
		}
		assertInboxFuzzWireKind(ctx, t, reg, method, `{"id":"unknown"}`, cascade.KindNotFound)
	}
	assertInboxFuzzWireKind(ctx, t, reg, "inbox.list", `{"all":true}`, cascade.KindPermissionDenied)
	assertInboxFuzzWireKind(ctx, t, reg, "inbox.read", `{"id":"n1","offset":-1}`, cascade.KindInvalidInput)
	assertInboxFuzzWireKind(ctx, t, reg, "inbox.read", `{"id":"n1","offset":100}`, cascade.KindInvalidInput)
}

func assertInboxFuzzWireKind(ctx context.Context, t *testing.T, reg *rpc.Registry, method, raw string, want cascade.Kind) {
	t.Helper()
	_, err := reg.Dispatch(ctx, &rpc.Request{Method: method, Params: json.RawMessage(raw)})
	if err == nil || err.Code != want.JSONRPCCode() || err.Message == "" {
		t.Fatalf("%s params %q: want %s, got %v", method, raw, want, err)
	}
}
