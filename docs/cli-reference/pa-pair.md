# `cascade pa pair`

Issues a one-use pairing code for a personal-assistant bridge module (Telegram
today, WhatsApp next). It is the issuance half of the bridge pairing flow; the
other half is typing `/pair <code>` into the bridge itself.

**It asks the daemon.** The command is a client for the `pa.pair_code` RPC: the
daemon runs the bridge's long poll and verifies the code, and it is the only
process holding the key the stored digest is computed under. With no daemon
running the command reports that and prints nothing.

```
cascade pa pair [subject] [--json]
```

## What it prints

```
$ cascade pa pair
pairing code: 7ZQK3M9F (subject tg-9f2a4c1b8d3e5f70)
expires: 2026-09-20T12:10:00Z
send "/pair 7ZQK3M9F" to the bot before it expires
```

With no daemon listening:

```
$ cascade pa pair
client: pa.pair_code daemon not running or unreachable at /…/cascade.sock
```

With `--json`:

```json
{"code":"7ZQK3M9F","subject":"tg-9f2a4c1b8d3e5f70","expires_at":"2026-09-20T12:10:00Z"}
```

## The code

| Property | Value |
|---|---|
| Alphabet | Crockford base32 (`I`, `L`, `O` and `U` are excluded) |
| Length | 8 characters (40 bits) |
| Lifetime | 10 minutes |
| Uses | one |
| Wrong attempts before the code is burned | 5 |

These are the R-16.37 constants, verbatim. Reissuing for the same subject
supersedes any code still outstanding, and resets the wrong-attempt counter.

The code is never stored. What the daemon persists is **HMAC-SHA256 of the code
under a key derived from the bridge's own bot token** (HKDF-SHA256), and the key
is never written anywhere. 40 bits of Crockford base32 is inside brute-force
range for a bare hash within the code's own TTL, so read access to `cascade.db`
would otherwise have been enough to pair; keyed, that database alone recovers
nothing and verifies nothing.

## The subject

`subject` names which bridge instance the code is for. A Telegram bridge's
subject is a digest of its bot token (`tg-` plus 16 hex characters), so the
value is stable, discloses nothing, and is what the bot echoes back on a
successful pairing.

**Omitting it is the normal call**: the daemon uses the bridge it actually runs.
If no bridge module is enabled there is no such subject, and it refuses:

```
$ cascade pa pair
cascade pa pair: no bridge module is enabled, so there is no subject to pair
(the telegram module is disabled in the cascade-pa module manifest); enable one
in the cascade-pa module manifest and grant its bot token
```

**Naming a different subject is also refused**, for the same reason: the digest
is keyed from the configured bridge's own credential, so a code minted under any
other subject could never be verified by anything.

```
$ cascade pa pair tg-somebody-elses-bot
cascade pa pair: this daemon runs no bridge for subject "tg-somebody-elses-bot";
its configured bridge is "tg-9f2a4c1b8d3e5f70"
```

Both refuse rather than inventing a placeholder, because a code issued under a
name no module verifies looks exactly like a working command and fails only
later, in Telegram, with no explanation.

## Non-interactive

This command never prompts. It has no positional value to await and no TTY
branch, so `CASCADE_NO_INPUT=1` changes nothing about its behaviour or its
output (R-14.71). Both lanes are exercised by
`cmd/cascade/testdata/scripts/pa_pair_no_input.txtar`.

## Where the code lives

The daemon writes the keyed digest of the code, its expiry and the attempt
counter into `cascade.db`; the plaintext code is printed once, by this command,
and never stored. Issuance and verification happen in the SAME process — the
daemon — which is what lets the digest be keyed from a credential that never
leaves it. The row is durable so that neither a restart nor a reissue loses the
lockout counter.

## Enabling a bridge

`cascade pa pair` issues codes for any bridge subject. Turning a bridge module
ON is separate and deliberately manual — see the Telegram section of
`plugins/cascade-pa/README.md` for the module flag and the vault grant the bot
token needs.

## Exit status

| Status | Meaning |
|---|---|
| 0 | a code was issued |
| 2 | invalid arguments (more than one subject) |
| other | the daemon is not running or unreachable, no bridge module is enabled, or the named subject is not the one this daemon runs; the message names which |
