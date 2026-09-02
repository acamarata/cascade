// Package runtime holds process bootstrap, the local|server|worker
// profiles, and configuration loading for the Cascade binary.
//
// Bootstrap wires three pieces together, in order: profile resolution
// (Profile, ResolveProfile — flag > CASCADE_PROFILE env > config.toml
// [runtime].profile > default "local"), path layout (PathProvider — the
// only place allowed to call os.UserHomeDir), and the TOML config loader
// (Load — warn-and-preserve unknown keys, generic CASCADE_<SECTION>__<KEY>
// env overrides, and the schema_version upgrade-rewrite frame). Every
// crossing function takes a context.Context first argument and never
// stores it in a struct; every domain type reads time through the
// injectable Clock interface, never time.Now() directly
// (02-TARGET-STRUCTURE §v1.1).
//
// This package is the bootstrap layer every later subsystem's startup
// path reads its paths and settings from (08-INIT-CONFIG-SPEC §2-3); see
// Bootstrap for the composed entrypoint.
package runtime
