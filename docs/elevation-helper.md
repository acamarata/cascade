# Elevation helper (macOS)

`cascade-elevate-helper` is the small cgo program that lets a release Cascade
binary use the macOS Keychain and LocalAuthentication. A release `cascade` is
built with `CGO_ENABLED=0` and cannot call either. The helper can, for exactly
one JSON request per run.

## Where it lives

The helper must sit in the same directory as the real `cascade` executable
(symlinks to `cascade` are resolved first). Cascade never looks it up on `PATH`
and no environment variable names it. Cascade refuses a helper that is:

- a symlink, a directory or any other non-regular file;
- group- or world-writable;
- owned by a user other than the current user or root;
- missing the execute bit.

Any refusal means the darwin helper custody source is unavailable. Cascade then
treats elevation as unavailable and every elevated verb refuses. The source is
not registered yet either (see [Registration](#registration)), so today darwin
refuses elevated verbs whatever the helper reports.

## Mutual signature check

Both sides check each other's Developer ID code signature.

- Cascade uses the helper only when the helper is signed by the same team as
  the running Cascade. Cascade reads its own team from the signature of its
  running process (by pid, not from the file at its path), and every
  `codesign` check on a process shows the signature first and verifies last,
  pinned to the code directory hash that was shown, so only the running image
  can pass. The injection and debugging entitlements are refused inside the
  requirement itself (`!(entitlement["..."] exists)`), so they are judged on
  the running image too, not read from the file. It checks the helper file and its directory chain, then starts the helper in its own
  process group and checks the process that is actually running before it
  opens the reply listener or writes a single request byte. A helper swapped
  after the file check fails the second check and is killed with its group. A
  gate that already holds bytes, or whose other end closed, is refused.
- The helper serves key operations only to a caller signed by the helper's own
  team. It identifies the caller from the audit token of its connection to the
  listener (pid and pid version), read when it connects and again for every
  request: the two reads must be identical and both must satisfy the
  requirement. An unsigned, ad-hoc signed or foreign-team caller gets
  `permission-denied` before any Keychain call.
- The requirement pins the Developer ID intermediate, the Developer ID
  Application leaf and the code-signing identifier (`cascade-elevate-helper`
  for the helper, `cascade` for the caller), so an Apple Development
  certificate of the same team and another binary of the same team do not
  pass. The release signing step must set those identifiers (`codesign -i`) and
  sign both binaries with `--options runtime`.
- The helper requires the hardened runtime on its caller, so `DYLD_*` injection
  into the caller is blocked (read from the running process's dynamic status).
  Cascade requires the hardened runtime on the helper for the same reason, and
  both requirements refuse the injection and debugging entitlements
  (`allow-dyld-environment-variables`, `disable-library-validation`,
  `get-task-allow`, `debugger`).

A source build or an unsigned build gets no darwin elevation. Development
builds can use the dev-keys file source instead.

## Protocol

Cascade opens a unix listener in a fresh private directory and starts the
helper with one end of a socketpair as fd 3. Stdin and stdout are `/dev/null`
and the environment is empty. Fd 3 is only a gate: once cascade has verified the
running helper, it opens the listener and writes its path there. The helper connects to that
listener, and cascade accepts only a connection whose peer pid is the helper
process it started and verified. One request line (at most 64 KiB) goes in on
that connection, one response line comes out, and the helper exits. Bytes
written into an inherited descriptor by a program that later became the helper,
or by a descendant that kept the gate, never reach cascade: the gate must be
empty when cascade is ready to use it, the helper runs in its own process
group that is killed when the exchange ends, and a connection from any other
pid is closed unread.

| Request | Success reply |
|---|---|
| `{"v":1,"op":"status"}` | `{"v":1,"ok":true,"available":b,"tier":"os-keychain","cgo":b}` |
| `{"v":1,"op":"generate"}` | `{"v":1,"ok":true}`; kind `conflict` when a key is already enrolled |
| `{"v":1,"op":"pubkey"}` | `{"v":1,"ok":true,"pub_b64":"..."}` |
| `{"v":1,"op":"sign","payload_b64":"..."}` | `{"v":1,"ok":true,"sig_b64":"..."}` |
| `{"v":1,"op":"delete"}` | `{"v":1,"ok":true}` after a LocalAuthentication presence check |

Failures are `{"v":1,"ok":false,"kind":"<kind>","message":"..."}`. Unknown ops,
bad JSON, unknown fields, an oversize line and a payload over 4096 bytes are
`invalid-input`. There is no caller-supplied prompt text: the helper builds the
sign prompt as `Cascade elevation <first 12 hex of sha256(payload)>` and Cascade
can show the same digest next to the request (`elevation.HelperPromptReason`
gives the text). `status` answers any caller. Run by hand (fd 3 not open), the
helper serves `status` only. `status` reports `available: true` only when the
helper can use LocalAuthentication and carries a storage entitlement
(`keychain-access-groups` or `application-identifier`, which come from an
embedded provisioning profile). `generate` runs only when both Keychain items
are definitely absent; any other state, including one it cannot read, is
refused, because storing replaces an existing item without a presence check.

## What the probe found

A throwaway-service probe stored a Keychain item with the same access control
the keystore uses (user presence, this device only, when unlocked). An unsigned
build and an ad-hoc signed build both failed with `-34018`
(`errSecMissingEntitlement`); the follow-up read and delete found nothing, and
the item was absent afterwards. Outcome: **NEEDS_PROFILE**. The data-protection
Keychain wants `keychain-access-groups` or `application-identifier`, which on
macOS come from a provisioning profile. The Developer ID plus hardened runtime
variant could not be run on the build host (no Developer ID identity there). The
same rule is expected to apply to it.

Consequence: until a Developer ID build with an embedded profile is shipped and
proven, the helper reports `available: false` and darwin keeps refusing
elevated verbs. Nothing falls back to a weaker store.

## Registration

The helper, its protocol and the cascade-side keystore ship, but the darwin
helper custody source is **not registered**: `DefaultSources` on darwin lists
only the in-process platform keystore and the file source, so no elevated verb
can reach the helper. Registration is one line in
`internal/elevation/custody_sources.go` and waits for two things:

1. the Developer ID provisioning (signing identity and embedded profile), an
   owner step, so the helper can report `available: true` at all;
2. the live-identity residual below (`SIGSTOP` then exec under the same pid)
   being accepted as an ADR-0005 darwin residual.

Until both hold, the helper's behaviour is tested through the keystore
directly, not through the production source list.

## Limits

- **Unattended signing.** `sign` and `delete` show a LocalAuthentication prompt.
  A session with no one at the machine cannot complete them, by design.
- **Same-user replacement.** The directory that holds the binaries is usually
  writable by the installing user. A process running as that user can replace
  the pair. The signature checks make a replacement fail unless it carries the
  same team's Developer ID signature, which that process cannot produce.
  Cascade refuses a helper whose directory, or any directory above it, is
  owned by another user or writable by group or world (sticky directories such
  as `/tmp` are fine), because someone who can write there could swap the file
  between the checks and the exec and the swapped program would run as this
  user before its signature is rejected (darwin has no way to exec a verified
  file descriptor). The same applies to a directory on a volume mounted to
  ignore ownership (every file there looks owned by the caller) and to one
  with an extended ACL entry that allows anyone to write (read with
  `ls -lde` on macOS). That rules out a group-writable package-manager bin
  directory such as `/Applications` or a shared Homebrew prefix; install the
  pair under a directory only this user (or root) can change.
- **Residual: the caller's identity is checked live.** The audit token the
  helper reads names whichever image is running at the caller's process id NOW,
  not the image that opened the connection. The pid version changes when that
  process execs another binary, which the two reads in the helper catch, but a
  same-user process that stops the helper (`SIGSTOP`) before its first read,
  executes an installed signed Cascade under the same pid and resumes it can
  still be taken for Cascade, and the same window admits a peer whose last
  operation before the helper's first read was made by some other process
  running at a genuine Cascade's pid. `pubkey` and the first `generate` need no
  prompt; `sign` and `delete` still show the LocalAuthentication prompt, whose
  text carries the payload digest (Cascade does not print that digest next to
  the request yet, so the owner cannot match the two). This needs an owner
  decision on ADR-0005 before the source is registered (see
  [Registration](#registration)); the real exec-after-write leg has not been
  run against a signed helper.
- **Assumption: the pid on the accepted connection.** Cascade identifies the
  helper's connection by the peer pid (`LOCAL_PEERPID` on darwin, which is the
  pid of the process that last operated on the peer socket; `SO_PEERCRED` on
  linux). The listener opens only after the running helper is verified, so no
  connection can predate that; whether darwin refreshes the pid when another
  process later connects a socket created earlier under the helper's pid has
  not been tested.
- **One architecture per helper.** The check pins the code directory hash that
  `codesign -dvvv` shows, which for a universal binary is one architecture's
  hash, so a universal helper is refused. Ship a thin helper per architecture.
- **Prompt wait.** The `delete` presence prompt gives up after 55 seconds. The
  `sign` prompt is the in-process keystore's own and has no separate bound in
  the helper; cascade stops waiting for it after 60 seconds.
- **Checks before the exchange are bounded one by one.** Each `codesign` and
  `ls -lde` call has its own 5-second limit, outside the exchange deadline, so
  a slow host can take tens of seconds before a refusal. Every such timeout
  refuses; none grants custody.
- **Two enrollments racing an owner-approved delete.** Enrollment is not
  serialized across helper processes. If two `generate` requests overlap an
  owner-approved `delete`, the Keychain can end up holding one enrollment's
  private key and the other's public key. Every later signature then fails
  verification (fails closed); the owner re-enrolls after a `delete`.
- **Peer sees only the signature.** A correctly signed Cascade that is itself
  compromised can ask for signatures. The LocalAuthentication prompt per
  signature is the control for that case.
