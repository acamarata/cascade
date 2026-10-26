package update

// Purpose: register the release-download egress class on the shared
//   registry, and the production Interceptor the CLI hands HTTPFetcher.
// Inputs: none at init; the process firewall at the first request.
// Outputs: egress.DefaultRegistry() carries EgressClassReleaseDownload
//   (enabled, AllowRestricted false) once this package is linked.
// Constraints: the constant lives in internal/hooks/egress/classes.go; only
//   the registration lives here. ProcessInterceptor opens the process
//   firewall lazily, on its first InterceptClass call, so the brew, OCI,
//   node-managed, digest-match and report-only paths never open custody.
//   A firewall that cannot be built refuses every request.
// SPORT: internal/update ProcessInterceptor/ADDED.

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/hooks/egress"
)

func init() {
	egress.DefaultRegistry().MustRegister(egress.EgressClassReleaseDownload, egress.InterceptConfig{
		Enabled: true,
		Owner:   "P1-SHIP-04",
	})
}

// ProcessInterceptor returns an Interceptor over the process firewall's
// engine, resolved once on first use.
func ProcessInterceptor() Interceptor { return &processInterceptor{} }

type processInterceptor struct {
	once   sync.Once
	engine *egress.Engine
	err    error
}

// InterceptClass resolves the process engine on first use and transits
// content through it.
func (p *processInterceptor) InterceptClass(ctx context.Context, class egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error) {
	p.once.Do(func() { p.engine, p.err = egress.ProcessEngine(egress.DefaultRegistry()) })
	if p.err != nil {
		return nil, p.err
	}
	return p.engine.InterceptClass(ctx, class, tier, content)
}
