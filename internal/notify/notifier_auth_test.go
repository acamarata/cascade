package notify

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

func sendMethods(nf *Notifier) map[string]func(context.Context, Notification) error {
	return map[string]func(context.Context, Notification) error{
		"Deliver": nf.Deliver, "DeliverNow": nf.DeliverNow,
	}
}

func requireSendKind(t *testing.T, err error, want cascade.Kind) {
	t.Helper()
	if kind, ok := cascade.KindOf(err); !ok || kind != want {
		t.Fatalf("error = %v, want kind %v", err, want)
	}
}

func requireSendQueueEmpty(t *testing.T, nf *Notifier) {
	t.Helper()
	for _, ch := range nf.queues.queues {
		if len(ch) != 0 {
			t.Fatal("refused delivery reached queue")
		}
	}
}

func TestInboxCrossScopeSendRequiresCapability(t *testing.T) {
	for _, class := range []Class{ClassScoped, ClassAddressed} {
		for _, method := range []string{"Deliver", "DeliverNow"} {
			t.Run(string(class)+"/"+method, func(t *testing.T) {
				s, clock := newAddressedService(t)
				n := Notification{Class: class, OriginScope: "a", TargetScope: "b",
					TargetSession: "session", Priority: PriorityNormal}
				send := sendMethods(s.Notifier)[method]
				requireSendKind(t, send(context.Background(), n), cascade.KindUnavailable)
				requireSendQueueEmpty(t, s.Notifier)
				deny := cascade.New(cascade.KindPermissionDenied, "approval required")
				calls := 0
				allowed := false
				ctx := asPrincipal("caller", "session")
				if err := s.Notifier.SetSendAuthorizer(func(gotCtx context.Context, got Notification) error {
					calls++
					if gotCtx != ctx || got.OriginScope != "a" || got.TargetScope != "b" || !got.Timestamp.Equal(clock.Now()) {
						t.Fatal("authorizer did not receive prepared notification and caller context")
					}
					if !allowed {
						return deny
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				requireSendKind(t, s.Notifier.SetSendAuthorizer(nil), cascade.KindConflict)
				if err := send(ctx, n); err != deny {
					t.Fatalf("denied send = %v, want %v", err, deny)
				}
				requireSendQueueEmpty(t, s.Notifier)
				allowed = true
				if err := send(ctx, n); err != nil {
					t.Fatal(err)
				}
				if calls != 2 || len(s.Notifier.queues.queues[PriorityNormal]) != 1 {
					t.Fatalf("calls = %d; authorized send was not queued exactly once", calls)
				}
			})
		}
	}
}

func TestSameScopeSendAllowedByDefault(t *testing.T) {
	for _, class := range []Class{ClassScoped, ClassAddressed} {
		for _, method := range []string{"Deliver", "DeliverNow"} {
			t.Run(string(class)+"/"+method, func(t *testing.T) {
				s, _ := newAddressedService(t)
				n := Notification{Class: class, OriginScope: "a", TargetScope: "a",
					TargetSession: "session", Priority: PriorityNormal}
				send := sendMethods(s.Notifier)[method]
				if err := send(context.Background(), n); err != nil {
					t.Fatal(err)
				}
				if len(s.Notifier.queues.queues[PriorityNormal]) != 1 {
					t.Fatal("same-scope send was not queued")
				}
				<-s.Notifier.queues.queues[PriorityNormal]
				deny := cascade.New(cascade.KindPermissionDenied, "send denied")
				if err := s.Notifier.SetSendAuthorizer(func(context.Context, Notification) error { return deny }); err != nil {
					t.Fatal(err)
				}
				if err := send(context.Background(), n); err != deny {
					t.Fatalf("same-scope authorizer refusal = %v, want %v", err, deny)
				}
				requireSendQueueEmpty(t, s.Notifier)
			})
		}
	}
}

func TestAuthorizerEngineErrorRefuses(t *testing.T) {
	for _, method := range []string{"Deliver", "DeliverNow"} {
		t.Run(method, func(t *testing.T) {
			s, _ := newAddressedService(t)
			engineErr := errors.New("policy engine unavailable")
			if err := s.Notifier.SetSendAuthorizer(func(context.Context, Notification) error { return engineErr }); err != nil {
				t.Fatal(err)
			}
			for _, class := range []Class{ClassScoped, ClassAddressed} {
				for _, target := range []string{"a", "b"} {
					n := Notification{Class: class, OriginScope: "a", TargetScope: target, TargetSession: "session"}
					if err := sendMethods(s.Notifier)[method](context.Background(), n); err != engineErr {
						t.Fatalf("engine error = %v, want %v", err, engineErr)
					}
					requireSendQueueEmpty(t, s.Notifier)
				}
			}
		})
	}
}

func TestGlobalCriticalClosedSources(t *testing.T) {
	want := []string{"disk.nearly.full", "elevation.required", "node.offline", "provider.auth.expired"}
	if got := GlobalCriticalSources(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GlobalCriticalSources = %v, want %v", got, want)
	}
	sources := GlobalCriticalSources()
	sources[0] = "untrusted"
	if !reflect.DeepEqual(GlobalCriticalSources(), want) {
		t.Fatal("GlobalCriticalSources returned mutable shared state")
	}
	for _, method := range []string{"Deliver", "DeliverNow"} {
		t.Run(method, func(t *testing.T) {
			s, _ := newAddressedService(t)
			send := sendMethods(s.Notifier)[method]
			for _, source := range []string{"", "unknown", "backup.result", "supervision.stalled"} {
				err := send(context.Background(), Notification{Class: ClassGlobalCritical, Source: source})
				requireSendKind(t, err, cascade.KindInvalidInput)
				requireSendQueueEmpty(t, s.Notifier)
			}
			for _, source := range want {
				if err := send(context.Background(), Notification{Class: ClassGlobalCritical, Source: source, Priority: PriorityUrgent}); err != nil {
					t.Fatalf("closed source %s: %v", source, err)
				}
			}
			if len(s.Notifier.queues.queues[PriorityUrgent]) != len(want) {
				t.Fatal("closed sources were not all queued")
			}
		})
	}
}
