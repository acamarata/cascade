// Package host implements the capability-grant checkpoint every
// process-tier plugin host function transits before it runs: per-plugin
// net scopes (CheckHTTP, http.go), storage domains (CheckStorage,
// storage.go), vault-broker-routed secret references (CheckSecretRef,
// secret.go), and the general policy-engine lookup (CheckGeneric,
// policy.go). Deny and the audit helper below are shared by all four.
//
// Purpose: BoundaryEnforcer, constructed here.
// Inputs: the plugin's loaded Grants, and the PolicyEngine / SecretBroker
//
//	/ AuditSink seams a composition root binds to the real collaborators.
//
// Outputs: nil on an allowed call; a cascade.ErrCapabilityDenied-kind
//
//	error, with an audit entry recorded, on any refusal.
//
// Constraints: fail CLOSED — an unknown, malformed, missing, or empty
//
//	grant denies, never defaults to allow (12-QUALITY-CONSTITUTION Art.1,
//	Art.3). golangci's plugins-providers-boundary depguard rule forbids
//	ANY non-test file under internal/plugins/** — this package included,
//	since internal/plugins/host is itself under internal/plugins/** —
//	from importing internal/** at all (.golangci.yml, matches
//	internal/plugins/process/types.go's own documented reading of the
//	same rule). So PolicyEngine and SecretBroker below are this
//	package's OWN local seams, not internal/policy.Engine or
//	internal/secrets.Broker themselves: a composition root outside
//	internal/plugins/** (e.g. internal/daemon) adapts the real
//	collaborators to these interfaces and hands a *HostBoundaryEnforcer
//	to internal/plugins/process through ITS OWN local seam (see
//	runtime.go's HostCapabilityChecker) — the same two-hop pattern T3
//	already established for EgressRegistrar/EgressInterceptor. See the
//	ticket journal for the full contradiction this resolves, both sides
//	quoted.
//
// SPORT: internal/plugins/host capability-boundary (ADD) — P1-E15-W4-S31-T4.
package host

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Grants is the process-runtime-local view of one plugin's loaded
// capability grants: the fields HostBoundaryEnforcer needs to decide a
// call, parsed from the manifest by C/S-05.T6's loader upstream of this
// package. The zero value denies everything — a nil NetScopes/
// StorageDomains and a false CrossDomain is the fail-closed default, not
// a caller error.
type Grants struct {
	// NetScopes are the plugin's declared outbound HTTP scopes: a bare
	// hostname (exact match) or "*.example.com" (subdomain match). An
	// empty slice denies every host.
	NetScopes []string
	// StorageDomains are the plugin's own declared storage domains.
	// Access to a domain in this set is same-domain and always allowed;
	// access to any other domain is cross-domain and additionally needs
	// CrossDomain.
	StorageDomains map[string]bool
	// CrossDomain reports whether the plugin holds the explicit
	// cross-domain storage capability. False (the zero value) denies
	// every domain outside StorageDomains.
	CrossDomain bool
}

// ownsDomain reports whether domain is in g's own declared set. A nil map
// and an empty string both report false, which is the fail-closed
// default for an unset or malformed domain.
func (g Grants) ownsDomain(domain string) bool {
	if domain == "" {
		return false
	}
	return g.StorageDomains[domain]
}

// AuditEvent is one recorded host-boundary decision: which plugin, which
// call, whether it was allowed, and why. 06-FORGE-SPEC §5.21 and this
// ticket's acceptance criteria require one of these for every denied call
// and every granted secret-ref access.
type AuditEvent struct {
	// PluginID identifies the plugin the call came from.
	PluginID string
	// CallType names the check: "http", "storage", "secret_ref", or the
	// capability string CheckGeneric evaluated.
	CallType string
	// Allowed is true for a granted call, false for a denial.
	Allowed bool
	// Reason is the denial reason, or a short note for an allowed
	// secret-ref access. Never the raw secret value or the raw URL/
	// domain that would over-share on a redacted audit surface; callers
	// pass a description, not user content.
	Reason string
}

// AuditSink is the seam onto wherever host-boundary decisions are
// recorded. A composition root adapts it to the real audit log. Required:
// NewHostBoundaryEnforcer refuses a nil sink, because an enforcer that
// cannot record what it denied would make the audit-trail acceptance
// criterion unverifiable in production.
type AuditSink interface {
	LogHostCall(ctx context.Context, event AuditEvent)
}

