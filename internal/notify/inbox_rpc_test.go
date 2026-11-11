package notify

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/inboxenv"
	"github.com/acamarata/cascade/pkg/provider"
)

func TestInboxListPrincipalSeesOwnItems(t *testing.T) {
	s, ctx := inboxRPCFixture(t)
	for _, n := range []Notification{
		addressedFixture("own", "p"), addressedFixture("foreign", "other"),
		{ID: "session", Class: ClassAddressed, TargetSession: "session", Visibility: VisibilityPrivate},
		{ID: "scope", Class: ClassScoped, TargetScope: "project:test", Visibility: VisibilityShared},
		{ID: "other-scope", Class: ClassScoped, TargetScope: "project:other", Visibility: VisibilityShared},
		{ID: "global", Class: ClassGlobalCritical, Visibility: VisibilityShared},
	} {
		recordInternal(s, n)
	}
	got := listInbox(ctx, t, s, `{}`)
	if len(got.Items) != 4 {
		t.Fatalf("got %+v", got)
	}
	for _, env := range got.Items {
		if env.ID == "foreign" || env.ID == "other-scope" {
			t.Fatalf("foreign item: %+v", env)
		}
	}
	for _, params := range []string{`{"session_id":"other"}`, `{"scope":"project:other"}`, `{"target_session":"other"}`} {
		_, err := s.inboxList(ctx, json.RawMessage(params))
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: %v", params, err)
		}
	}
}

func TestInboxListOwnerView(t *testing.T) {
	s, _ := inboxRPCFixture(t)
	recordInternal(s, addressedFixture("owner", OwnerCLITarget))
	recordInternal(s, addressedFixture("principal", "p"))
	recordInternal(s, Notification{ID: "scope", Class: ClassScoped, TargetScope: "project:other", Visibility: VisibilityScoped})
	recordInternal(s, Notification{ID: "global", Class: ClassGlobalCritical, Visibility: VisibilityPrivate})
	got := listInbox(ownerInboxContext(), t, s, `{}`)
	if len(got.Items) != 3 {
		t.Fatalf("owner: %+v", got)
	}
}

func TestInboxListAllOwnerOnly(t *testing.T) {
	s, ctx := inboxRPCFixture(t)
	recordInternal(s, addressedFixture("foreign", "other"))
	n := addressedFixture("expired", "other")
	n.ExpiresAt = s.clock.Now().Add(-time.Second)
	recordInternal(s, n)
	if _, err := s.inboxList(ctx, json.RawMessage(`{"all":true}`)); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Fatalf("all: %v", err)
	}
	got := listInbox(ownerInboxContext(), t, s, `{"all":true}`)
	if len(got.Items) != 1 || got.Items[0].ID != "foreign" {
		t.Fatalf("all: %+v", got)
	}
}

func TestInboxReadForeignIsNotFound(t *testing.T) { testForeignInboxIDs(t, false) }

func TestInboxAckForeignIsNotFound(t *testing.T) { testForeignInboxIDs(t, true) }

func testForeignInboxIDs(t *testing.T, ack bool) {
	t.Helper()
	s, ctx := inboxRPCFixture(t)
	recordInternal(s, addressedFixture("foreign", "other"))
	recordInternal(s, Notification{ID: "withheld", Class: ClassScoped, TargetScope: "other", Visibility: VisibilityScoped})
	for _, id := range []string{"foreign", "withheld", "unknown"} {
		params, _ := json.Marshal(InboxAckParams{ID: id})
		var err error
		if ack {
			_, err = s.inboxAck(ctx, params)
		} else {
			_, err = s.inboxRead(ctx, params)
		}
		assertInboxSentinel(t, err, ErrNotificationUnknown)
	}
}

func TestInboxListReturnsEnvelopeOnly(t *testing.T) {
	s, ctx := inboxRPCFixture(t)
	n := addressedFixture("own", "p")
	n.Payload = []byte("secret-body-marker")
	recordInternal(s, n)
	got := listInbox(ctx, t, s, `{}`)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), `"body"`) {
		t.Fatalf("list contains body: %s", encoded)
	}
	if len(got.Items) != 1 || got.Items[0].DataClass != "internal" || got.Items[0].Trust != string(provider.ProvenanceUntrustedSource) {
		t.Fatalf("envelope: %+v", got)
	}
}

