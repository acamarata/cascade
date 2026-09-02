package runtime

import (
	"context"
	"fmt"
	"os"
	"sort"
)

// Purpose: the TOML config loader's core types and the Load entry point —
//   ConfigSource precedence, the two typed sections this ticket owns
//   ([runtime], [elevation]), *Config and its effective-view accessors,
//   and Load itself, which wires together the file-read/upgrade,
//   env-override, and section-parsing steps that live in this package's
//   sibling files (split from a single config.go per R-14.117: config_load.go
//   owns file I/O and section parsing, config_env.go owns the generic
//   CASCADE_<SECTION>__<KEY> env-override machinery and tree helpers,
//   config_handlers.go owns the `cascade config` CLI-facing handlers).
// Inputs: Load takes a context, the resolved config file path, the
//   --profile flag value, and injected Getenv/Environ accessors.
// Outputs: a *Config carrying the two typed sections this ticket owns,
//   every other 08 §3 section preserved verbatim in Extra, and a source
//   annotation per effective key.
// Constraints: Art.7.1 — Load performs its own file I/O but never touches
//   $HOME directly (the caller resolves the path via PathProvider); tests
//   always pass a t.TempDir() path. Art.1 — sections this ticket does not
//   own (logging, storage, retrieval, ...) are preserved, never validated
//   or defaulted here; inventing a default for an unowned section would be
//   the R-14.107 mistake repeated.
// SPORT: runtime/config (ADD, placeholder per T-1 sport_updates).

// ConfigSource names the precedence level that produced an effective
// config value.
type ConfigSource string

// The four precedence levels a config value can resolve from, weakest
// first: SourceDefault (nobody set it) < SourceFile (config.toml) <
// SourceEnv (CASCADE_<SECTION>__<KEY>) < SourceFlag (a CLI flag, profile
// resolution only in this ticket).
const (
	SourceDefault ConfigSource = "default"
	SourceFile    ConfigSource = "file"
	SourceEnv     ConfigSource = "env"
	SourceFlag    ConfigSource = "flag"
)

// runtimeSection is the [runtime] table (08-INIT-CONFIG-SPEC §3, cold
// reload class). Home and DataDir are never read from config.toml — they
// are always the resolved PathProvider values, set by Bootstrap — so this
// ticket never invents a second, independent way to configure paths
// alongside CASCADE_HOME.
type runtimeSection struct {
	Profile Profile `toml:"profile"`
	Home    string  `toml:"home"`
	DataDir string  `toml:"data_dir"`
}

// elevationSection is the [elevation] table (08-INIT-CONFIG-SPEC
// §[elevation], cold-only / non-hot-reloadable). This ticket loads and
// shape-validates it only; tightening-only enforcement and divergent-boot
// detection are C/S-05.T8's (baseline.go).
type elevationSection struct {
	AllowRemote  bool   `toml:"allow_remote"`
	HelperPubkey string `toml:"helper_pubkey"`
}

// ConfigError is the typed error the loader returns for malformed TOML,
// an unrecognised key inside a section this ticket owns, a type mismatch,
// or a missing required field.
type ConfigError struct {
	Field  string
	Reason string
}

