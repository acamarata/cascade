package mcp

// Purpose: the capability filter that decides which tools an MCP client
//   is allowed to SEE (P1-E16-W4-S34-T2, R-14.251 rule 2). It replaces the
//   one-entry knownSafeGrants placeholder registry.go shipped with, whose
//   own comment said a capability-policy engine was expected to replace it
//   wholesale rather than extend it ad hoc.
// Inputs: a tool's declared required capability, and the process's policy
//   engine expressed as the CapabilityFilter seam.
// Outputs: exposed, or not.
// Constraints: fail-closed on every axis, exactly as the table it
//   replaces was. No filter wired, an unregistered capability, a verdict
//   short of allow, or an evaluation error all mean NOT exposed. A
//   filtered tool is absent from the manifest with a debug log; no error
//   reaches the client, because an MCP client that could distinguish
//   "denied" from "does not exist" would learn that a privileged tool
//   exists.
// SPORT: internal/mcp capability filter (ADD) — P1-E16-W4-S34-T2.

import "context"

// CapabilityFilter reports whether the caller may see a tool that needs
// the named capability.
//
// It is an interface rather than a direct dependency on internal/policy
// for one reason that matters: this package is imported by the MCP server
// and by every test that builds a tool registry, and threading a whole
// policy engine (a capability registry, a grant store and an autonomy
// controller) through all of them to ask one yes/no question would make
// the filter harder to exercise than the thing it guards. The production
// implementation is coretools.PolicyFilter, which asks
// (*policy.Engine).Evaluate — the ONE policy entry point (R-21.236).
type CapabilityFilter interface {
	// Allow reports whether capability is granted. An implementation that
	// cannot decide must return false; there is no error return, because
	// there is no answer other than "not exposed" that this caller could
	// act on.
	Allow(ctx context.Context, capability string) bool
}

// DenyAllFilter is the filter a registry built without one uses.
//
// It exists so "no filter was wired" is a stated state with a name rather
// than a nil check whose meaning a reader has to infer, and so the
// fail-closed default is the one a caller gets by omission.
type DenyAllFilter struct{}

// Allow always reports false.
func (DenyAllFilter) Allow(context.Context, string) bool { return false }

// AllowAllFilter exposes every tool regardless of capability. It is the
// filter the registry uses for tools that declare NO required capability
// — the pre-existing plugin path, whose grants are checked by isExposable
// instead — and tests use it to exercise dispatch without standing up a
// policy engine. It is never the default: a tool that names a capability
// and finds no filter is not exposed.
type AllowAllFilter struct{}

// Allow always reports true.
func (AllowAllFilter) Allow(context.Context, string) bool { return true }