// ErrNoAuditSink reports a HostBoundaryEnforcer constructed with a nil
// AuditSink. Every denial and every secret-ref grant must be recorded
// (06-FORGE-SPEC §5.21); an enforcer that cannot record one is a
// misconfiguration, not a silent no-op.
var ErrNoAuditSink = cascade.New(cascade.KindInvalidInput, "host: a HostBoundaryEnforcer needs an AuditSink")

// ErrNoPluginID reports a HostBoundaryEnforcer constructed with an empty
// plugin identifier. Every audit entry must name the plugin it came from.
var ErrNoPluginID = cascade.New(cascade.KindInvalidInput, "host: a HostBoundaryEnforcer needs a non-empty plugin id")

// HostBoundaryEnforcer is the capability-grant checkpoint for one
// launched plugin's host function calls. The zero value is not usable;
// build one with NewHostBoundaryEnforcer.
//
// every caller spell this "HostBoundaryEnforcer" (05-PEWS-PLAN-W4-W6.md
// §Epic O S-31.T4, 06-FORGE-SPEC §5.10/§5.21) — internal/plugins/process.
// ProcessRuntime sets the precedent for this exemption in this same repo.
//
//nolint:revive // the stutter is deliberate: the ticket contract names this type by this exact name throughout
type HostBoundaryEnforcer struct {
	pluginID string
	grants   Grants
	policy   PolicyEngine
	broker   SecretBroker
	audit    AuditSink
}

// NewHostBoundaryEnforcer builds an enforcer for pluginID with grants,
// checked against policy and broker, recording every decision to audit.
//
// policy and broker may be nil: a plugin that never calls host_secret_ref
// needs no SecretBroker, and one that declares no generic capabilities
// needs no PolicyEngine. CheckSecretRef and CheckGeneric each fail closed
// on their own nil collaborator (secret.go, policy.go) rather than
// requiring every enforcer to wire both. pluginID and audit are always
// required: an unnamed or unrecorded decision defeats the audit-trail
// acceptance criterion outright.
func NewHostBoundaryEnforcer(pluginID string, grants Grants, policy PolicyEngine, broker SecretBroker, audit AuditSink) (*HostBoundaryEnforcer, error) {
	if pluginID == "" {
		return nil, ErrNoPluginID
	}
	if audit == nil {
		return nil, ErrNoAuditSink
	}
	return &HostBoundaryEnforcer{pluginID: pluginID, grants: grants, policy: policy, broker: broker, audit: audit}, nil
}

// Deny builds the fail-closed refusal for callType, records an audit
// entry (plugin id, call type, reason), and returns a
// cascade.ErrCapabilityDenied-kind error wrapping reason. Every Check*
// method in this package routes its refusal through Deny, so "every
// denied call produces an audit log entry" holds by construction rather
// than by each check remembering to log separately.
//
// A nil reason is a caller defect, not a permissive default: it is
// replaced with a generic "capability not granted" so the audit entry and
// returned error are never blank.
func (e *HostBoundaryEnforcer) Deny(ctx context.Context, callType string, reason error) error {
	if reason == nil {
		reason = cascade.New(cascade.KindCapabilityDenied, "capability not granted")
	}
	e.record(ctx, callType, false, reason.Error())
	return cascade.Wrapf(cascade.KindCapabilityDenied, reason, "host: plugin %q: %s denied", e.pluginID, callType)
}

// allow records a granted call's audit entry. Only CheckSecretRef calls
// this today (the acceptance criterion names secret-ref grants
// specifically); CheckHTTP/CheckStorage/CheckGeneric's successful paths
// are the common case and are not separately audited, matching
// 06-FORGE-SPEC §5.21's "every granted secret-ref access is also logged"
// (it does not ask this of every allowed call).
func (e *HostBoundaryEnforcer) allow(ctx context.Context, callType, note string) {
	e.record(ctx, callType, true, note)
}

// record delivers one AuditEvent to e's sink.
func (e *HostBoundaryEnforcer) record(ctx context.Context, callType string, allowed bool, reason string) {
	e.audit.LogHostCall(ctx, AuditEvent{PluginID: e.pluginID, CallType: callType, Allowed: allowed, Reason: reason})
}
