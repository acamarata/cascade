// Purpose: relay-origin context propagation for the conductor router.
//   When a lane dispatch relays work back into the router, it stamps a
//   RelayOrigin on the context naming the origin dispatch, lane and model
//   so the router can restrict the re-entry to exactly that lane and model
//   (P1-NODE-06a). A request without a relay origin in context is an
//   ordinary request and takes the unmodified path.
// Inputs: none at the type layer; context propagation only.
// Outputs: RelayOrigin struct, context writers and readers.
// Constraints: no provider or registry import; this file carries only the
//   context-keyed value and the accessors. The filter that enforces the
//   restriction lives in filters_relay.go.
// SPORT: conductor.relay-origin/ADD (P1-NODE-06a).

package conductor

import "context"

// RelayOrigin is the stamped triple a relayed dispatch puts on the
// context: the origin dispatch id, the lane it came from and the model it
// resolved to. The router reads this to refuse any candidate that is not
// the origin lane or resolves to a different model.
type RelayOrigin struct {
	// DispatchID is the origin dispatch correlation id.
	DispatchID string
	// LaneID is the origin lane name the relay came from.
	LaneID string
	// Model is the origin model name the relay resolved to.
	Model string
}

// relayOriginKey is the unexported context key type. Using a distinct
// named type (rather than string) guarantees no other package can collide
// with it even if it guesses the key name.
type relayOriginKey struct{}

// WithRelayOrigin returns ctx stamped with o. A zero-value RelayOrigin
// still stamps the context; RelayOriginFrom distinguishes the
// no-origin-stamped case by its second return value.
func WithRelayOrigin(ctx context.Context, o RelayOrigin) context.Context {
	return context.WithValue(ctx, relayOriginKey{}, o)
}

// RelayOriginFrom returns the RelayOrigin stamped on ctx by WithRelayOrigin
// and true. When ctx carries no relay origin the second return is false
// and the first is the zero value.
func RelayOriginFrom(ctx context.Context) (RelayOrigin, bool) {
	v, ok := ctx.Value(relayOriginKey{}).(RelayOrigin)
	return v, ok
}
