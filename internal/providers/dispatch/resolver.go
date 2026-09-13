// Package dispatch implements the production conductor.ProviderResolver
// (see DEFECT-conductor-execute-permanently-unavailable.md): a thin
// adapter turning a Router's provider.Selection into a live
// provider.ModelProvider, built from the durable providers registry
// (internal/providers/registry). This closes the construction-time half
// of the gap the daemon composition root's nil resolver argument left
// open (R-16.80): internal/conductor.NewExecutor no longer fails closed
// on a nil Resolver once this type is wired in.
//
// Purpose (this file): the Resolver type, its constructor and Resolve
// itself - registry lookup, driver/auth-mode validation and the
// credential-boundary fail-closed check. build.go holds the per-
// DriverKind construction switch; providers/transport holds the real
// net/http transport (moved out of this package per
// FIX-transport-tier-relocation.md: providers/transport is TierPlugins,
// already a normative egress-allowlist member, and this package now
// carries no net/http import of its own).
//
// Inputs: a RegistryLookup (the lane/provider read view an already-open
//
//	*registry.Registry satisfies structurally) and, per call, a
//	provider.Selection naming which lane the router picked.
//
// Outputs: a live provider.ModelProvider (the correct providers/*
//
//	driver, matched to the registry's DriverKind, its BaseURL and
//	Selection.Model wired as the driver's default), or a taxonomy error.
//
// Constraints: never reads, invents, hardcodes or defaults a credential.
//
//	Credential resolution is an injected seam (CredentialSource) the
//	composition root must supply; a nil CredentialSource is a fail-closed
//	KindUnavailable naming the exact missing vault-key reference, never a
//	fabricated success (Art.1). OAuth-authenticated providers are a
//	second, separately disclosed gap: no OAuthBroker seam is wired at
//	this composition root either, so an oauth-mode provider record fails
//	closed with KindUnsupported rather than being silently attempted.
//
// SPORT: internal/providers/dispatch (ADD, DEFECT-conductor-execute-
//
//	permanently-unavailable.md).
package dispatch

import (
	"context"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
	"github.com/acamarata/cascade/providers/transport"
)

// CredentialSource dereferences a vault-key name to its current secret
// value. Its shape matches every providers/* driver's own locally
// declared KeyResolver exactly (structural satisfaction, no adapter
// needed): a value passed here also satisfies anthropic.KeyResolver,
// gemini.KeyResolver, ollama.KeyResolver and openai.KeyResolver directly.
//
// No production implementation is wired in at the daemon composition
// root (cmd/cascade/daemon_unix_conductor.go passes nil): connecting this
// seam to internal/secrets.Broker requires deciding how a headless daemon
// obtains vault access without an interactive elevation prompt, which is
// an owner decision this package does not make. Until that seam is
// supplied, Resolve fails closed for every key-authenticated provider.
type CredentialSource interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// RegistryLookup is the read subset of internal/providers/registry.
// Registry this package needs: find the lane the router picked, then the
// provider record it belongs to. *registry.Registry satisfies this
// interface directly with no adapter; declared locally so a test can
// supply an in-memory fake instead of opening a real database.
type RegistryLookup interface {
	ListLanes(ctx context.Context) ([]registry.LaneRecord, error)
	GetProvider(ctx context.Context, name string) (registry.ProviderRecord, error)
}

// Resolver is the production conductor.ProviderResolver. The zero value
// is not usable; construct with NewResolver.
type Resolver struct {
	lookup      RegistryLookup
	credentials CredentialSource
	clock       runtime.Clock
	// transport is every driver adapter's route to the network, built by
	// providers/transport (moved out of this package - see this file's
	// header comment): anthropic/gemini/ollama through their own tiny
	// Transport-to-HTTPDoer adapters, openai through OpenAIDoer's reverse
	// translation. NewResolver's own signature never names *http.Client,
	// so no test in this package needs to import "net/http" (the default
	// unit lane forbids it - internal/build/hygiene.go).
	transport transport.Transport
}

// NewResolver builds a Resolver. credentials may be nil: every
// key-authenticated provider then resolves to a typed KindUnavailable
// naming its missing credential rather than panicking or fabricating a
// driver (see this file's header comment). lookup, clock and transport
// must not be nil; the composition root builds transport with
// providers/transport's NewHTTPTransport.
func NewResolver(lookup RegistryLookup, credentials CredentialSource, clock runtime.Clock, transport transport.Transport) (*Resolver, error) {
	if lookup == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "dispatch: registry lookup must not be nil")
	}
	if clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "dispatch: clock must not be nil")
	}
	if transport == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "dispatch: transport must not be nil")
	}
	return &Resolver{lookup: lookup, credentials: credentials, clock: clock, transport: transport}, nil
}

var _ interface {
	Resolve(ctx context.Context, sel provider.Selection) (provider.ModelProvider, error)
} = (*Resolver)(nil)

// Resolve turns sel into a live provider.ModelProvider: it looks up the
// selected lane and its provider record in the real registry, then
// builds the matching providers/* driver. Every step is real (Art.1): a
// lane or provider the registry does not know about is KindNotFound, a
// driver kind or auth mode this composition root cannot dispatch yet is
// KindUnsupported, and a key-authenticated provider with no credential
// source wired is KindUnavailable naming the missing vault-key reference.
func (r *Resolver) Resolve(ctx context.Context, sel provider.Selection) (provider.ModelProvider, error) {
	lane, err := r.findLane(ctx, sel.LaneID)
	if err != nil {
		return nil, err
	}
	rec, err := r.lookup.GetProvider(ctx, lane.ProviderName)
	if err != nil {
		return nil, err
	}
	if !rec.Driver.Valid() {
		return nil, cascade.Newf(cascade.KindInternal, "dispatch: provider %q has invalid driver_kind %q", rec.Name, rec.Driver)
	}
	if !rec.Auth.Valid() {
		return nil, cascade.Newf(cascade.KindInternal, "dispatch: provider %q has invalid auth_type %q", rec.Name, rec.Auth)
	}
	if rec.Auth == registry.AuthOAuth {
		return nil, cascade.Newf(cascade.KindUnsupported,
			"dispatch: provider %q uses oauth auth, and no production OAuthBroker is wired at this composition root yet", rec.Name)
	}
	if r.credentials == nil {
		return nil, cascade.Newf(cascade.KindUnavailable,
			"dispatch: provider %q requires credential %q, no credential source is configured at this composition root", rec.Name, rec.AuthRef)
	}
	return r.build(rec, sel.Model)
}

// findLane returns the LaneRecord named laneID, or KindNotFound. A
// selection naming a lane the registry no longer holds is a real,
// reportable inconsistency, never silently ignored.
func (r *Resolver) findLane(ctx context.Context, laneID string) (registry.LaneRecord, error) {
	lanes, err := r.lookup.ListLanes(ctx)
	if err != nil {
		return registry.LaneRecord{}, err
	}
	for _, lane := range lanes {
		if lane.LaneName == laneID {
			return lane, nil
		}
	}
	return registry.LaneRecord{}, cascade.Newf(cascade.KindNotFound, "dispatch: lane %q is not registered", laneID)
}
