# Security posture

This file is the threat model, firewall description and injection-boundary
reference for cascade. It is created and owned by the security-documentation
ticket in the release epic; other tickets seed the section they implement.

## Vault backends

Cascade's secret vault stores values through one custody backend, chosen at
runtime. The selection order is fixed:

| Order | Backend | Platform | Notes |
|---|---|---|---|
| 1 | macOS Keychain | darwin | Reached by running `/usr/bin/security` as a subprocess (`add-generic-password`, `find-generic-password`, `delete-generic-password`). There is no Security.framework linkage. |
| 2 | freedesktop secret service | linux | Spoken over D-Bus in pure Go against `org.freedesktop.secrets`. There is no libsecret binding. |
| 3 | Encrypted file vault | every platform | The fallback, and on Windows the only backend. |

**No CGO anywhere.** Both native backends talk to the OS through a subprocess or
a wire protocol rather than a C binding, so every backend is present in a
release binary built with `CGO_ENABLED=0`. A cgo-gated backend would be silently
absent from every shipped artifact, which is the failure this rule exists to
prevent.

**The selection is reported, not hidden.** Whichever backend answers, its name
appears in `cascade vault list --json` and in every `vault set`/`import` result.
A host that fell back to the encrypted file vault says so; it never presents
itself as having used the OS keychain.

**Failing closed.** A backend that cannot reach its dependency (a locked
collection, an unreachable session bus, a keychain that refuses without a
prompt) returns a typed refusal. None of them returns an empty result. This
matters most for `list`: an empty name list is an assertion that the vault holds
nothing, and a backend that cannot read its store must not make that assertion.
A store whose contents will not decode is an integrity refusal that leaves the
file untouched, rather than a fresh empty vault written over the old one.

### Encrypted file vault

The file vault holds every secret in one age v1 file (`vault.age`) in the
cascade data directory, encrypted with a scrypt passphrase recipient:
ChaCha20-Poly1305 over the age STREAM chunking, with the passphrase held in a
0600 key file (`vault.key`) beside it. The format is the real age v1 format, so
`age -d` opens the file with that passphrase; the test suite proves this by
decrypting a fixture the reference `age` tool produced.

The file vault is a fallback, and its security is the security of the key file's
file permissions. It does not have the OS keychain's property that a value is
held outside the process's own storage and gated on user presence. On a host
where an OS keychain is available, that backend is chosen first for exactly that
reason.

### Values never leave the vault by accident

- A value is never accepted as a command-line argument: `vault set` and
  `vault rotate` read it from stdin or from a named file, so it does not reach
  the shell history or the process table.
- On macOS the value is passed to `/usr/bin/security` hex-encoded, so it is not
  visible in the process table there either.
- `vault list` reports names only. It has no code path to a value.
- Reading a value (`vault get`) and replacing one (`vault rotate`) are elevated
  verbs: they require an enrolled elevation helper and a working local
  authenticator, and they are excluded from the MCP tool surface, so no agent
  can reach a secret value through a tool call.
- No error message, log line or report carries a value. The vault.env parser
  reports the line NUMBER of a line it cannot read and withholds the line
  itself. The macOS backend discards the `security` tool's diagnostics before
  wrapping a failure, because those diagnostics can quote the arguments the tool
  was given.

### Elevated verbs on a release binary

The elevated-verb gate needs a local authenticator, and the platform
authenticator backends are cgo-gated in the elevation domain. A release binary
built with `CGO_ENABLED=0` therefore has no authenticator available, and
`vault get` and `vault rotate` refuse on it with `ELEVATION_REQUIRED`, naming
that reason. Storage, listing and import continue to work: they are not elevated
verbs. Windows refuses the elevated verbs outright as a tier-2 platform, with a
typed unsupported error rather than a panic.

## Quarantine

Cascade scans content at its own internal boundaries for credential material
before that content is written down or sent anywhere. The scanner is **local
only**: `internal/secrets` imports nothing but the standard library, `pkg/cascade`
and a hash, on every path including error paths. A detector that reported
somebody's secrets to a network service would be a worse outcome than the leak
it was built to prevent, so the property is enforced by an import-boundary gate
over the whole package rather than by review.

### Three signals, and why entropy alone is never enough

