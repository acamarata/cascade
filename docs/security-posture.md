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
