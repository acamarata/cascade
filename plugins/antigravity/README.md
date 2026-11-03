# Antigravity harness plugin

`antigravity` is a builtin plugin with one command: `detect`.
Importing this package registers its manifest and handlers through
`pkg/plugin.RegisterBuiltin`. The TOML manifest mirrors the Go manifest.

`Detect()` probes PATH for the `agy` executable and returns a `Detection`
and an error. A successful lookup reports `Present: true` and the resolved
`Path`. A missing executable reports an empty result with no error.
Permission failures and other lookup errors propagate with their causes
preserved and a Cascade error kind. The command handler propagates those
errors; callers needing the presence and path use `Detect()` directly.

The probe follows the OpenCode plugin's filesystem-only lookup pattern,
including PATHEXT candidates on Windows and skipping empty PATH entries.
It does not execute the CLI. This plugin only detects the executable;
instruction file installation and removal are deferred to P2.

Tests inject `lookPath` and use a temporary HOME, so `agy` need not be installed:

```sh
go test -count=1 -run '^TestAntigravity(DetectReportsAgyPath|ManifestMatchesCode)$' ./plugins/antigravity/
```
