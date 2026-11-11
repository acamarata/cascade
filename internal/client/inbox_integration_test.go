//go:build integration

// Purpose: the inbox client methods through a real rpc.Registry over an
// in-memory HTTP transport; behind the integration tag because it imports
// net/http (Art.7.2). inbox_unit_test.go is the untagged half.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/inboxenv"
)

// TestClientInboxMethodsCallVerbs checks exact parameters and typed decoding.
func TestClientInboxMethodsCallVerbs(t *testing.T) {
	env := inboxenv.Envelope{ID: "notice", Class: "addressed", DataClass: "internal", Trust: "untrusted-source", Summary: "ready"}
	cases := []struct {
		verb, params string
		result       any
	}{
		{"inbox.list", `{"all":true,"unread":true}`, notify.InboxListResult{Items: []inboxenv.Envelope{env}, Total: 3, Unread: 2, Withheld: 1}},
		{"inbox.read", `{"id":"notice","offset":7}`, notify.InboxReadResult{Envelope: env, Heading: inboxenv.BlockHeading, Body: "body", Offset: 7, NextOffset: 11, TotalBytes: 11, Done: true}},
		{"inbox.ack", `{"id":"notice"}`, notify.InboxAckResult{ID: "notice", Acked: true}},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			reg, calls := rpc.NewRegistry(), 0
			reg.Register(tc.verb, func(_ context.Context, raw json.RawMessage) (any, error) {
				calls++
				assertInboxJSON(t, raw, tc.params)
				return tc.result, nil
			})
			c := inboxRegistryClient(t, reg)
			got, err := invokeInbox(c, tc.verb)
			if err != nil || !reflect.DeepEqual(got, tc.result) || calls != 1 {
				t.Fatalf("result = %+v, err = %v, calls = %d; want %+v, nil, 1", got, err, calls, tc.result)
			}
			reg.Register(tc.verb, func(_ context.Context, raw json.RawMessage) (any, error) {
				calls++
				assertInboxJSON(t, raw, tc.params)
				return nil, notify.ErrNotificationUnknown
			})
			_, err = invokeInbox(c, tc.verb)
			assertInboxWireError(t, err, tc.verb, notify.ErrNotificationUnknown)
			if calls != 2 {
				t.Fatalf("calls = %d, want 2", calls)
			}
		})
	}
}

func invokeInbox(c *Client, verb string) (any, error) {
	ctx := context.Background()
	switch verb {
	case "inbox.list":
		return c.InboxList(ctx, InboxListParams{All: true, Unread: true})
	case "inbox.read":
		return c.InboxRead(ctx, "notice", 7)
	default:
		return c.InboxAck(ctx, "notice")
	}
}

func assertInboxJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var a, b map[string]any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("params = %s, want %s", got, want)
	}
}

func assertInboxWireError(t *testing.T, err error, verb string, want error) {
	t.Helper()
	wire := cascade.NewRPCError(want)
	wantMessage := cascade.KindNotFound.String() + ": " + verb + ": " + wire.Message
	if !cascade.HasKind(err, cascade.KindNotFound) || err.Error() != wantMessage {
		t.Fatalf("err = %v, want KindNotFound and %q", err, wantMessage)
	}
}

// inboxRegistryTransport replaces only socket I/O, retaining production RPC
// parsing, dispatch, error serialization and client request/response handling.
type inboxRegistryTransport struct {
	t   *testing.T
	reg *rpc.Registry
}

func (s inboxRegistryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != rpc.RPCPath || r.Header.Get("Content-Type") != "application/json" {
		s.t.Fatalf("unexpected RPC request: %s %s", r.Method, r.URL.Path)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	req, parseErr := rpc.Parse(body)
	if parseErr != nil {
		s.t.Fatal(parseErr)
	}
	if err := rpc.SkewCheck(req.ClientVersion); err != nil {
		s.t.Fatal(err)
	}
	ctx := principal.WithCaller(r.Context(), principal.Caller{OwnerCLI: true})
	result, dispatchErr := s.reg.Dispatch(ctx, req)
	encoded, err := json.Marshal(rpc.NewEnvelope(req.ID, result, dispatchErr))
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded))}, nil
}

func inboxRegistryClient(t *testing.T, reg *rpc.Registry) *Client {
	t.Helper()
	dir := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, dir)
	}
	c := New(dir+"/daemon.sock", UnixDialer, time.Second)
	c.httpClient.Transport = inboxRegistryTransport{t: t, reg: reg}
	return c
}

// TestClientInboxWiring exercises the registered notification service and ack
// state through the production client codec and RPC registry.
func TestClientInboxWiring(t *testing.T) {
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	c := inboxRegistryClient(t, reg)
	svc := notify.NewService(notify.DefaultConfig(), runtime.NewFixedClock(time.Unix(1000, 0)), nil)
	if err := notify.RegisterInboxRPC(reg, svc); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	n := notify.Notification{ID: "notice", Class: notify.ClassAddressed, TargetPrincipal: notify.OwnerCLITarget,
		Visibility: notify.VisibilityPrivate, Priority: notify.PriorityNormal, Kind: "ready", Payload: []byte("prefix body")}
	if err := svc.Notifier.Deliver(ctx, n); err != nil {
		t.Fatal(err)
	}
	svc.Dispatcher.Drain(ctx)
	list, err := c.InboxList(ctx, InboxListParams{Unread: true})
	if err != nil || list.Total != 1 || list.Unread != 1 || len(list.Items) != 1 || list.Items[0].ID != n.ID {
		t.Fatalf("list = %+v, err = %v", list, err)
	}
	read, err := c.InboxRead(ctx, n.ID, 7)
	if err != nil || read.Envelope != list.Items[0] || read.Body != "body" || read.Offset != 7 || read.NextOffset != 11 || !read.Done || read.Heading != inboxenv.BlockHeading {
		t.Fatalf("read = %+v, err = %v", read, err)
	}
	ack, err := c.InboxAck(ctx, n.ID)
	if err != nil || !ack.Acked || ack.ID != n.ID {
		t.Fatalf("ack = %+v, err = %v", ack, err)
	}
	list, err = c.InboxList(ctx, InboxListParams{Unread: true})
	if err != nil || list.Total != 1 || list.Unread != 0 || len(list.Items) != 0 {
		t.Fatalf("after ack = %+v, err = %v", list, err)
	}
	_, err = c.InboxRead(ctx, "unknown", 0)
	assertInboxWireError(t, err, "inbox.read", notify.ErrNotificationUnknown)
	_, err = c.InboxAck(ctx, "unknown")
	assertInboxWireError(t, err, "inbox.ack", notify.ErrNotificationUnknown)
}
