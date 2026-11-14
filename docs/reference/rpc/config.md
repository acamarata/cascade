# Config RPC methods

Methods over the daemon's configuration. Access classes and elevation are
listed in the generated RPC method reference.

### `config.accept_loosening`

Accepts the current `config.toml` as the new guarded baseline, after the
owner has loosened a guarded setting. Elevated: the call always needs a
verified elevation bound to the exact params, else it fails with
`elevation-required` and nothing changes.

Params: exactly `{"config_sha256": "<64 lowercase hex>"}`; anything else is
`invalid-input`. The digest must match the file on disk: no `config.toml` is
`invalid-input`, and a different digest is `conflict`. The file must load,
both with and without environment overrides; an environment override that
changes a guarded value is `conflict` and must be unset first. The baseline
checker then records the acceptance against the verified attestation.

Result: `{"sections_hash": "...", "reload": {...}}`. `reload` holds
`accepted` (whether the live reload applied the config), `restart_required_keys`
(settings that only take effect after a restart, sorted) and `elevation_ref`.