| Signal | What it is | On its own |
|---|---|---|
| Pattern | A named shape from the registry: a vendor API-key prefix, a JWT triplet, PEM private-key armour, a URL authority with an inline password, a base64 blob that decodes to JSON naming a credential field. | Enough. These shapes do not occur by accident. |
| Entropy | Shannon entropy per character over value-shaped runs of 16 characters or more, above `[secrets] entropy_floor` (default 3.5 bits/char). | **Never enough.** Reported as a hint, always below the quarantine threshold. |
| Context name | A field named `key`/`secret`/`token`/`pass`/`cred`/`auth` within 64 bytes to the left. | Lifts an entropy run to quarantine-eligible, and supplies the `UPPER_SNAKE` name to store it under. |

Only a finding that reaches `[secrets] confidence_threshold` (default 0.8) is
quarantined. Both keys are validated on load and on reload; a rejected reload
leaves the running configuration in force rather than dropping to no rules.

### Where the dial is set, and what it costs

A missed secret is a leak and a false positive is an annoyance, so the design
leans toward catching things. It does **not** lean all the way, because a
detector that quarantines ordinary content gets switched off, and a switched-off
detector misses everything. Two exclusions are therefore deliberate and are
false negatives on purpose:

- **Structured identifiers.** A UUID, a 32-or-more-character pure hex string (a
  git object id, a checksum) or a pure digit run is capped below the threshold
  *even when a credential-named field sits beside it*. `request_token_id =
  <uuid>` is a request id. Quarantining every trace id and commit sha in a
  developer's notes is how the feature earns being disabled.
- **Alphabetic runs.** The entropy signal only considers runs that mix letters
  and digits. `getUserByIdentifier` and `AWS_SECRET_ACCESS_KEY` are above any
  usable entropy floor and are not credentials. A purely alphabetic secret with
  no vendor prefix is therefore invisible to the entropy signal — a pattern
  match still finds it.

The counterpart is the diagnostic-bundle redactor (`internal/doctor`), which is
tuned the opposite way: recall-first, over-redacting by design. That is correct
there, because over-redaction costs a reader a little context, while here it
costs the operator their trust in the tool.

### The quarantine ledger

Findings go to an append-only ledger under the profile data directory, mode 0600,
alongside a 0600 key file. A record carries the class, the byte offset and
length, the confidence, the suggested name, a caller-supplied source reference
and a **keyed** BLAKE3 fingerprint of the flagged bytes. It does not carry the
value: `QuarantineEntry` has no field that could hold one, and neither does
`DetectionHit`. The fingerprint is keyed with a per-store key so that a ledger
pasted into a bug report cannot be dictionary-attacked back to a short secret,
which a bare hash would allow.

A torn line costs that one record, not the file: refusing to read the whole
ledger because one line is damaged would hide every other finding from the
person who has to review them, and nothing in the ledger is ever overwritten.

### Getting out of quarantine

Every entry has two exits, and both are recorded:

```
cascade vault quarantine list                      # metadata only, never values
cascade vault set --from-quarantine <id>           # promote into the vault
cascade vault quarantine release <id>              # the detector was wrong
```

`vault set --from-quarantine` takes the NAME from the entry's suggested name and
reads the **value from stdin** (or `--value-file`), because the detector never
stored one — there is nothing to recover but the name and the location. Under
`CASCADE_NO_INPUT=1` with nothing piped in, promotion is a hard error rather
than a prompt nobody can answer. The entry is released as `promoted` only after
the vault write succeeds.

`quarantine release` retires a wrong detection. The entry stops appearing in
`quarantine list`; the ledger keeps the record that it existed and that it was
released. A quarantine with no way out is data loss, so recovery is part of the
feature rather than an afterthought.

## The egress firewall

Egress is the last boundary. Whatever the firewall misses leaves the machine,
and whatever it mangles the user sees as a broken tool. The two costs pull in
opposite directions, and the resolution is stated once here: on this path,
refusing to send is the safe failure.

### Two passes

Every outbound write goes through `Intercept`, which runs two passes in order.

**The substitution pass** replaces credential material with a typed
vault-reference tag. It runs in two stages of its own. First, an exact-substring
match over every value the vault holds, tagged unconditionally: no entropy
floor, no confidence threshold, no detector opinion. A secret the operator
stored is a secret whatever it looks like, and a shapeless passphrase is exactly
the case a shape-based detector misses. Second, the detector finds credential
material the vault has never seen, at the confidence the operator configured.

The grammar is the same one the turn rewriter uses, and there is no other:

```
<password>NAME</password>   <apikey>NAME</apikey>   <token>NAME</token>
<connstr>NAME</connstr>     <pii kind="ssn|dob|account">NAME</pii>
```

`NAME` is a vault reference, never a value. The placeholder encodes nothing
derived from the secret: not the bytes, not a hash, not the length. Two secrets
of different lengths stored under one name substitute to identical output, which
is what makes a length oracle impossible. Substituting already-substituted
content changes nothing further, and identical input gives identical bytes.

