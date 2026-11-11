package notify

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestInboxRPCAccessOwnerView(t *testing.T) {
	svc, _ := inboxRPCFixture(t)
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(nil)
	if err := RegisterInboxRPC(reg, svc); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"inbox.list", "inbox.read", "inbox.ack"} {
		params := json.RawMessage(`{"id":"unknown"}`)
		if method == "inbox.list" {
			params = json.RawMessage(`{}`)
		}
		for _, caller := range []principal.Caller{{OwnerCLI: true}, {Principal: &principal.Principal{ID: "p"}}} {
			ctx := principal.WithCaller(context.Background(), caller)
			_, err := reg.Dispatch(ctx, &rpc.Request{Method: method, Params: params})
			if method == "inbox.list" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Code != cascade.NewRPCError(ErrNotificationUnknown).Code || err.Message != cascade.NewRPCError(ErrNotificationUnknown).Message {
				t.Fatalf("%s: want not found, got %v", method, err)
			}
		}
		_, err := reg.Dispatch(context.Background(), &rpc.Request{Method: method})
		if err == nil || err.Code != cascade.NewRPCError(cascade.ErrPermissionDenied).Code {
			t.Fatalf("anonymous %s: %v", method, err)
		}
	}
}

func inboxRPCFixture(t *testing.T) (*Service, context.Context) {
	t.Helper()
	d := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, d)
	}
	s := NewService(DefaultConfig(), runtime.NewFixedClock(time.Unix(1000, 0)), nil)
	ctx := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{
		ID: "p", SessionID: "session", Scope: "project:test", Kind: principal.KindSession,
	}})
	return s, ctx
}

func ownerInboxContext() context.Context {
	return principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
}

func addressedFixture(id, target string) Notification {
	return Notification{ID: id, Class: ClassAddressed, TargetPrincipal: target, Visibility: VisibilityPrivate}
}

func recordInternal(s *Service, n Notification) {
	n.Origin = Origin{Source: "test.inbox", RecordRef: n.ID, Tier: provider.SensitivityInternal,
		Trust: provider.ProvenanceUntrustedSource}
	stamp(&n)
	s.Inbox.record(n)
}

func listInbox(ctx context.Context, t *testing.T, s *Service, params string) InboxListResult {
	t.Helper()
	got, err := s.inboxList(ctx, json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	return got.(InboxListResult)
}

func readInbox(ctx context.Context, t *testing.T, s *Service, id string, offset int) InboxReadResult {
	t.Helper()
	params, err := json.Marshal(InboxReadParams{ID: id, Offset: offset})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.inboxRead(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	return got.(InboxReadResult)
}

func assertInboxSentinel(t *testing.T, got, want error) {
	t.Helper()
	if got != want || got == nil || got.Error() != want.Error() {
		t.Fatalf("got %v (%p), want %v (%p)", got, got, want, want)
	}
}

func recordCeilingFixtures(s *Service, target string) {
	for _, id := range []string{"internal", "restricted", "local"} {
		n := addressedFixture(id+"-"+target, target)
		if id != "local" {
			n.Origin = Origin{Source: "test.inbox", RecordRef: n.ID, Tier: provider.SensitivityInternal,
				Trust: provider.ProvenanceUntrustedSource}
			if id == "restricted" {
				n.Origin.Tier = provider.SensitivityRestricted
			}
		}
		stamp(&n)
		s.Inbox.record(n)
	}
}

func TestInboxReadLocalOnlyNotFoundForPrincipal(t *testing.T) {
	s, ctx := inboxRPCFixture(t)
	recordCeilingFixtures(s, "p")
	recordCeilingFixtures(s, OwnerCLITarget)
	got := listInbox(ctx, t, s, `{}`)
	if len(got.Items) != 1 || got.Items[0].ID != "internal-p" || got.Withheld != 2 {
		t.Fatalf("principal: %+v", got)
	}
	for _, id := range []string{"local-p", "restricted-p", "unknown"} {
		params, _ := json.Marshal(InboxAckParams{ID: id})
		_, err := s.inboxRead(ctx, params)
		assertInboxSentinel(t, err, ErrNotificationUnknown)
		_, err = s.inboxAck(ctx, params)
		assertInboxSentinel(t, err, ErrNotificationUnknown)
	}
	for _, id := range []string{"local-owner-cli", "restricted-owner-cli"} {
		got := readInbox(ownerInboxContext(), t, s, id, 0)
		if got.Envelope.ID != id || got.Envelope.DataClass == "internal" {
			t.Fatal(got)
		}
	}
}

func TestInboxOwnerModelCallerKeepsCeiling(t *testing.T) {
	s, _ := inboxRPCFixture(t)
	recordCeilingFixtures(s, OwnerCLITarget)
	ctx := principal.WithCaller(context.Background(), principal.Caller{})
	for _, params := range []string{`{}`, `{"all":true}`} {
		got := listInbox(ctx, t, s, params)
		if len(got.Items) != 1 || got.Items[0].ID != "internal-owner-cli" || got.Withheld != 2 {
			t.Fatalf("owner model: %+v", got)
		}
	}
	for _, id := range []string{"local-owner-cli", "restricted-owner-cli"} {
		params, _ := json.Marshal(InboxAckParams{ID: id})
		_, err := s.inboxRead(ctx, params)
		assertInboxSentinel(t, err, ErrNotificationUnknown)
		_, err = s.inboxAck(ctx, params)
		assertInboxSentinel(t, err, ErrNotificationUnknown)
		page := readInbox(ownerInboxContext(), t, s, id, 0)
		if page.Envelope.ID != id {
			t.Fatal(page)
		}
	}
}
