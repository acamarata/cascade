package host

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeAuditSink records every AuditEvent delivered to it, in order.
// Shared by every _test.go file in this package.
type fakeAuditSink struct {
	events []AuditEvent
}

func (s *fakeAuditSink) LogHostCall(_ context.Context, event AuditEvent) {
	s.events = append(s.events, event)
}

func (s *fakeAuditSink) last() (AuditEvent, bool) {
	if len(s.events) == 0 {
		return AuditEvent{}, false
	}
	return s.events[len(s.events)-1], true
}

func TestNewHostBoundaryEnforcerRequiresPluginID(t *testing.T) {
	_, err := NewHostBoundaryEnforcer("", Grants{}, nil, nil, &fakeAuditSink{})
	if !errors.Is(err, ErrNoPluginID) {
		t.Fatalf("got %v, want ErrNoPluginID", err)
	}
}

func TestNewHostBoundaryEnforcerRequiresAuditSink(t *testing.T) {
	_, err := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, nil, nil)
	if !errors.Is(err, ErrNoAuditSink) {
		t.Fatalf("got %v, want ErrNoAuditSink", err)
	}
}

func TestNewHostBoundaryEnforcerNilPolicyAndBrokerAreValid(t *testing.T) {
	e, err := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, nil, &fakeAuditSink{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e == nil {
		t.Fatal("got nil enforcer with no error")
	}
}

func TestDenyRecordsAuditEntryAndWrapsCapabilityDeniedKind(t *testing.T) {
	sink := &fakeAuditSink{}
	e, err := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, nil, sink)
	if err != nil {
		t.Fatalf("build enforcer: %v", err)
	}
	reason := cascade.New(cascade.KindInvalidInput, "boom")
	got := e.Deny(context.Background(), "custom", reason)

	if !errors.Is(got, cascade.ErrCapabilityDenied) {
		t.Fatalf("Deny error kind = %v, want KindCapabilityDenied", got)
	}
	if !errors.Is(got, reason) {
		t.Fatalf("Deny's error does not wrap the original reason: %v", got)
	}

	entry, ok := sink.last()
	if !ok {
		t.Fatal("Deny recorded no audit entry")
	}
	if entry.PluginID != "plugin-a" || entry.CallType != "custom" || entry.Allowed {
		t.Fatalf("audit entry = %+v, want plugin-a/custom/denied", entry)
	}
	if entry.Reason != reason.Error() {
		t.Fatalf("audit reason = %q, want %q", entry.Reason, reason.Error())
	}
}

func TestDenyWithNilReasonStillRecordsAndReturnsNonBlank(t *testing.T) {
	sink := &fakeAuditSink{}
	e, _ := NewHostBoundaryEnforcer("plugin-a", Grants{}, nil, nil, sink)
	got := e.Deny(context.Background(), "custom", nil)
	if got == nil || got.Error() == "" {
		t.Fatalf("Deny(nil reason) = %v, want a non-blank error", got)
	}
	entry, ok := sink.last()
	if !ok || entry.Reason == "" {
		t.Fatalf("audit entry for a nil reason = %+v, want a non-blank Reason", entry)
	}
}

func TestGrantsZeroValueOwnsNoDomain(t *testing.T) {
	var g Grants
	if g.ownsDomain("anything") {
		t.Fatal("zero-value Grants must not own any domain")
	}
	if g.ownsDomain("") {
		t.Fatal("an empty domain must never be owned")
	}
}
