// Purpose: the MCP response firewall. A response leaving this daemon for
//
//	a harness connection is an OUTBOUND crossing, and until this file
//	existed it was the one registered egress class with no code routing
//	through the engine: the class was declared, the marshal was not.
//	Every transport marshals through ResponseMarshaler.Marshal, so the
//	substitution pass sees the bytes before the wire does.
//
// Inputs: a *Response and the process's egress interceptor.
// Outputs: the substituted JSON bytes, or an error and nothing to write.
// Constraints: fail closed. A marshaler that could not acquire its
//
//	capability, a refused class and a substitution failure all return an
//	error, and the transport writes nothing. No clock, no randomness.
//
// SPORT: MCP_RESPONSE_FIREWALL: ADD (internal/mcp response-marshal egress routing).
package mcp

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

// responseTier is the sensitivity an MCP response is declared at. It is
// internal, not public: the bytes go to a harness process on this machine
// under the operator's own account. It is not restricted either, because
// declaring it so would make the class refuse every response (the class
// is registered without AllowRestricted) and a firewall that refuses
// everything is indistinguishable from one that is working.
const responseTier = egress.TierInternal

// ResponseInterceptor is the egress seam, satisfied by *egress.Engine. It
// is declared here rather than imported as a concrete type so a test can
// drive the marshal path with a recording interceptor, and so nothing in
// this package depends on how the engine is built.
type ResponseInterceptor interface {
	Intercept(ctx context.Context, token egress.Capability, tier egress.SensitivityTier,
		content []byte) ([]byte, error)
}

// ResponseMarshaler encodes a Response and passes it through the egress
// firewall. The zero value is not usable; build one with
// NewResponseMarshaler or NewDefaultResponseMarshaler.
type ResponseMarshaler struct {
	firewall ResponseInterceptor
	token    egress.Capability
}

// NewResponseMarshaler binds firewall to the mcp.response class. The
// capability is acquired once, here, so a build in which that class was
// disabled or never registered fails at construction rather than on the
// first response.
func NewResponseMarshaler(firewall ResponseInterceptor) (*ResponseMarshaler, error) {
	if firewall == nil {
		return nil, cascade.New(cascade.KindInvalidInput,
			"mcp: a response marshaler needs an egress interceptor")
	}
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassMCP)
	if err != nil {
		return nil, err
	}
	return &ResponseMarshaler{firewall: firewall, token: token}, nil
}

// NewDefaultResponseMarshaler builds the marshaler a transport uses when
// the composition root has not bound one.
//
// STATED GAP, deliberately not hidden: the engine it builds has no vault
// bound, so the substitution pass's exact-value half matches nothing and
// only the detector half runs. That is strictly more than the nothing
// this path had before, and it is less than a vault-bound engine gives.
// Binding the process vault is the composition root's job and belongs to
// whoever mounts the egress engine; it is not something this package can
// do for itself without inventing a secret store.
func NewDefaultResponseMarshaler() (*ResponseMarshaler, error) {
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, err
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), unboundVault{}, detector)
	if err != nil {
		return nil, err
	}
	return NewResponseMarshaler(engine)
}

// Marshal encodes resp and returns the bytes the transport may write.
//
// It never returns partially filtered bytes: a substitution failure, a
// refused class and an unusable marshaler all return an error with no
// content, and the transport's contract is to write nothing when this
// returns one.
func (m *ResponseMarshaler) Marshal(ctx context.Context, resp *Response) ([]byte, error) {
	if m == nil || m.firewall == nil {
		return nil, cascade.New(cascade.KindUnavailable,
			"mcp: no response firewall configured; refusing to write an unfiltered response")
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "mcp: encoding response")
	}
	return m.firewall.Intercept(ctx, m.token, responseTier, raw)
}

// unboundVault is the value source of a process that has bound no vault:
// it holds nothing and says so. It is not a stand-in for a vault that
// exists elsewhere, and it never answers a lookup with a fabricated
// value.
type unboundVault struct{}

// List reports an empty vault.
func (unboundVault) List(context.Context) ([]string, error) { return nil, nil }

// Get refuses: an unbound vault has no entries, and reporting one would
// be an invention.
func (unboundVault) Get(_ context.Context, name string) ([]byte, error) {
	return nil, cascade.Newf(cascade.KindNotFound, "mcp: no vault is bound; %q cannot be read", name)
}
