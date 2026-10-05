# Derived v1 home

This dot-free home derives only from the committed S-53 inputs under
`internal/migration/v1/testdata/v1-goldens/`. All domain files are byte copies
except `vault.env`: replace each assignment name, in source order, with
`MIG_FIXTURE_SECRET_<n>` (one-based). Preserve comments, quoting and values.
The source vault names must never reach a custody backend.

SHA-256 entries below describe the committed bytes. Source paths are relative
to the repository root. The manifest is derived metadata; its counts are
independently recomputed by tests. Config has one `source_keys` entry and two
`planned_changes`: the translator adds exactly `schema_version`. Other domains
have one count each. This README is the checksum index itself.

Fixture | Source | SHA-256
--- | --- | ---
`dot-cascade/accounts/accounts.json` | `internal/migration/v1/testdata/v1-goldens/accounts/accounts.json` | `b4c0b0e9f8cbebcc63ecf1cf524d63f075cffaeb2bda134f8cc4f2ef7af1b842`
`dot-cascade/config.toml` | `internal/migration/v1/testdata/v1-goldens/config/daemon-known.toml` | `b4456f17174cf631b200e0b798e1842ec51b8796d5f192dcf93ae75b73d5e30f`
`dot-cascade/memory/decisions.md` | `internal/migration/v1/testdata/v1-goldens/memory/decisions.md` | `566e2341ab1cd5aa4f78380c4c7c5b4b52c2b474f202dae4b7e6608ba1c99d05`
`dot-cascade/memory/feedback-redacted.md` | `internal/migration/v1/testdata/v1-goldens/memory/feedback-redacted.md` | `71e9031d890263315dec9939cea2aca5ca21151ad2b25e8ed865d7c804d11880`
`dot-cascade/memory/lessons.md` | `internal/migration/v1/testdata/v1-goldens/memory/lessons.md` | `b7a09d30839564b450e3e23904f259c674a2033e1d9a6ea89e73a3d06b23e8dc`
`dot-cascade/memory/patterns.md` | `internal/migration/v1/testdata/v1-goldens/memory/patterns.md` | `cebc25bc841f4b47b0fabb1c84ebaae780cd47585ad0f594d817bafba8f94705`
`vault.env` | `internal/migration/v1/testdata/v1-goldens/vault/vault.env` | `b082050543910cc34f9e6a1057aeb31c396287cd042043621e3ff838a4e18c1d`
`manifest.json` | `derived` | `dd3997a86ef3bf7716b160707952b65c380065e926d812902dfe8cb0ea7944be`

Tests copy `dot-cascade/` to `.cascade/` and put `vault.env` at
`.cascade/vault.env` under a temporary home. For the nonsecret derivative,
each `REDACTED` occurrence becomes `NONSECRET-<relative-path-slug>-<n>` where
the slug replaces each run of non-alphanumeric characters with a hyphen and
n is the one-based occurrence within that file. Tests log all substitutions.
