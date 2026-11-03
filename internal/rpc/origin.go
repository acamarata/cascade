// Purpose: origin marks the provenance of an RPC call for policy gates
// that must refuse certain actions when the caller is a tool (as opposed to
// a human-attended daemon session).

package rpc

import "context"

// Origin is a provenance tag attached to a request context by the middleware
// that accepted it. Values are compared by identity; the zero value is not
// OriginTool.
type Origin string

// OriginTool marks a request that entered the daemon through an MCP tool
// handler (a machine-to-machine caller). Certain elevated operations — in
// particular, rehydrating secrets into a child process — are refused under
// this origin, because the tool caller cannot be audited the way a terminal
// session can.
const OriginTool Origin = "tool"

// originKey is the unexported context key for Origin; no foreign
// context.WithValue can collide with or fake it.
type originKey struct{}

// WithOrigin returns a copy of ctx carrying o. Passing the zero Origin is
// permitted but OriginFrom will still report absent.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom returns the origin attached to ctx, or ("", false) when none is
// present or the value is the zero Origin.
func OriginFrom(ctx context.Context) (Origin, bool) {
	o, ok := ctx.Value(originKey{}).(Origin)
	if !ok || o == "" {
		return "", false
	}
	return o, true
}