// Error implements the error interface.
func (e *ConfigError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("runtime: config: %s: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("runtime: config: %s", e.Reason)
}

// Config is the fully resolved runtime configuration: the two sections
// this ticket owns, typed; the schema_version; every other 08 §3 section
// preserved verbatim; and a source annotation per effective key.
type Config struct {
	SchemaVersion int
	Runtime       runtimeSection
	Elevation     elevationSection
	// Extra holds every top-level section other than schema_version,
	// runtime, and elevation, exactly as decoded from the file. These are
	// valid future 08 §3 sections this ticket does not own; they are
	// preserved for round-tripping (schema_version rewrite) and are never
	// validated or defaulted here.
	Extra map[string]interface{}

	sources map[string]ConfigSource
	// rawTree is the full decoded document (env overrides applied, before
	// Home/DataDir were stamped in) — kept only so the schema-upgrade
	// rewrite can round-trip every section without polluting the file
	// with derived, non-file-backed values.
	rawTree map[string]interface{}
}

// Source reports which precedence level produced key's effective value.
// An unknown key reports SourceDefault, matching "nobody set this".
func (c *Config) Source(key string) ConfigSource {
	if c == nil || c.sources == nil {
		return SourceDefault
	}
	if s, ok := c.sources[key]; ok {
		return s
	}
	return SourceDefault
}

// EffectiveEntry is one row of `cascade config list --effective`: the
// fully dotted key, its resolved value, and the source that produced it.
type EffectiveEntry struct {
	Key    string       `json:"key"`
	Value  interface{}  `json:"value"`
	Source ConfigSource `json:"source"`
}

// EffectiveEntries returns c's full effective view, sorted by key.
func (c *Config) EffectiveEntries() []EffectiveEntry {
	values := map[string]interface{}{}
	values["schema_version"] = int64(c.SchemaVersion)
	values["runtime.profile"] = string(c.Runtime.Profile)
	values["runtime.home"] = c.Runtime.Home
	values["runtime.data_dir"] = c.Runtime.DataDir
	values["elevation.allow_remote"] = c.Elevation.AllowRemote
	values["elevation.helper_pubkey"] = c.Elevation.HelperPubkey
	flattenTree(c.Extra, "", values)

	entries := make([]EffectiveEntry, 0, len(values))
	for k, v := range values {
		entries = append(entries, EffectiveEntry{Key: k, Value: v, Source: c.Source(k)})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

// LoadOptions carries every external input Load needs. All accessor
// fields are optional in production (nil falls back to the real process
// environment), but tests (Art.7.1) must always supply Getenv/Environ
// pointing at fakes and a Path under t.TempDir().
type LoadOptions struct {
	// Path is the resolved config.toml location (PathProvider.ConfigPath()
	// in production). Load never creates this file when it is missing —
	// it only rewrites it in place when a schema upgrade mutates it.
	Path string
	// ProfileFlag is the --profile flag value, or "" when unset.
	ProfileFlag string
	Getenv      Getenv
	// Environ matches os.Environ's signature.
	Environ func() []string
	// Warn receives a formatted message for every unknown key found
	// inside a section this ticket owns (warn-and-preserve, never a hard
	// error). A nil Warn discards messages.
	Warn func(format string, args ...interface{})
}

// Load reads and validates config.toml, resolves the active profile, runs
// the schema_version upgrade-rewrite frame, and applies generic
// CASCADE_<SECTION>__<KEY> env overrides on top of the file. A missing
// file is not an error: every field resolves to its default.
func Load(ctx context.Context, opts LoadOptions) (*Config, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	environ := opts.Environ
	if environ == nil {
		environ = os.Environ
	}
	warn := opts.Warn
	if warn == nil {
		warn = func(string, ...interface{}) {}
	}

	tree, sources, err := readAndUpgradeTree(opts.Path)
	if err != nil {
		return nil, err
	}

	// Generic CASCADE_<SECTION>__<KEY> env overrides.
	for k, v := range collectEnvOverrides(environ()) {
		treeSet(tree, k, v)
		sources[k] = SourceEnv
	}

	elevation, err := parseElevationSection(tree)
	if err != nil {
		return nil, err
	}

	profile, profSource, err := resolveRuntimeSection(tree, opts.ProfileFlag, getenv, warn)
	if err != nil {
		return nil, err
	}
	sources["runtime.profile"] = profSource

	return &Config{
		SchemaVersion: schemaVersionOf(tree),
		Runtime:       runtimeSection{Profile: profile},
		Elevation:     elevation,
		Extra:         extraSections(tree),
		sources:       sources,
		rawTree:       tree,
	}, nil
}
