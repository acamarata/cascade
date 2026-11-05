//go:build !windows

// Purpose: registers the inbox send capabilities and installs the daemon's
// send authorizer on the one Notifier (P1-HRN-15).
// Inputs: w.Deps.Policy (the daemon policy engine and capability registry)
// and w.Deps.Notify (set by the notify registration, phaseSupervised Order 5).
// Outputs: capabilities inbox.send and inbox.cross_scope_send; the authorizer
// installed through Notifier.SetSendAuthorizer.
// Constraints: only an allow verdict lets a delivery proceed. An ask files
// its approval through the engine's approval queue and refuses
// KindPermissionDenied; an engine error refuses. A wiring without a policy
// engine or notify runtime installs nothing, so the Notifier keeps refusing
// cross-scope deliveries itself. No send RPC or MCP surface is added here.
// SPORT: cmd/cascade inbox send authorization wiring (P1-HRN-15).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// inboxSendCapability gates every Addressed or Scoped send on its
	// origin scope.
	inboxSendCapability = "inbox.send"
	// inboxCrossScopeSendCapability gates a send whose target scope differs
	// from its origin scope.
	inboxCrossScopeSendCapability = "inbox.cross_scope_send"
	// inboxSendServiceSubjectID is the daemon's own subject for sends made
	// without a caller principal.
	inboxSendServiceSubjectID = "daemon-notify"
)

var _ = registerDaemonWiring(daemonRegistration{
	Name: "inbox-send-auth", Phase: phaseSupervised, Order: 8,
	Wire: wireInboxSendAuth,
})

// inboxSendCapabilities is the capability pair this registration adds.
func inboxSendCapabilities() []policy.Capability {
	return []policy.Capability{
		{Name: inboxSendCapability, Desc: "send an inbox notification within its origin scope",
			DefaultPolicy: policy.ClassLocalDev},
		{Name: inboxCrossScopeSendCapability, Desc: "send an inbox notification to another scope",
			DefaultPolicy: policy.ClassExternalSideEffect},
	}
}

// wireInboxSendAuth registers the capabilities and installs the authorizer.
func wireInboxSendAuth(w *daemonWiring) error {
	pol, nr := w.Deps.Policy, w.Deps.Notify
	if pol == nil || pol.Engine == nil || pol.Registry == nil || nr == nil || nr.Service() == nil {
		return nil
	}
	for _, c := range inboxSendCapabilities() {
		if err := pol.Registry.Add(w.Ctx, c); err != nil {
			return cascade.Wrap(cascade.KindInternal, err, "inbox-send-auth: register capability")
		}
	}
	return nr.Service().Notifier.SetSendAuthorizer(newInboxSendAuthorizer(pol.Engine))
}

// inboxSendSubject is the caller principal in ctx, else the daemon service
// subject.
func inboxSendSubject(ctx context.Context) policy.Subject {
	if c, ok := principal.CallerFrom(ctx); ok && c.Principal != nil && c.Principal.ID != "" {
		return policy.Subject{Kind: policy.SubjectAgent, ID: principalSubjectPrefix + c.Principal.ID}
	}
	return policy.Subject{Kind: policy.SubjectAgent, ID: inboxSendServiceSubjectID}
}

// newInboxSendAuthorizer evaluates inbox.send on the origin scope and, for a
// cross-scope target, inbox.cross_scope_send. Only allow proceeds.
func newInboxSendAuthorizer(engine *policy.Engine) notify.SendAuthorizer {
	return func(ctx context.Context, n notify.Notification) error {
		subject := inboxSendSubject(ctx)
		if err := evalInboxSend(ctx, engine, subject, inboxSendCapability, n.OriginScope, n.OriginScope); err != nil {
			return err
		}
		if n.TargetScope != "" && n.TargetScope != n.OriginScope {
			return evalInboxSend(ctx, engine, subject, inboxCrossScopeSendCapability, n.OriginScope, n.TargetScope)
		}
		return nil
	}
}

// evalInboxSend runs one evaluation and maps every non-allow outcome to a
// refusal.
func evalInboxSend(ctx context.Context, engine *policy.Engine, subject policy.Subject, capability, origin, target string) error {
	out, err := engine.Evaluate(ctx, policy.EvalRequest{
		Subject: subject, Capability: capability, CommandLess: true,
		Attributes: map[string]string{"origin_scope": origin, "target_scope": target},
	})
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "inbox send: policy engine refused "+capability)
	}
	switch out.Verdict {
	case policy.VerdictAllow:
		return nil
	case policy.VerdictAsk:
		return cascade.Newf(cascade.KindPermissionDenied,
			"inbox send: %s requires approval (request %s)", capability, out.ApprovalRequestID)
	case policy.VerdictDeny:
		fallthrough
	default:
		return cascade.Newf(cascade.KindPermissionDenied, "inbox send: %s denied: %s", capability, out.Reason)
	}
}
