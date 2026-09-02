package runtime

import (
	"context"
	"fmt"
)

// Purpose: assemble the runtime startup sequence — PathProvider, the TOML
//   loader (incl. the schema_version frame it owns), and profile
//   resolution — into a single entrypoint later subsystems call into.
// Inputs: BootstrapOptions carries every external input (the --profile
//   flag, injected env/home accessors, an injected Clock).
// Outputs: a *Runtime holding the resolved Profile, PathProvider, and
//   loaded *Config.
// Constraints: T-1 task 7 — this is the startup-sequence anchor that
//   S-04.T2, S-05.T3, and S-05.T4 change:-wire into; it must not grow
//   business logic beyond composing the pieces those tickets already
//   define. No vault/daemon dependency (elevation baseline enforcement is
//   C/S-05.T8's).
// SPORT: runtime/bootstrap (ADD, placeholder per T-1 sport_updates).

// BootstrapOptions carries every external input the bootstrap sequence
// needs. All fields are optional; a nil accessor falls back to the real
// process environment. Tests (Art.7.1) must always supply Getenv/HomeDir/
// Environ pointing at fakes.
type BootstrapOptions struct {
	ProfileFlag string
	Getenv      Getenv
	HomeDir     HomeDirFunc
	Environ     func() []string
	Clock       Clock
	Warn        func(format string, args ...interface{})
}

// Runtime is the fully resolved bootstrap result. Later tickets extend the
// startup sequence by consuming this struct — they must not construct
// PathProvider or Config themselves.
type Runtime struct {
	Profile Profile
	Paths   PathProvider
	Config  *Config
	Clock   Clock
}

// Bootstrap runs the full startup sequence: resolve paths, load and
// validate config.toml (running the schema_version upgrade-rewrite frame
// when needed), and resolve the active profile. It is the single
// entrypoint every later subsystem's startup path calls into.
func Bootstrap(ctx context.Context, opts BootstrapOptions) (*Runtime, error) {
	clock := opts.Clock
	if clock == nil {
		clock = NewSystemClock()
	}

	paths, err := NewPathProvider(opts.Getenv, opts.HomeDir)
	if err != nil {
		return nil, fmt.Errorf("runtime: bootstrap paths: %w", err)
	}

	cfg, err := Load(ctx, LoadOptions{
		Path:        paths.ConfigPath(),
		ProfileFlag: opts.ProfileFlag,
		Getenv:      opts.Getenv,
		Environ:     opts.Environ,
		Warn:        opts.Warn,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: bootstrap config: %w", err)
	}

	// runtime.home/runtime.data_dir are always the resolved PathProvider
	// values, stamped in here rather than read from the file (see
	// runtimeSection's doc comment in config.go).
	cfg.Runtime.Home = paths.Root()
	cfg.Runtime.DataDir = paths.DataDir()

	return &Runtime{
		Profile: cfg.Runtime.Profile,
		Paths:   paths,
		Config:  cfg,
		Clock:   clock,
	}, nil
}
