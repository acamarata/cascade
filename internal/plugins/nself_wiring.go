// Purpose (this file): the cascade-nself composition root — the one place
//
//	allowed to import both sides, exactly as review_wiring.go bridges
//	cascade-review and cascadepa_wiring.go bridges `cascade chat`.
//	plugins/nself may not import internal/** (Art.10.2), so it declares a
//	local, string-identical EgressInterceptor seam; this file binds the
//	REAL internal/hooks/egress engine to it and, by importing the package
//	at all, puts cascade-nself into plugin.Builtins() in the shipped binary
//	(internal/plugins/registry_test.go's pinned inventory is the proof).
//
// Inputs: none at import time beyond the process-wide default egress
//
//	registry and the default secret-detection config — the same two
//	collaborators dispatch_remote.go's newDispatchRemoteInterceptor already
//	resolves in this package.
//
// Outputs: either the real interceptor is installed, or nothing is and the
//
//	plugin keeps its REFUSING default (one slog line says which). This is
//	deliberately not a panic: egress.Registry.Capability legitimately
//	refuses a class an operator has disabled, and a disabled optional
//	plugin must not take the daemon down with it. Fail-closed, loudly, is
//	the behaviour — never fail-open, which is what a pass-through default
//	inside the plugin would have been.
//
// Constraints: init() must not touch the environment. It reads the
//
//	compile-time class registry and builds a detector; it opens no file,
//	dials nothing and forks nothing.
//
// SPORT: internal/plugins:nself-wiring (ADD) — P1-E25-W5-S52-T2.

package plugins

import (
	"context"
	"log/slog"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
	nselfplugin "github.com/acamarata/cascade/plugins/nself"
)

// nselfEgressClass and nselfEgressTier assert at COMPILE time that the
// plugin's local mirror of the class name and tier carry the same string
// values as the real inventory entries. A silent divergence would mean the
// plugin asked the firewall about a class nobody registered, and
// InterceptClass would refuse every response with an "unknown class"
// error that reads like a configuration problem.
const (
	nselfEgressClass = egress.EgressClass(nselfplugin.EgressClassNselfBackend)
	nselfEgressTier  = egress.SensitivityTier(nselfplugin.TierInternal)
)

func init() {
	installNselfEgressInterceptor(newRealNselfInterceptor, slog.Default())
}

// installNselfEgressInterceptor is init()'s body, extracted so
// nself_wiring_test.go drives BOTH branches (a working build and a
// refusing registry) against the same code init() runs, rather than a
// re-typed duplicate.
func installNselfEgressInterceptor(build func() (nselfplugin.EgressInterceptor, error), log *slog.Logger) {
	interceptor, err := build()
	if err != nil {
		log.Warn("cascade-nself: egress interceptor not installed; every nself tool response will refuse to emit",
			slog.String("class", string(nselfEgressClass)),
			slog.String("error", err.Error()))
		return
	}
	if serr := nselfplugin.SetEgressInterceptor(interceptor); serr != nil {
		log.Warn("cascade-nself: egress interceptor rejected by the plugin",
			slog.String("error", serr.Error()))
	}
}

// newRealNselfInterceptor builds the production interceptor over the
// process-wide default class registry.
func newRealNselfInterceptor() (nselfplugin.EgressInterceptor, error) {
	return newNselfInterceptor(egress.DefaultRegistry())
}

// newNselfInterceptor is newRealNselfInterceptor's body with the class
// registry as a parameter: the unforgeable capability for the nself-backend
// class (Capability itself refuses an unknown or disabled class), a real
// secrets.Detector, and a real egress.Engine — the same construction
// newDispatchRemoteInterceptor uses in this package. The registry is a
// parameter so nself_wiring_test.go can drive the refusal an operator who
// disabled this class would produce, which is the branch that decides
// whether the daemon keeps running.
func newNselfInterceptor(registry *egress.Registry) (nselfplugin.EgressInterceptor, error) {
	if _, err := registry.Capability(nselfEgressClass); err != nil {
		return nil, err
	}
	detector, err := secrets.NewDetector(secrets.DefaultRegistry(), secrets.DefaultDetectionConfig())
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "plugin: build cascade-nself secret detector")
	}
	engine, err := egress.NewEngine(registry, nselfEmptyVault{}, detector)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "plugin: build cascade-nself egress engine")
	}
	return nselfEgressAdapter{engine: engine}, nil
}

// nselfEgressAdapter adapts the real *egress.Engine to the plugin's local
// EgressInterceptor seam. It is a cast, not a translation: the class and
// tier strings are identical by the constants above.
type nselfEgressAdapter struct {
	engine *egress.Engine
}

// InterceptClass implements nselfplugin.EgressInterceptor over the real
// firewall. It refuses any class other than the one this plugin owns: a
// builtin that could name an arbitrary class would be able to pick the
// most permissive policy in the inventory.
func (a nselfEgressAdapter) InterceptClass(
	ctx context.Context,
	class nselfplugin.EgressClass,
	tier nselfplugin.SensitivityTier,
	content []byte,
) ([]byte, error) {
	if class != nselfplugin.EgressClassNselfBackend {
		return nil, cascade.Newf(cascade.KindCapabilityDenied,
			"plugin: cascade-nself may only write to the %q egress class, not %q", nselfEgressClass, class)
	}
	return a.engine.InterceptClass(ctx, nselfEgressClass, egress.SensitivityTier(tier), content)
}

// nselfEmptyVault is the exact-value pass's value source for this path:
// always empty.
//
// DISCLOSED GAP: the firewall's real value source in production is
// secrets.EgressVault over a live *secrets.Broker, which needs a Custody
// backend and an ElevationGate that no composition root threads into a
// builtin plugin's wiring today — dispatch_remote.go's
// dispatchRemoteEmptyVault documents the identical gap for the identical
// reason. The detector's shape-based pass is still real and still runs on
// every byte, which is what catches a credential in this payload; the
// exact-value pass has nothing to compare against, and that is stated here
// rather than assumed harmless.
type nselfEmptyVault struct{}

func (nselfEmptyVault) List(context.Context) ([]string, error) { return nil, nil }

func (nselfEmptyVault) Get(context.Context, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindNotFound, "plugin: cascade-nself's egress vault has no entries")
}
