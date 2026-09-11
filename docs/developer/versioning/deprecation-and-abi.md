# Deprecation policy and manifest ABI stability

## The one-minor warning window

A deprecated surface warns for one full minor release before it is
removed. Concretely: a symbol, flag, config key, or CLI verb marked
deprecated in `v2.3.0` may be removed no earlier than `v2.4.0`. It is
never removed in the same minor release that deprecates it, and it is
never removed silently. This is the general deprecation-cycle rule this
project follows; the `pkg/` Go SDK's specific application of it (which
package layout counts as public, and what "addition" vs "removal" means
in Go terms) lives in the sibling
[api-stability/compat-promise.md](../api-stability/compat-promise.md) -
that page applies this one-minor window to `pkg/`, it does not restate or
redefine it.

A deprecation notice names: what is deprecated, what replaces it (if
anything), and the release it will be removed in. That release is always
at least one full minor version ahead of the release that announced the
deprecation.

## Plugin manifest ABI: cascade.plugin/v2

The plugin manifest schema, `cascade.plugin/v2`, is stable across every
`v2.x` release. A manifest a plugin author writes against `cascade.plugin/v2`
today is valid for the entire `v2.x` line; the host never mutates that
schema in place.

A breaking change to the manifest shape is never made by editing
`cascade.plugin/v2`'s existing fields. It requires a new schema version
id (`cascade.plugin/v3` or later). The host may support multiple manifest
schema versions concurrently during a transition; it never silently
reinterprets an existing `cascade.plugin/v2` manifest under new rules.

This is a stronger promise than the general one-minor window above: the
manifest schema does not get a one-minor deprecation cycle for breaking
changes, because there is no breaking change to `cascade.plugin/v2` at
all. The only breaking-change path is a new schema id, which existing
`cascade.plugin/v2` manifests are entirely unaffected by.

## Crash reporting posture

Crash reporting follows the same default-off, opt-in posture as
telemetry (see [crash-reporting.md](crash-reporting.md)); it is not part
of the ABI or deprecation promise above and is documented separately.
