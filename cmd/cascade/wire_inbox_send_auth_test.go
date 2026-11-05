//go:build !windows

package main

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/pkg/cascade"
)

// inboxSendWiring builds a wiring over a real policy engine and notify
// service, as composeDaemon hands the registration.
func inboxSendWiring(t *testing.T) (*daemonWiring, *notify.Service) {
	t.Helper()
	f := newPolicyEngineFixture(t)
	parts, err := newPolicyEngine(context.Background(), policyEngineInputs{
		Store: f.store, Clock: f.clock, Paths: f.paths,
	})
	if err != nil {
		t.Fatalf("newPolicyEngine: %v", err)
	}
	svc := notify.NewService(notify.DefaultConfig(), f.clock, nil)
	w := &daemonWiring{Ctx: context.Background(), Deps: daemonRuntime{
		Policy: &policyWiring{policyEngineParts: parts},
		Notify: daemon.NewNotifyRuntime(svc),
	}}
	return w, svc
}

// TestInboxSendAuthWiring: the registration installs an authorizer that
// refuses a cross-scope Scoped delivery when no grant confers
// inbox.cross_scope_send.
func TestInboxSendAuthWiring(t *testing.T) {
	w, svc := inboxSendWiring(t)
	if err := wireInboxSendAuth(w); err != nil {
		t.Fatalf("wireInboxSendAuth: %v", err)
	}
	for _, name := range []string{inboxSendCapability, inboxCrossScopeSendCapability} {
		if _, err := w.Deps.Policy.Registry.Lookup(context.Background(), name); err != nil {
			t.Fatalf("capability %q not registered: %v", name, err)
		}
	}
	n := notify.Notification{Class: notify.ClassScoped, OriginScope: "a", TargetScope: "b",
		TargetSession: "session", Priority: notify.PriorityNormal}
	for name, send := range map[string]func(context.Context, notify.Notification) error{
		"Deliver": svc.Notifier.Deliver, "DeliverNow": svc.Notifier.DeliverNow,
	} {
		err := send(context.Background(), n)
		if err == nil {
			t.Fatalf("%s: cross-scope delivery without the capability was accepted", name)
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPermissionDenied {
			t.Fatalf("%s: error = %v, want KindPermissionDenied", name, err)
		}
	}
	if err := svc.Notifier.SetSendAuthorizer(func(context.Context, notify.Notification) error { return nil }); err == nil {
		t.Fatal("the wiring left the authorizer replaceable")
	}
}

// TestSetSendAuthorizerOnce: a second SetSendAuthorizer call is a conflict.
func TestSetSendAuthorizerOnce(t *testing.T) {
	_, svc := inboxSendWiring(t)
	allow := func(context.Context, notify.Notification) error { return nil }
	if err := svc.Notifier.SetSendAuthorizer(allow); err != nil {
		t.Fatalf("first SetSendAuthorizer: %v", err)
	}
	err := svc.Notifier.SetSendAuthorizer(allow)
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindConflict {
		t.Fatalf("second SetSendAuthorizer error = %v, want KindConflict", err)
	}
}
