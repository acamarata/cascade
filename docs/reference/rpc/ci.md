# CI RPC methods

Methods for the CI watch and repository registration. Access classes and
elevation are listed in the generated RPC method reference.

### `ci.repo_register`

Registers a local repository with CI and records its baseline tips. The call
needs a verified local elevation for this method, else `elevation-required`.

Params: `path` (the repository) and `baseline` (a list of refs; may be empty).
The registration is stored in the daemon database and signed with the
daemon's envelope signer. When `baseline` is given and the repository already
has a different recorded baseline, the call fails with a `conflict` error
whose data carries the existing registration.

Result: `RepoID`, `RegisteredAt`, `Detached`, `Baseline` (the stored tips, each
`{Ref, Commit}`, always a list) and `Excluded` (tips left out, each
`{Ref, Commit, Reason}`, always a list). These fields carry no JSON tags and
appear with these capitalised names.

### `ci.watch_replay_end`

No params. Returns `{"end": n}`, the highest sequence number among the CI
watch events already stored (checkpoint planned, dispatched, terminal and
aborted), or 0 when there are none. A client that is replaying the `ci` event
topic uses it as the boundary between history and live events. A failure to
read the event store is `unavailable`.
