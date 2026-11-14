# Sync RPC methods

Methods over node-to-node sync. Access classes and elevation are listed in the
generated RPC method reference. For the sync model see
[sync](../../sync.md). Params for all four methods must be one JSON object (an
empty or `null` body is accepted); unknown fields are `invalid-input`. Status
and conflict methods answer `unavailable` when the daemon has no sync engine.

### `sync.status`

No params. Result: `peer_tier`, `domains` and `open_conflicts`. `domains` has
one entry per core sync domain, sorted by domain then subkind, each with
`domain`, `subkind`, `class`, `strategy`, `eligible`, an optional `reason` when
it is not eligible, `position` (the sync cursor) and `position_known`.

### `sync.run`

Runs a sync pass.

Params: optional `domain` and `subkind`. With neither, every domain that is
eligible for this peer tier is synced. With `domain` set, `subkind` defaults to
the domain name; an unknown domain is an error, and a domain this tier may not
sync is `policy-denied`, naming what it may sync. A daemon with no run path
wired answers `unavailable` instead of reporting a sync that did not happen.

Result: `{"domains": [...]}`, each entry with `domain`, `subkind`, `synced` and,
on failure, `error`. A failure in one domain is reported in its entry and does
not stop the others.

### `sync.conflicts_list`

No params. Result: `{"conflicts": [...]}`, the open conflicts in the conflict
journal.

### `sync.conflicts_resolve`

Resolves one open conflict. Elevation is conditional: a call that keeps the
server copy (`keep: "server"`) needs none, and any other call, including one
whose params cannot be read, needs an elevation grant.

Params: `record_id` (required) and `keep`, `server` or `local`; anything else
is `invalid-input`. An unknown conflict id is an error.

Keeping `server` returns `{"record_id", "keep": "server", "elevated": false}`.
Keeping `local` discards the server's copy, so the handler asks the elevation
gate to authorize the discard first; with no gate wired it fails with
`elevation-required`. On success it returns `{"record_id", "keep": "local",
"elevated": true}`.
