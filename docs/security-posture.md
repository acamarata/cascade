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