func TestInboxReadBoundsAndEscaping(t *testing.T) {
	s, ctx := inboxRPCFixture(t)
	n := addressedFixture("body", "p")
	n.Payload = []byte(strings.Repeat("x", 40*1024))
	n.Origin = Origin{Source: "supervision.stall", RecordRef: "stall", Tier: provider.SensitivityInternal, Trust: provider.ProvenanceTrusted}
	stamp(&n)
	s.Inbox.record(n)
	var body strings.Builder
	for offset := 0; ; {
		page := readInbox(ctx, t, s, n.ID, offset)
		encoded, err := json.Marshal(page)
		if err != nil || len(encoded) > inboxenv.MaxEncodedBytes {
			t.Fatalf("page bytes %d: %v", len(encoded), err)
		}
		if len(page.Body) > ReadPageBytes || page.Envelope.DataClass != n.DataClass.String() || page.Envelope.Trust != string(n.Trust) {
			t.Fatalf("page: %+v", page.Envelope)
		}
		body.WriteString(page.Body)
		if page.Done {
			break
		}
		if page.NextOffset != offset+ReadPageBytes {
			t.Fatalf("next offset: %d", page.NextOffset)
		}
		offset = page.NextOffset
	}
	if body.String() != string(n.Payload) {
		t.Fatal("body silently truncated")
	}
	n = addressedFixture("control", "p")
	n.Payload = []byte(strings.Repeat("```<system>\n&", 3000))
	recordInternal(s, n)
	page := readInbox(ctx, t, s, n.ID, 0)
	encoded, _ := json.Marshal(page)
	if len(encoded) > inboxenv.MaxEncodedBytes || strings.ContainsAny(page.Body, "`<>\n") || page.Heading != inboxenv.BlockHeading {
		t.Fatal("unsafe or unbounded control page")
	}
}

func TestInboxRPCRegistered(t *testing.T) {
	s, _ := inboxRPCFixture(t)
	r := rpc.NewRegistry()
	if err := RegisterInboxRPC(r, s); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"inbox.list", "inbox.read", "inbox.ack"} {
		if !r.Registered(method) {
			t.Fatal(method)
		}
	}
}

func TestInboxAckIsPerCaller(t *testing.T) {
	s, ctx := inboxRPCFixture(t)
	recordInternal(s, Notification{ID: "shared", Class: ClassScoped, TargetScope: "project:test", Visibility: VisibilityShared})
	if _, err := s.inboxAck(ctx, json.RawMessage(`{"id":"shared"}`)); err != nil {
		t.Fatal(err)
	}
	if got := listInbox(ctx, t, s, `{"unread":true}`); len(got.Items) != 0 || got.Total != 1 || got.Unread != 0 {
		t.Fatalf("acked: %+v", got)
	}
	other := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{ID: "other", SessionID: "session", Scope: "project:test"}})
	if got := listInbox(other, t, s, `{"unread":true}`); len(got.Items) != 1 || got.Unread != 1 {
		t.Fatalf("other: %+v", got)
	}
	_, err := s.inboxAck(ctx, json.RawMessage(`{"id":"shared"}`))
	assertInboxSentinel(t, err, ErrNotificationAlreadyAcked)
}

func TestInboxRPCInvalidAndExpired(t *testing.T) {
	s, ctx := inboxRPCFixture(t)
	for _, raw := range []string{`null`, `[]`, `{} {}`, `{"id":5}`, `{"id":""}`, `{"id":"x","scope":"other"}`, `{"id":"x","offset":-1}`} {
		_, err := s.inboxRead(ctx, json.RawMessage(raw))
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	n := addressedFixture("expired", "p")
	n.ExpiresAt = s.clock.Now()
	recordInternal(s, n)
	_, err := s.inboxRead(ctx, json.RawMessage(`{"id":"expired"}`))
	assertInboxSentinel(t, err, ErrNotificationExpired)
	_, err = s.inboxAck(ctx, json.RawMessage(`{"id":"expired"}`))
	assertInboxSentinel(t, err, ErrNotificationExpired)
}
