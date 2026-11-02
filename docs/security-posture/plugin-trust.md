# Process-plugin trust

A process plugin runs as its own executable, so installing one is a trust
decision. Cascade records that decision in one place and grants it from one
kind of evidence: an elevated `cascade plugin add` or `cascade plugin update`
that the daemon verified for that exact call.

## What makes a record trusted

A process-tier install record becomes trusted only when all of these hold:

- The daemon's elevation middleware verified a signed attestation for this
  call, and its witness names the verb being performed (`plugin.add` or
  `plugin.update`). A witness for one verb says nothing about another.
- The witness covers the hash of the exact raw params the handler received.
  Params changed after verification are refused.
- Those params decode to the plugin id, checksum and manifest bytes being
  installed. Bytes that parse to another manifest, or name another id, are
  refused.
- The checksum is not empty and is the SHA-256 of the manifest bytes,
  checked on the daemon side.
- The plugin's executable sits beside the `cascade` binary, named after the
  plugin id (`.exe` on Windows); an id that is not a valid manifest id
  (`[a-z][a-z0-9-]*`) is refused. It must be a regular file, not a symlink,
  and on Unix not group- or world-writable. Windows has no writability check:
  any regular file is accepted there, and the install directory's ACL is what
  protects it. There is no PATH lookup and no environment override.

The record then carries `trust_tier: trusted`, the executable's SHA-256 as
`exec_digest`, and the manifest's grant set as `trust_grants`. Nothing is
launched at install time. Any failed condition refuses with a policy-denied
error naming the condition, and writes nothing.

## What does not make a record trusted

- An unelevated call, or a witness for another verb or other params.
- The cascade-pa install flow's own elevation witness. A process-tier plugin
  found through that flow is refused with a pointer to `cascade plugin add`.
- A `perms grant`, or any other edit of a record's stored grants. A process
  plugin's authority (network scopes, token environment, MCP exposure) is its
  stored grants intersected with `trust_grants`, so widening the stored grants
  adds no authority, and an untrusted process record has none. (Writing the
  daemon's database directly is outside this boundary; see Limits.)

## Updates

A non-elevated update of the same runtime keeps `trust_tier`, `exec_digest`
and `trust_grants` exactly as they were; it can narrow the stored grants but
never widen trust. Whether an update widens grants is measured against the
plugin's effective grants, so stored grants widened by `perms grant` never
hide a widening. A non-elevated update of a trusted record also needs a
checksum that is the SHA-256 of the new manifest bytes, which becomes the new
pin; without one it is refused and nothing is written.

An elevated `plugin.update` of a process-tier manifest checks, in order: the
manifest parses and names the requested id; the trust grant, under the same
rules as an add (any failure is the policy-denied error naming it); the
checksum; the installed record; then the process handshake. An elevated
update to another runtime records no trust.

After upgrading `cascade`, a plugin's sibling executable usually changes, so
it no longer matches the recorded `exec_digest`, and the manifest version
often stays the same. The launch refusal names the recovery:
`cascade plugin update <id> --reverify`, for example
`cascade plugin update cascade-github --reverify`.

`--reverify` is not a second trust source. The CLI fetches the registry index
and the installed version's manifest and sends both through the elevated
`plugin.update` RPC. The daemon re-pins `exec_digest` only when every link
holds:

1. The elevation witness names `plugin.update` and covers these exact params.
2. The record exists, is a process plugin, was trusted before (it carries a
   prior `exec_digest`) and is installed at the manifest's version.
3. The index verifies with the registry key (the configured one or the
   embedded one) and is not a rollback (sequence 0, an older sequence, or the
   same sequence with other bytes is refused).
4. The index entry for this id and version has a checksum equal to the
   call's checksum and to the sha256 of the manifest bytes.
5. The manifest's signed `exec_sha256` for this `<goos>/<goarch>` equals the
   sha256 of the resolved sibling executable (a symlink or a
   group/world-writable file refuses).

Any failure is policy-denied, names the failed link and writes nothing; the
granted capabilities (`trust_grants`) never change. A manifest without an
`exec_sha256` entry for the platform refuses with `no signed executable digest
for <platform>`; publishing those digests for first-party plugins is a
release-pipeline job. Until a plugin's digests are published, the fallback
is the old one: `cascade plugin remove <id>`, then an elevated
`cascade plugin add <id>`.

## Limits

- The executable digest is pinned at grant time and checked again at every
  launch. A process plugin starts only through `LaunchInstalled`
  (`internal/plugins/trusted_launch.go`): the record must be enabled and
  trusted, the sibling executable is resolved again (missing, not a regular
  file, a symlink, or group/world-writable refuses), and its sha256 must equal
  `exec_digest`. A mismatch is refused as policy-denied, naming
  `cascade plugin update <id> --reverify`. A same-user process can still swap
  the file between the hash and the exec, or before a crash respawn (which
  runs the same path again without hashing it); that is not a boundary this
  release claims.
- The child's environment is a closed allowlist: `PATH`, `HOME`, `TMPDIR`,
  `LANG`, `LC_ALL` and `TZ` when set, plus `CASCADE_GITHUB_TOKEN`,
  `GITHUB_TOKEN` and `GH_TOKEN` for `cascade-github` only when its effective
  grants hold `oauth:repo`. The principal token, the owner-CLI marker and
  provider keys never reach a child.
- Every launch and every call writes an effect intent before it runs and a
  confirmed, failed or unknown-outcome record after. A recorder holds a lock
  file under `<data dir>/plugin-effects/` for its whole life; when the effect
  log is next opened, intents whose recorder is gone are marked
  unknown-outcome and never re-run. A call whose params carry secret values
  records a digest over their tagged form instead of a hash of the plaintext.
- The daemon's plugin store is the trust root. A record is not signed, so a
  process that can write the daemon's database directly can forge one; the
  RPC surface cannot.
- The executable's owner and its directory's permissions are not checked. The
  directory beside the `cascade` binary is trusted as installed; a user who
  can replace files there can already replace `cascade` itself.
- An update of a process-tier plugin is confirmed by launching the candidate
  through the same launcher and completing the handshake. Under an elevated
  `plugin.update` the candidate must match the digest that update just
  pinned; a non-elevated update must still match the stored digest. A failed
  handshake rolls the update back and leaves the record unchanged.
