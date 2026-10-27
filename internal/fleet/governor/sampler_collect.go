package governor

// Purpose: SamplerConfig.collector resolves which metrics collector a Sampler
// polls: the injected SamplerConfig.Collect, else the platform's
// collectMetrics. Split out of sampler.go to keep that file under the
// 300-line cap.
//
// Inputs: a SamplerConfig. Outputs: a non-nil collector func.
//
// Constraints: production callers leave Collect nil and get the host
// collector. Tests inject a deterministic one so they never depend on the
// host's first reading, which on linux needs two ticks (the first has no CPU
// baseline, see sampler_linux.go).
//
// SPORT: internal/fleet/governor.Sampler (ADD, P1-CAP-12 follow-up).

// collector returns cfg.Collect when set, else the platform collector.
func (cfg SamplerConfig) collector() func() (ResourceSnapshot, error) {
	if cfg.Collect != nil {
		return cfg.Collect
	}
	return collectMetrics
}
