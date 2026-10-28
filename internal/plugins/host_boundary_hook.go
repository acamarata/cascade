//go:build !windows

// Package plugins (host_boundary_hook.go): Purpose: the seam through which
// a composition root supplies each trusted launch's host capability checker
// (the plugin capability boundary P1-PLG-08 routes host calls through).
// Inputs: a HostBoundaryFactory, and per launch the LaunchEnv, plugin id and
// the record's EffectiveGrants.
// Outputs: the process.HostCapabilityChecker LaunchInstalled assigns to the
// launched runtime, or nil.
// Constraints: this file only stores and calls the factory; it never routes
// a host call. An unset factory or a factory error leaves the checker nil,
// and P1-PLG-08's routing then answers every host call KindUnavailable.
// Windows has no process plugins, so this file is unix-only.
// SPORT: internal/plugins host boundary hook (P1-PLG-03).
package plugins

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/internal/plugins/process"
)

// HostBoundaryFactory builds the capability checker for one launch of
// pluginID holding grants.
type HostBoundaryFactory func(ctx context.Context, env LaunchEnv, pluginID string, grants []string) (process.HostCapabilityChecker, error)

var hostBoundaryState struct {
	mu      sync.RWMutex
	factory HostBoundaryFactory
}

// SetHostBoundary installs f and returns a restore func that puts the
// previous factory back. A nil f detaches it.
func SetHostBoundary(f HostBoundaryFactory) (restore func()) {
	hostBoundaryState.mu.Lock()
	previous := hostBoundaryState.factory
	hostBoundaryState.factory = f
	hostBoundaryState.mu.Unlock()
	return func() {
		hostBoundaryState.mu.Lock()
		hostBoundaryState.factory = previous
		hostBoundaryState.mu.Unlock()
	}
}

func init() {
	configureRuntime = func(ctx context.Context, env LaunchEnv, id string, grants []string, rt *process.ProcessRuntime) {
		rt.CapabilityChecker = hostBoundaryFor(ctx, env, id, grants)
	}
}

// hostBoundaryFor returns the checker for one launch: the factory's result,
// or nil when no factory is set or it fails.
func hostBoundaryFor(ctx context.Context, env LaunchEnv, pluginID string, grants []string) process.HostCapabilityChecker {
	hostBoundaryState.mu.RLock()
	f := hostBoundaryState.factory
	hostBoundaryState.mu.RUnlock()
	if f == nil {
		return nil
	}
	checker, err := f(ctx, env, pluginID, append([]string(nil), grants...))
	if err != nil {
		return nil
	}
	return checker
}
