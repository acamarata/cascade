// Package plugins (launch_env.go): Purpose: the one source of the
// collaborators a trusted process-plugin launch needs: the plugin store, the
// effect writer, the event bus, the consent-warning stream, the egress
// registrar, the environment reader and the clock.
// Inputs: a LaunchEnvSource a composition root installs (cmd/cascade: the
// CLI source at init, the daemon source when the daemon wires).
// Outputs: the LaunchEnv LaunchInstalled launches under.
// Constraints: no source installed means every launch refuses
// KindUnavailable; a source never falls back to a default store or to the
// process environment on its own. The source is resolved on every launch,
// so a daemon that replaces the CLI source is honoured from then on.
// SPORT: internal/plugins trusted launch environment (P1-PLG-03).
package plugins

import (
	"context"
	"io"
	"sync"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// LaunchEnv is everything one trusted launch runs under. Getenv is the only
// way the child's environment is read: childEnv copies the allowlisted names
// it answers and nothing else.
type LaunchEnv struct {
	Store     provider.Store
	Effects   audit.EffectWriter
	Bus       *events.Bus
	Stderr    io.Writer
	Registrar process.EgressRegistrar
	Getenv    func(string) string
	Clock     runtime.Clock
}

// LaunchEnvSource builds the LaunchEnv for one launch.
type LaunchEnvSource func(ctx context.Context) (LaunchEnv, error)

var launchEnvState struct {
	mu  sync.RWMutex
	src LaunchEnvSource
}

// SetLaunchEnvSource installs src as the launch environment source and
// returns a restore func that puts the previous source back. A nil src
// detaches the source, so every launch refuses until one is installed.
func SetLaunchEnvSource(src LaunchEnvSource) (restore func()) {
	launchEnvState.mu.Lock()
	previous := launchEnvState.src
	launchEnvState.src = src
	launchEnvState.mu.Unlock()
	return func() {
		launchEnvState.mu.Lock()
		launchEnvState.src = previous
		launchEnvState.mu.Unlock()
	}
}

// errNoLaunchEnv is the refusal when no composition root installed a source.
var errNoLaunchEnv = cascade.New(cascade.KindUnavailable,
	"plugins: no plugin launch environment is wired in this process; no process plugin can start")

// ResolveLaunchEnv resolves the installed source for one launch. No source
// is KindUnavailable; a source error is returned as the source reported it.
// A LaunchEnv without a store or an effect writer is refused here, so no
// launch or call ever runs unrecorded.
func ResolveLaunchEnv(ctx context.Context) (LaunchEnv, error) {
	launchEnvState.mu.RLock()
	src := launchEnvState.src
	launchEnvState.mu.RUnlock()
	if src == nil {
		return LaunchEnv{}, errNoLaunchEnv
	}
	env, err := src(ctx)
	if err != nil {
		return LaunchEnv{}, err
	}
	switch {
	case env.Store == nil:
		return LaunchEnv{}, cascade.New(cascade.KindUnavailable, "plugins: the launch environment has no plugin store")
	case env.Effects == nil:
		return LaunchEnv{}, cascade.New(cascade.KindUnavailable,
			"plugins: the launch environment has no effect writer; a plugin launch and its calls are never unrecorded")
	case env.Getenv == nil:
		return LaunchEnv{}, cascade.New(cascade.KindUnavailable, "plugins: the launch environment has no environment reader")
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}
	return env, nil
}

// paramsDigestKey carries WithParamsDigest's override.
type paramsDigestKey struct{}

// WithParamsDigest makes the next Call under ctx record digest as its
// params_hash instead of sha256(params): a caller whose params carry
// rehydrated or secret-bearing values passes a digest over the tagged form,
// so the audit row is never a hash of plaintext. A non-hex or empty digest
// refuses the call.
func WithParamsDigest(ctx context.Context, digest string) context.Context {
	return context.WithValue(ctx, paramsDigestKey{}, digest)
}

// ParamsDigestFrom reads WithParamsDigest's override.
func ParamsDigestFrom(ctx context.Context) (string, bool) {
	d, ok := ctx.Value(paramsDigestKey{}).(string)
	return d, ok
}

// launched is every TrustedHandle not yet closed, for CloseLaunched.
var launched struct {
	mu  sync.Mutex
	set map[*TrustedHandle]struct{}
}

func trackLaunched(t *TrustedHandle) {
	launched.mu.Lock()
	defer launched.mu.Unlock()
	if launched.set == nil {
		launched.set = map[*TrustedHandle]struct{}{}
	}
	launched.set[t] = struct{}{}
}

func untrackLaunched(t *TrustedHandle) {
	launched.mu.Lock()
	delete(launched.set, t)
	launched.mu.Unlock()
}

// CloseLaunched closes every live TrustedHandle (a daemon's shutdown), and
// returns the first error.
func CloseLaunched(ctx context.Context) error {
	launched.mu.Lock()
	all := make([]*TrustedHandle, 0, len(launched.set))
	for t := range launched.set {
		all = append(all, t)
	}
	launched.mu.Unlock()
	var first error
	for _, t := range all {
		if err := t.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}
