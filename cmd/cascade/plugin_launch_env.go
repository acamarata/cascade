//go:build !windows

// Purpose: installs the CLI's plugin launch environment source (a trusted
// process plugin launched by a CLI verb such as `cascade github ci
// merge-on-green`), the egress registrar adapter every launch registers its
// class through, and the recorder lock primitive (flock).
// Inputs: the process environment (paths), the runtime store.
// Outputs: plugins.SetLaunchEnvSource at init; the store, effect log and bus
// are opened once per process, on the first launch, and plugin effect
// recovery runs on that open.
// Constraints: the child's environment is read only through os.Getenv by
// the plugins package's allowlist; the effect log is openEffectLog's (no
// second audit.Log). The daemon replaces this source (wire_plugin_launch.go).
// Windows has no process plugins (process.Launch refuses there), so no
// source is installed and every launch refuses KindUnavailable.
// SPORT: cmd/cascade plugin launch environment (P1-PLG-03).
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/internal/output"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/plugins/process"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

func init() {
	plugins.SetRecorderLock(tryFlock)
	plugins.SetLaunchEnvSource(openCLILaunchEnv)
}

// tryFlock takes a non-blocking exclusive flock on f; false means another
// open file holds it.
func tryFlock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	}
	return false, err
}

// pluginEffectsDir is the recorder lock directory under a data directory.
func pluginEffectsDir(paths runtime.PathProvider) string {
	return filepath.Join(paths.DataDir(), "plugin-effects")
}

// cliLaunchEnv memoizes the CLI's launch environment; a failed open is not
// remembered, so a later launch retries it.
var cliLaunchEnv struct {
	mu  sync.Mutex
	env *plugins.LaunchEnv
}

// openCLILaunchEnv is the CLI source: it opens the runtime store, the
// effect log over it and the bus once per process, running plugin effect
// recovery on that open.
func openCLILaunchEnv(ctx context.Context) (plugins.LaunchEnv, error) {
	cliLaunchEnv.mu.Lock()
	defer cliLaunchEnv.mu.Unlock()
	if cliLaunchEnv.env != nil {
		return *cliLaunchEnv.env, nil
	}
	paths, err := runtime.NewPathProvider(os.Getenv, os.UserHomeDir)
	if err != nil {
		return plugins.LaunchEnv{}, err
	}
	clock := runtime.NewSystemClock()
	store, _, _, err := openRuntimeStore(ctx, paths, clock)
	if err != nil {
		return plugins.LaunchEnv{}, err
	}
	env, err := launchEnvOver(ctx, store, events.New(store, clock), clock, paths)
	if err != nil {
		return plugins.LaunchEnv{}, err
	}
	cliLaunchEnv.env = &env
	return env, nil
}

// launchEnvOver builds a LaunchEnv over store and bus: the effect log is
// openEffectLog's, and plugin effect recovery runs before it is returned.
func launchEnvOver(ctx context.Context, store provider.Store, bus *events.Bus, clock runtime.Clock, paths runtime.PathProvider) (plugins.LaunchEnv, error) {
	effects, err := openEffectLog(store, clock, nil, paths)
	if err != nil {
		return plugins.LaunchEnv{}, err
	}
	if _, err := plugins.RecoverPluginEffects(ctx, effects, pluginEffectsDir(paths)); err != nil {
		return plugins.LaunchEnv{}, err
	}
	return plugins.LaunchEnv{
		Store: store, Effects: effects, Bus: bus, Stderr: launchWarnings{out: output.NewDefault(false, false, false, false)},
		Registrar: pluginEgressRegistrar{reg: egress.DefaultRegistry()}, Getenv: os.Getenv, Clock: clock,
	}, nil
}

// launchWarnings carries the plugin runtime's pre-exec consent line to the
// CLI's stderr through internal/output (the output contract bars os.Stderr).
type launchWarnings struct{ out *output.Writer }

func (l launchWarnings) Write(p []byte) (int, error) {
	l.out.Warn("%s", strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// pluginEgressRegistrar adapts the process egress registry to
// process.EgressRegistrar; a repeat registration is success.
type pluginEgressRegistrar struct{ reg *egress.Registry }

// Register implements process.EgressRegistrar.
func (a pluginEgressRegistrar) Register(class process.EgressClass, cfg process.EgressRegistrationConfig) error {
	tiers := make([]egress.SensitivityTier, len(cfg.AllowedTiers))
	for i, t := range cfg.AllowedTiers {
		tiers[i] = egress.SensitivityTier(t)
	}
	err := a.reg.Register(egress.EgressClass(class), egress.InterceptConfig{
		Enabled: cfg.Enabled, AllowRestricted: cfg.AllowRestricted, AllowedTiers: tiers, Owner: cfg.Owner,
	})
	if errors.Is(err, egress.ErrDuplicateClass) {
		return nil
	}
	return err
}
