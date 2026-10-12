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
//	registry. The engine is the process firewall's (egress.ProcessEngine),
//	resolved on every write, never at init: init() runs before any
//	composition root installs the firewall.
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
//	compile-time class registry; it opens no file, dials nothing and forks
//	nothing. A write with no process firewall installed refuses.
//
// SPORT: internal/plugins:nself-wiring (ADD) — P1-E25-W5-S52-T2.

package plugins

import (
	"context"
	"log/slog"

	"github.com/acamarata/cascade/internal/hooks/egress"
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
// class is checked here (Capability itself refuses an unknown or disabled
// class), and the engine is the process firewall's, resolved per write. The
// registry is a parameter so nself_wiring_test.go can drive the refusal an
// operator who disabled this class would produce, which is the branch that
// decides whether the daemon keeps running.
func newNselfInterceptor(registry *egress.Registry) (nselfplugin.EgressInterceptor, error) {
	if _, err := registry.Capability(nselfEgressClass); err != nil {
		return nil, err
	}
	return nselfEgressAdapter{registry: registry}, nil
}

// nselfEgressAdapter adapts the process firewall to the plugin's local
// EgressInterceptor seam. It is a cast, not a translation: the class and
// tier strings are identical by the constants above.
type nselfEgressAdapter struct {
	registry *egress.Registry
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
	engine, err := egress.ProcessEngine(a.registry)
	if err != nil {
		return nil, err
	}
	return engine.InterceptClass(ctx, nselfEgressClass, egress.SensitivityTier(tier), content)
}