**The sensitivity pass** checks the tier the caller declared against what the
destination class admits:

| Declared tier | Admitted |
|---|---|
| `local-only` | only on a class whose registrant set `AllowLocalOnly` |
| `restricted` | only on a class whose registrant set `AllowRestricted` |
| `internal` | on any enabled class |
| `public` | always |

The tier is an explicit argument on every call. It is never derived from the
content and never carried on a context: a byte slice declares nothing about
itself, and a classification that can be lost by dropping a context is not a
classification. An unset or unrecognised tier resolves to `restricted`, so the
permissive answer is never the default one.

### The choke point

Registration is not advisory. Every outbound byte requires two things: a
registered class, and a capability the firewall issued. A caller cannot
construct a capability, because its only field is unexported and its only
constructor is the registry's. Classes are refused by default: an unregistered
class is refused before any content is examined, and a registered class that is
not enabled is refused with nothing written.

Refusals are total. On an unknown class, a disabled class, a missing capability,
a tier the class does not admit, content the rewriter cannot parse, a vault that
cannot be read, and a substitution that fails partway, the caller receives an
error and nothing to write. There is no path that hands back partially
substituted bytes.

### Registered classes

| Class | State | Owner |
|---|---|---|
| `mcp.response` | enabled | the tool-protocol response path |
| `hook.response` | enabled | the hook engine's outbound action crossing |
| `telemetry` | disabled, deferred | reserved so a caller gets a named refusal |
| `oauth` | enabled | the token-endpoint call the vault's broker makes |
| `provider-intake` | enabled | provider intake; health and recovery probes reuse it |
| `spike-measurement` | enabled | a measurement spike, deleted with the spike |
| `backup-target` | enabled, admits restricted | a remote backup destination |
| `plugin-remote` | disabled until the remote-runtime key is set | the remote plugin runtime |
| `registry-fetch` | enabled | the plugin registry fetch |

Telemetry egress is deferred. Nothing in this documentation, the command help or
the readme claims it is active, and the class refuses every call.

Each subsystem that later gains an outbound path registers its own class and
calls `Intercept` before writing. The package's own documentation carries the
full owner list, and a drift test fails when that list and the specification's
diverge by a single byte.

### The import allowlist

Network I/O is egress, so a package importing `net` or `net/http` must be one
that goes through the firewall. The arch gate enforces an allowlist and turns
red on a new importer that is on neither half of it: the set the ruling names,
and the set of packages that predate the ruling, each carrying the reason it is
still there. Naming them is what keeps the second list from becoming a place to
hide.

Spawning a process is not egress. It carries a separate, single-member list
bound by the driver-boundary rules, and the two lists are never merged: merging
them would let a `net/http` importer hide behind a process-spawn exemption. The
gate refuses both smuggling directions.

## Approval tokens

An ask-tier action reaches a human decision through the approval queue. What
comes back from that decision is an approval token: a signed statement that one
specific action, for one specific requester, was approved once.

**Ed25519, from the vault.** The signing keypair lives in the vault under
`cascade.approval.ed25519` and is loaded through the vault's non-elevated
internal read path, not through the elevated `vault get` verb. Signing an
approval must not itself raise an attestation prompt, or every approval would
cost two. The private key is never held between signatures: it is fetched,
used, and zeroed in the same call.

**Five minutes, hard.** A token expires no later than five minutes after it was
issued. The ceiling is not a default and not configurable; a caller cannot ask
for longer. Both the issue instant and the expiry come from the injected clock,
so an expiry check never reads the wall clock and never depends on the machine's
timezone.

**Verification refuses first.** Verification is stateless: it decodes, checks
expiry, then checks the signature. An expired token is refused whether or not
its signature is good, and bytes that cannot be decoded are refused before any
signature is examined. There is no input that produces a token and no error
without passing all of those.

**Single use is the ledger's job.** Verification proves a token is authentic and
current, not that it is unspent. Replay is caught at redemption by the
single-use ledger in the audit domain, which records the token's nonce. The two
responsibilities stay apart on purpose: a stateless verifier can run anywhere,
and only the controller can spend.

**Bridges carry a request id and nothing else.** When an approval decision is
taken on another device, what crosses the bridge is the request id alone. The
token, the nonce, the verb and the parameter digest all stay on the controller,
which looks the request up and verifies it locally. The projection is a struct
with one field, so widening the token cannot widen what a bridge sees.

**Local-only actions.** The bridge-approvable set is ask-tier actions at risk
L1-L2 and nothing else. Every elevated verb (vault get and rotate, approval and
standing-grant administration, backup key handling, plugin and permission
changes, node enrolment, sync conflict resolution that discards local state,
policy or sensitivity loosening, enabling remote elevation or the remote plugin
runtime, and purging data on uninstall) is local-only: it can never be approved
from another device, and it can never be covered by a standing grant. So is
anything on the deny-list. A standing grant naming either is refused before it
reaches storage.

## Approval-token record

The bytes a signature covers are a domain-separated canonical-JSON record, not
a bare hash of the parameters. Signing the parameters alone would let an
approval for one verb be replayed as an approval for another verb that happens
to take the same arguments.

**The signed fields** are exactly: schema version, key id, request id, verb,
parameter digest, requester, approver, origin, scope, target (node, session and
task), risk, sensitivity, policy version, audience, issue time, expiry and
nonce. There are no others, and a record carrying a field outside that list is
refused.

**One encoding.** Keys are sorted, there is no insignificant whitespace, and
numbers and timestamps have one written form each. Verification re-encodes what
it decoded and compares the result byte for byte with its input, so "canonical"
is a decidable question rather than a list of formatting rules. Unknown fields,
repeated keys and trailing content are each refused, and all of that happens
before the signature is checked.

**Domain separation.** The signing input is a fixed context string followed by
the canonical encoding. A signature made in another context does not verify as
an approval, and an approval signature does not verify anywhere else.

**Redemption is controller-only.** Nodes never redeem an approval; they relay to
the controller. The controller moves the record from approved to consuming with
an atomic compare-and-set under a unique (request id, nonce) constraint, so
exactly one caller wins, and the winner writes a stable execution id before
anything is dispatched. Executors de-duplicate on that id. A process killed
between the consume and the run recovers by re-driving the same execution id:
the consume is never lost, and a second execution is never issued.

## Clipboard fallback

The clipboard is the last-resort delivery channel for a value an operator must
paste into a target that cannot be automated. It is time-bounded and audited,
never an open API a caller can drop a secret into and walk away from.

**The 30-second auto-clear.** A write to the clipboard schedules a two-step
clear 30 seconds later: one write of a single ASCII space, then one
zero-length write, in that order. The second step exists to defeat
clipboard-history tools that would otherwise keep serving the last
non-empty entry after the first overwrite. Both steps run and are recorded
even if the first one's subprocess reports failure — a partially-successful
clear is still recorded as an attempted clear, not silently dropped.

**What is guaranteed and what is not, per platform.** On macOS, the write and
both clear steps are single stdin-pipe invocations of `/usr/bin/pbcopy`; a
process kill between write and clear is the gap the persisted pending-clear
record below exists to close. On Linux, the same two-step pattern runs
through `xclip -selection clipboard -i`; if xclip is absent or exits non-zero,
the write fails closed with a typed `ErrClipboardUnavailable` before anything
reaches the clipboard — there is no alternative delivery path, and Wayland
(`wl-copy`) support is an explicit, tracked P2 deferral rather than a silent
gap. **What clipboard delivery cannot guarantee on any platform**: a third
process that reads and re-copies the clipboard's contents during the
30-second window, before the clear fires, has already exfiltrated the value;
the clear guarantees the value does not persist past the window, not that
nothing observed it during the window. Nor can it guarantee it never clears a
value the user copied afterward — see the pending-clear contract below.

**Zero-payload audit contract.** Every write and every clear is recorded, and
neither record ever carries the payload, a hash of it, or its byte length —
only an opaque reference, the platform, and the write/clear timestamps. A
reader of the audit trail can see that a clipboard delivery happened and when
it was cleared; they can never recover or narrow down what was delivered from
the trail alone.

**Windows is refused, not degraded.** Windows is tier-2 scope (binary plus a
headless one-shot; no daemon clipboard service), and clipboard delivery is not
offered there at all. The refusal happens before any OS clipboard API is
touched — there is no partial-support fallback that might silently leave a
value on a Windows clipboard with no clear behind it.

## Clipboard gate

Clipboard delivery is a gated surface, not an open API: a caller cannot place
a value on the clipboard without a verified approval token, and the pending
clear survives a daemon restart rather than depending on the process that
armed it staying alive.

**Approval-token requirement.** A write refuses before any subprocess is
spawned unless it carries an approval token that verifies — the same
signature, expiry and domain-separation check every other approval consumes.
An absent, expired or forged token refuses with a typed
`ErrApprovalRequired` and touches nothing: no clipboard write, no persisted
pending-clear row, no audit event.

**The pending clear survives a restart.** The 30-second countdown is not only
an in-process timer. The moment a value is written, a durable row — an
opaque reference, the platform, the write time and the clear deadline, never
the payload, a hash of it, or its length — is persisted so a daemon crash
between write and clear does not leave a secret on the clipboard
indefinitely. On the next start, every persisted row is re-armed: one whose
deadline has already passed clears immediately, and one still within its
window is rescheduled for its remaining time.

**One calling verb.** The design admits exactly one caller: the elevated
`cascade vault get --clipboard` verb, which obtains the approval token before
calling in. No other command, plugin surface or RPC method is permitted to
place a value on the clipboard. As of this section's writing the verb itself
and the daemon-start re-arm call are not yet wired to the clipboard package
described above (`internal/secrets`); a CI grep is designed to fail on any
caller of `ClipboardWriter.Write` outside that verb once it exists, so the
one-caller rule is enforced by the time either lands rather than assumed.

## Action-boundary rehydration

A turn that has been through the privacy firewall carries typed tags where
credentials used to be: `<password>NAME</password>`, `<apikey>`, `<token>`,
`<connstr>` and `<pii kind="...">`, each naming a vault reference and never a
value. Putting the values back is a separate direction, and it happens at one
boundary only.

**Executor-only.** `Rehydrate` is called inside the executor's
action-dispatch function, immediately before the content is handed to the
subprocess or the model call. It must not appear in middleware, a pipeline
stage, a tool handler, a log path or a storage write. The approved use is the
whole of the permitted pattern:

```go
rc, err := rehydrator.Rehydrate(ctx, action.Content)
if err != nil {
    return err
}
defer rc.Zero()
return executor.Run(ctx, rc.Data)
```

The rule is machine-enforced rather than documented: a build gate fails on a
`Rehydrate` caller outside the secrets package and the conductor executor.

**The Zero obligation.** The rehydrated buffer lives from `Rehydrate` to the
executor's return and no longer. `Zero` overwrites every byte of the backing
array with `0x00` and is idempotent, so the deferred call is safe on the
success path and the error path alike. The same applies to any staging buffer
a pipe carrier uses.

**Fail-closed rules.** An unknown vault name refuses with
`ErrVaultKeyNotFound`. A run of bytes that opens like a tag and does not
satisfy the grammar refuses with `ErrMalformedTag`; this direction never
falls back to treating it as prose. Content that is not valid UTF-8 refuses.
Empty content returns nothing and makes no vault call. Every refusal returns
no content at all, so a lookup that fails on the second tag cannot leave the
first tag's value in a buffer the caller can read.

**Anti-logging type invariants.** The returned type implements none of
`fmt.Stringer`, `fmt.GoStringer`, `encoding.TextMarshaler`,
`json.Marshaler` or `slog.LogValuer`, asserted at compile time. A logging
call handed one prints a struct address, never a credential.

**The vault read is not elevated.** Rehydration resolves each tag through an
unexported in-package read rather than the elevated vault verb. Raising an
attestation prompt per dispatched action is not a control anyone can answer
honestly at that rate, and in a build where the elevated verbs are refused
outright it would make every tagged action unrunnable.

## Injection channel

**One permitted carrier.** A rehydrated value lives in a non-inherited memory
buffer inside the trusted executor process. Where a value must cross a
process boundary at all, the only permitted carrier is an explicitly numbered
pipe that no other descendant inherits, closed by the executor on completion
and never referenced from a serialized payload.

**Five forbidden carriers**, each with a red-team case asserting the value is
absent byte for byte: child-process environment variables; argv;
working-directory files, including temp files under the child's working
directory; inherited file descriptors; and serialized task payloads, which
covers job rows, queue messages, event bodies and journals.

**Output redaction.** A subprocess that echoes its own input must not become
a leak path. The executor's stdout, stderr and structured result reach no
capture, log, event emit or persistence sink until they have passed the
egress substitution pass on the executor's own egress path.

**The audit log is on the same footing.** The append path runs the
substitution pass over the `action` and `explain` fields through an injected
redactor seam before the record is sealed, so a credential a caller puts in
either field is tagged in the row that is written. A redactor failure refuses
the append; there is no path that stores a field the redactor did not clear.

**MCP responses are outbound.** A tool-protocol response leaving the daemon
for a harness connection is an outbound crossing, and it is marshalled
through the egress firewall on its `mcp.response` class rather than encoded
straight to the wire. A transport that cannot build its firewall writes
nothing rather than falling back to an unfiltered encode.
