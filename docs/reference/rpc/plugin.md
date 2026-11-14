# Plugin RPC methods

Methods for installing, changing and calling plugins. Access classes and
elevation for every method are listed in the
generated RPC method reference. `plugin.add`, `plugin.update`,
`perms.grant` and `perms.revoke` are elevated: each call needs an elevation
grant bound to the exact params, and a call without one fails with
`elevation-required` and changes nothing.

### `plugin.add`

Installs a plugin from a manifest.

Params: `id`, `manifest_bytes` (the manifest file contents) and an optional
`checksum`. The manifest must parse, and its id must equal `id`
(`invalid-input` otherwise). Registered only when the daemon has a plugin
store.

Result: `{"message": "installed <name> v<version>", "grants": [...]}`;
`grants` is omitted when empty.

### `plugin.update`

Updates an installed plugin to a new manifest, or re-verifies its trust.

Params (unknown fields refused): `id`, `checksum`, `manifest_bytes`, and the
optional `reverify` and `index_bytes`. `index_bytes` is accepted only together
with `reverify: true`. An update whose manifest id differs from `id` is
`invalid-input`; the checksum is verified against the manifest bytes.

Result: `{"message": "updated <name> to v<version>", "grants": [...]}`, or
`"re-verified <name> v<version>"` for a re-verification, where `grants` lists
the trust grants.

### `perms.grant`

Adds one capability grant to an installed plugin.

Params (unknown fields refused): `id` (the plugin) and `capability` (must not
be blank). An uninstalled plugin is `not-found`. The change is recorded as an
audited effect before it is applied, and the effect is marked failed if the
change cannot be saved. For a trusted process plugin the trust grants change
with the grants.

Result: `{"name", "grants_before", "grants_after"}`.

### `perms.revoke`

The inverse of `perms.grant`: same params, result, elevation and audit
behaviour, removing the capability instead of adding it.

### `plugin.search`

Searches the plugin catalog.

Params: optional `q`, matched case-insensitively against name and description.
Result: a list of entries, always a list and never `null`, drawn from the
built-in plugin set and, when a registry is configured, the verified registry
index. Registry entries carry `id`, `name`, `description`, `tags` and
`latest_version`; built-in entries also carry `runtime`. A registry that is
configured but erroring fails the call with `unavailable`; a registry index
that fails verification is skipped with a logged warning and only the built-in
entries are returned.

### `plugin.intent`

Dispatches a named plugin intent.

Params (unknown fields refused): `intent_name` and `input` (any JSON). Result:
`{"result": <the intent's JSON reply>}`. A reply that is not valid JSON fails
with `internal`. A daemon with no intent dispatcher answers `unavailable` and
dispatches nothing.

### `plugin.review.review`

Runs a code review over a diff.

Params: `level` (`CR-A`, `CR-B` or `CR-C`; anything else is `invalid-input`)
and `diff`. Params are required. Result: the review provider's reply, with
`Findings` (each with `Severity`, `File`, `Line` and `Message`) and
`Approved`. These fields carry no JSON tags and appear with those names.

### `plugin.review.pr_diff`

Fetches the diff of a GitHub pull request for review. Owner CLI only: a
session principal gets `permission-denied`.

Params (one object, at most 512 bytes, unknown fields refused): `owner`,
`repo` and `number`, each checked against GitHub naming rules
(`invalid-input` on a bad value). Fetches are rate limited and a burst fails
with `quota-exhausted`.

Result: `{"diff": "...", "bytes": n}`. A diff over 8 MiB is `invalid-input`,
and a reply whose reported length differs from the diff is `integrity`.
