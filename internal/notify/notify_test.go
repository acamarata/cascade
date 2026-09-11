package notify

import (
	"testing"
	"time"
)

func TestPriorityOrdering(t *testing.T) {
	if PriorityUrgent >= PriorityHigh || PriorityHigh >= PriorityNormal || PriorityNormal >= PriorityLow {
		t.Fatalf("priority ordering broken: urgent=%d high=%d normal=%d low=%d",
			PriorityUrgent, PriorityHigh, PriorityNormal, PriorityLow)
	}
}

func TestPriorityStringAndValid(t *testing.T) {
	cases := []struct {
		p     Priority
		want  string
		valid bool
	}{
		{PriorityUrgent, "urgent", true},
		{PriorityHigh, "high", true},
		{PriorityNormal, "normal", true},
		{PriorityLow, "low", true},
		{Priority(99), "unknown", false},
	}
	for _, c := range cases {
		if got := c.p.String(); got != c.want {
			t.Errorf("Priority(%d).String() = %q, want %q", c.p, got, c.want)
		}
		if got := c.p.Valid(); got != c.valid {
			t.Errorf("Priority(%d).Valid() = %v, want %v", c.p, got, c.valid)
		}
	}
}

func TestClassResolveFailClosed(t *testing.T) {
	cases := []struct {
		in   Class
		want Class
	}{
		{ClassScoped, ClassScoped},
		{ClassAddressed, ClassAddressed},
		{ClassGlobalCritical, ClassGlobalCritical},
		{Class(""), ClassScoped},
		{Class("bogus"), ClassScoped},
	}
	for _, c := range cases {
		if got := c.in.Resolve(); got != c.want {
			t.Errorf("Class(%q).Resolve() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVisibilityResolveFailClosed(t *testing.T) {
	cases := []struct {
		in   Visibility
		want Visibility
	}{
		{VisibilityPrivate, VisibilityPrivate},
		{VisibilityScoped, VisibilityScoped},
		{VisibilityShared, VisibilityShared},
		{VisibilityExecutive, VisibilityExecutive},
		{Visibility(""), VisibilityPrivate},
		{Visibility("nope"), VisibilityPrivate},
	}
	for _, c := range cases {
		if got := c.in.Resolve(); got != c.want {
			t.Errorf("Visibility(%q).Resolve() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNotificationExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		name string
		n    Notification
		want bool
	}{
		{"zero never expires", Notification{}, false},
		{"future not expired", Notification{ExpiresAt: now.Add(time.Second)}, false},
		{"exact instant expired", Notification{ExpiresAt: now}, true},
		{"past expired", Notification{ExpiresAt: now.Add(-time.Second)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.n.expired(now); got != c.want {
				t.Errorf("expired() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNewServiceWiresCollaborators(t *testing.T) {
	svc := NewService(DefaultConfig(), fixedClock{now: time.Unix(0, 0)}, nil)
	if svc.Registry == nil || svc.Dispatcher == nil || svc.Inbox == nil || svc.Notifier == nil || svc.Router == nil {
		t.Fatalf("NewService left a nil collaborator: %+v", svc)
	}
}

// fixedClock is a minimal runtime.Clock test double, local to this
// package so every _test.go file can use it without importing
// internal/runtime's own FixedClock (kept trivial to avoid a needless
// cross-package test dependency for a one-method interface).
type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }
