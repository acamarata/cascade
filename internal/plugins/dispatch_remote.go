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
// Inputs: none at package scope; newDispatchRemoteInterceptor (the
// default-registry wrapper around newDispatchRemoteInterceptorFrom) is called
// once per RuntimeRemote ProvisionElevated call that needs one built
// (the caller may also inject its own via remoteIntercept and skip this
// entirely).
// Outputs: a remote.Interceptor backed by the process firewall's engine
// (egress.ProcessEngine), so both halves of the substitution pass run: the
// detector over credential shape and the exact-value pass over the real
// vault.
// Constraints: fail closed. With no process firewall installed the
// interceptor is not built and the handshake is refused.
// SPORT: internal/plugins dispatch-remote (ADD) — P1-E15-W4-S33-T4.
package plugins

import (
	"context"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/plugins/remote"
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

// newDispatchRemoteInterceptor builds the production Interceptor over the
// process-wide default registry, where EgressClassPluginRemote is
// registered disabled: the call refuses with ErrClassDisabled before any
// handshake byte exists.
func newDispatchRemoteInterceptor() (remote.Interceptor, error) {
	return newDispatchRemoteInterceptorFrom(egress.DefaultRegistry())
}

// newDispatchRemoteInterceptorFrom builds the Interceptor over reg: it
// acquires the real egress.Capability for EgressClassPluginRemote from reg
// (Capability() itself refuses a disabled or unknown class, per
// egress.Registry.Capability's own contract) and takes the process
// firewall's engine over the SAME registry, so a caller cannot pair a token
// from one registry with the policy of another.
func newDispatchRemoteInterceptorFrom(reg *egress.Registry) (remote.Interceptor, error) {
	if reg == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "plugin: the remote-handshake interceptor needs an egress registry")
	}
	token, err := reg.Capability(egress.EgressClassPluginRemote)
	if err != nil {
		return nil, err
	}
	engine, err := egress.ProcessEngine(reg)
	if err != nil {
		return nil, err
	}
	return dispatchRemoteInterceptor{engine: engine, token: token}, nil
}
