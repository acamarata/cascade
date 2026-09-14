// Package plugins (dispatch_remote.go): Purpose: the real
// internal/hooks/egress wiring for ProvisionElevated's RuntimeRemote
// case (dispatch.go), split into its own file so dispatch.go stays under
// the repo's 300-line cap — the same precedent P1-E15-W4-S32-T2's
// journal recorded for its own out-of-files_scope split files. This file
// is at internal/plugins/ directly (no subdirectory), the same exempt
// position dispatch.go itself occupies (dispatch.go's own header
// comment), so it may import internal/** freely; internal/plugins/remote
// cannot (BOUNDARY NOTE, internal/plugins/remote/remote.go's package
// doc) — which is exactly why this glue lives here and not there.
//
// Inputs: none at package scope; newDispatchRemoteInterceptor is called
// once per RuntimeRemote ProvisionElevated call that needs one built
// (the caller may also inject its own via remoteIntercept and skip this
// entirely — production wiring for a live credential-store-backed vault
// is a gap this file does not close, see below).
// Outputs: a remote.Interceptor backed by a real *egress.Engine.
// Constraints: the handshake carries no credential material by
// construction (internal/plugins/remote has no import path to
// internal/secrets at all), so this file's vault is deliberately empty
// rather than backed by a live *secrets.Broker — see
// dispatchRemoteEmptyVault's own doc comment for why building a full
// custody chain here is out of this ticket's scope, named rather than
// silently skipped.
// SPORT: internal/plugins dispatch-remote (ADD) — P1-E15-W4-S33-T4.
package plugins

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins/remote"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// provisionRemoteRuntime is ProvisionElevated's RuntimeRemote case,
// extracted so ProvisionElevated itself stays under the funlen gate's
// 50-line cap. It never returns a nil error, by design (remote.Dispatch's
// own contract) — a caller must not treat a non-nil return here as a
// harder failure than the flag=false or deferred-dispatch cases it
// already covers.
func provisionRemoteRuntime(
	ctx context.Context, m plugin.Manifest, enableRemoteRuntime bool, remoteIntercept remote.Interceptor,
) error {
	cfg := remote.RemoteRuntimeConfig{
		Host: m.Remote.Host, Port: m.Remote.Port, ABIVersion: remote.HostABIVersionV1,
	}
	interceptor := remoteIntercept
	if interceptor == nil && enableRemoteRuntime {
		built, err := newDispatchRemoteInterceptor()
		if err != nil {
			return err
		}
		interceptor = built
	}
	return remote.Dispatch(ctx, cfg, enableRemoteRuntime, m.ID, interceptor)
}

// dispatchRemoteTier is the SensitivityTier this composition root
// declares for handshake content: TierInternal, because a handshake body
// (ABI version, plugin id) is ordinary operational data, never a value a
// user or another system should never see, and never a value that must
// stay strictly on this machine either — R-21.228's "tier passed
// explicitly" is honored by naming this choice here rather than letting
// it default.
const dispatchRemoteTier = egress.TierInternal

// dispatchRemoteInterceptor adapts a real *egress.Engine + the
// unforgeable egress.Capability for EgressClassPluginRemote to
// remote.Interceptor.
type dispatchRemoteInterceptor struct {
	engine *egress.Engine
	token  egress.Capability
}

// Intercept implements remote.Interceptor over the real firewall.
func (a dispatchRemoteInterceptor) Intercept(ctx context.Context, content []byte) ([]byte, error) {
	return a.engine.Intercept(ctx, a.token, dispatchRemoteTier, content)
}

// newDispatchRemoteInterceptor builds the production Interceptor:
// acquires the real egress.Capability for EgressClassPluginRemote from
// the process-wide default registry (this is what proves the class is
// disabled unless an operator has separately re-registered or enabled
// it — Capability() itself refuses a disabled class, per
// egress.Registry.Capability's own contract), binds it to a real
// secrets.Detector (the same construction provisionStorageDomain already
// uses in this file), and an intentionally empty vault.
func newDispatchRemoteInterceptor() (remote.Interceptor, error) {
	token, err := egress.DefaultRegistry().Capability(egress.EgressClassPluginRemote)
	if err != nil {
		return nil, err
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "plugin: build remote-handshake secret detector")
	}
	engine, err := egress.NewEngine(egress.DefaultRegistry(), dispatchRemoteEmptyVault{}, detector)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "plugin: build remote-handshake egress engine")
	}
	return dispatchRemoteInterceptor{engine: engine, token: token}, nil
}

// dispatchRemoteEmptyVault is the exact-value pass's value source for
// the handshake path: always empty.
//
// DISCLOSED GAP (LANE-RULES §1): the firewall's real value source in
// production is secrets.EgressVault, backed by a live *secrets.Broker —
// which itself needs a Custody backend and an ElevationGate, neither of
// which any composition root threads into ProvisionElevated today (its
// signature carries a *sql.DB/Store/PluginDomainRegistry tuple, not a
// vault broker). Building that chain here would be a materially larger
// change than this ticket's stated scope (a handshake stub), and the
// handshake body this vault is checked against carries no credential
// material in the first place — internal/plugins/remote has no import
// path to internal/secrets at all (BOUNDARY NOTE), so there is
// structurally nothing for a live vault to catch here that the
// detector's shape-based pass (still real, still running) would not
// already catch. Named here rather than silently assumed correct.
type dispatchRemoteEmptyVault struct{}

func (dispatchRemoteEmptyVault) List(context.Context) ([]string, error) { return nil, nil }

func (dispatchRemoteEmptyVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "plugin: remote-handshake vault has no entries")
}
