# Bundle RPC methods

Methods for installing a policy bundle for a repository, in two steps:
`bundle.install` records an approval, and `bundle.activate` consumes it. Access
classes and elevation are listed in the
generated RPC method reference. Params must be one object with no
unknown fields and no trailing data. `repo_id` must match
`[A-Za-z0-9][A-Za-z0-9_-]{0,127}` and `hash` is 64 lowercase hex characters;
otherwise the call is `invalid-input`. Per-repository state is kept as one
JSON record in the daemon's bundle directory; a daemon without one answers
`unavailable`.

### `bundle.install`

Approves one bundle hash for a repository. Elevated: the call needs a verified
elevation bound to its params, else `elevation-required`.

Params: `repo_id`, `hash` and `policy_version` (`[A-Za-z0-9][A-Za-z0-9._-]{0,63}`).
The approval is held in memory, one per repository (a new one replaces the
old), and expires after 10 minutes. Nothing is installed yet.

Result: `repo_id`, `hash`, `policy_version` and `expires_at`.

### `bundle.activate`

Installs the bundle approved by `bundle.install`. Params: `repo_id` and `hash`
(required). The pending approval is consumed whatever the outcome. It is
`permission-denied` when there is no approval for the repository, when it
covers another hash, or when it has expired.

On success the previous installed hash becomes `previous_hash`, the new hash
is installed with the approved `policy_version`, `rolled_back` is cleared, and
the record is saved. Result: `repo_id`, `installed_hash`, `previous_hash`,
`policy_version`, `rolled_back`.

### `bundle.status`

Params: `repo_id`. Returns the same fields as `bundle.activate`. A repository
with no installed bundle is `not-found`; a record that does not read back as
that repository's own is `integrity`.

### `bundle.rollback`

Marks the installed bundle as rolled back. Params: `repo_id` and `hash`, which
must be the installed hash. `permission-denied` when it is already rolled
back, when another hash is installed, or when more than 10 minutes have passed
since activation. A daemon without an audit log answers `unavailable`. The
rollback is written to the audit log before the record is updated.

Result: the same fields as `bundle.status`, with `rolled_back` true. The
handler only sets the flag; it does not itself reinstall `previous_hash`.
