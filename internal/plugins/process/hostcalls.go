//go:build !windows

// Purpose: dispatches a launched plugin's host-ABI notifications through
//
//	the injected capability boundary before anything else happens with
//	them.
//
// Inputs: the Notification frames a plugin's Transport delivers on its
//
//	Notifications channel.
//
// Outputs: none directly — every decision is a CapabilityChecker call,
//
//	which is responsible for its own audit trail (internal/plugins/host).
//
// Constraints: a nil CapabilityChecker means the composition root has not
//
//	wired the boundary; calls are ignored, never treated as allowed. See
//	consumeHostCalls's doc comment for the transport-layer gap this
//	works within (P1-E15-W4-S31-T4's journal quotes it in full).
//
// SPORT: internal/plugins/process host-call-dispatch (ADD) — P1-E15-W4-S31-T4.

package process

import (
	"context"
	"encoding/json"
	"strings"
)

// HostCapabilityChecker is the local seam onto the plugin capability
// boundary (internal/plugins/host.HostBoundaryEnforcer). This package
// cannot import internal/plugins/host directly: both sit under
// internal/plugins/**, and golangci's plugins-providers-boundary depguard
// rule forbids any internal/** import from a non-test file there (see
// types.go's doc comment on the identical EgressRegistrar pattern this
// mirrors). A composition root outside internal/plugins/** wraps a real
// *host.HostBoundaryEnforcer in a small adapter satisfying this
// interface and assigns it to ProcessRuntime.CapabilityChecker.
//
// CheckSecretRef returns `any` rather than host.SecretHandle: this
// package has no plugin-facing response path to forward the handle
// through yet (see consumeHostCalls), so it only needs the check to run
// and audit, never the handle's value.
type HostCapabilityChecker interface {
	CheckHTTP(ctx context.Context, url string) error
	CheckStorage(ctx context.Context, domain string) error
	CheckSecretRef(ctx context.Context, key string) (any, error)
	CheckGeneric(ctx context.Context, capability string) error
}

// Host-ABI notification method names (05-PEWS-PLAN-W4-W6.md §Epic O
// preamble). hostMethodStoragePrefix covers every host_storage_* verb.
const (
	hostMethodHTTPRequest = "host_http_request"
	hostMethodSecretRef   = "host_secret_ref"
	hostMethodStoragePref = "host_storage_"
)

// hostGenericCapability maps every other capability-gated host-ABI
// method to the capability string CheckGeneric evaluates. host_log
// carries no capability of its own in the preamble's list and is
// deliberately absent: log output is not policy-gated.
var hostGenericCapability = map[string]string{
	"host_stream_emit":   "host.stream_emit",
	"host_event_emit":    "host.event_emit",
	"host_tool_register": "host.tool_register",
}

// consumeHostCalls drains h's plugin-initiated notifications for the
// life of its current transport and checks every recognized host-ABI
// call against rt.CapabilityChecker before anything else happens with
// it.
//
// It is the process runtime's ONLY plugin-to-host call path today:
// transport.go's routeFrame correlates a plugin REQUEST's id with the
// host's own outstanding Call, not the reverse, so a plugin-initiated
// JSON-RPC request (id set, expecting a reply) is decoded as a
// Notification with its id silently discarded, and no response ever
// reaches the plugin. Returning a value or an error to the plugin for a
// request-shaped host call needs its own id-correlation half inside
// Transport, and transport.go is not in this ticket's files_scope change
// list. This loop enforces the capability boundary on the one channel
// that reaches the host today; the ticket journal quotes both sides of
// this contradiction.
func (rt *ProcessRuntime) consumeHostCalls(ctx context.Context, h *Handle) {
	h.mu.RLock()
	t := h.transport
	h.mu.RUnlock()
	for n := range t.Notifications() {
		rt.checkHostCall(ctx, n)
	}
}

// checkHostCall routes one plugin notification to the matching
// HostCapabilityChecker method. A nil CapabilityChecker means no
// composition root has wired the boundary yet; the call is ignored
// rather than panicking — but unlike Audit's documented nil-is-valid
// convention on ProcessRuntime, production wiring of CapabilityChecker
// is a hard requirement of this ticket's acceptance criteria, not an
// optional collaborator.
func (rt *ProcessRuntime) checkHostCall(ctx context.Context, n Notification) {
	if rt.CapabilityChecker == nil {
		return
	}
	switch {
	case n.Method == hostMethodHTTPRequest:
		_ = rt.CapabilityChecker.CheckHTTP(ctx, decodeHostParam(n.Params, "url"))
	case n.Method == hostMethodSecretRef:
		_, _ = rt.CapabilityChecker.CheckSecretRef(ctx, decodeHostParam(n.Params, "key"))
	case strings.HasPrefix(n.Method, hostMethodStoragePref):
		_ = rt.CapabilityChecker.CheckStorage(ctx, decodeHostParam(n.Params, "domain"))
	default:
		if capability, known := hostGenericCapability[n.Method]; known {
			_ = rt.CapabilityChecker.CheckGeneric(ctx, capability)
		}
	}
}

// decodeHostParam reads one string field from a notification's JSON
// params. A decode failure or a missing field returns "", which every
// HostCapabilityChecker method treats as a fail-closed denial (an empty
// URL/domain/key never matches a declared grant).
func decodeHostParam(raw json.RawMessage, field string) string {
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	return m[field]
}
