# Linux passphrase presence tier

On Linux, the release build has no protected platform source. The presence tier fills that gap in pure Go: an Ed25519 key that is encrypted under a passphrase you type on the controlling terminal for every elevated verb. It is weaker than hardware custody. Read [the residual](#what-it-does-not-protect-against) before you rely on it.

Custody ranking is platform, then presence, then file, then none. A platform source always wins when one exists. The tier is Linux only; macOS uses the helper and Windows refuses elevation.

## Set up

```text
cascade elevate-helper --enroll --presence
```

The command asks for a passphrase of at least 12 bytes, twice. It then:

1. writes an audit record of the enrolment (action `elevation.presence.enroll`, with the new fingerprint and `fresh` or `rotation`), before the key file changes. If the audit log cannot be written, the enrolment is refused and the key file is not created or changed;
2. creates `<data dir>/elevation/presence.key` with mode `0600`, once;
3. records the public key in the trust store (trust on first use).

The key file holds scrypt parameters (`n=131072`, `r=8`, `p=1`), a salt, a nonce and the private key sealed with AES-256-GCM. The public key is bound into the sealed data, so a swapped public key fails to open.

## Each elevated verb

An elevated verb such as `cascade plugin add` prompts on the terminal:

```text
Cascade presence passphrase:
```

Cascade reads the passphrase from `/dev/tty` with echo off, derives the key, decrypts, signs, and zeroes the passphrase, the derived key and the decrypted key before it returns. Cascade keeps none of them between verbs, so each elevation asks again. (The Go crypto libraries may keep their own internal copies; see the last section.)

The passphrase is never read from standard input, the environment or a command-line argument. A process with no controlling terminal (a daemon, a CI step, `echo pass | cascade ...`) refuses and signs nothing. `CASCADE_NO_INPUT=1` refuses before the terminal is opened.

If you interrupt the prompt and your shell stops echoing, run `stty sane`.

## Rotate the passphrase or key

Run the same command again:

```text
cascade elevate-helper --enroll --presence
```

Rotation asks for the current passphrase first. A wrong passphrase refuses and changes nothing. With the right one, Cascade writes the audit record for the new fingerprint, then writes the new key beside the old file, renames it over and updates the trust record. If the trust record cannot be updated, the old key is put back; if it is unclear whether the trust record was written, nothing is rolled back and the old key is kept as `presence.key.previous.<id>` (if that cannot be written the error says so). Only one enrolment runs at a time.

## What a bad file does

Cascade refuses, before it asks for a passphrase, a `presence.key` that is not a regular file with mode `0600` owned by you, or that has an unknown version, an `n` below 32768, an `r` below 8, a cost above the accepted ceilings (256 MiB of scrypt memory, or more than four times the enrolled work), or fields of the wrong size. A changed ciphertext or public key refuses after the prompt. A key whose seed does not produce the recorded public key refuses as an integrity failure.

## What it does not protect against

The offline cost of guessing the passphrase is one 128 MiB scrypt run per guess at the enrolled parameters. That slows an attacker who copies `presence.key`; it does not stop a weak passphrase from falling to a dictionary.

A process running as your user can replace presence.key and trust.json together with a key whose passphrase it knows; cascade detects this only as a changed enrolment fingerprint in doctor and the audit log.

The tier proves that something typed the passphrase on a terminal, not that a person did. A process that already knows the passphrase can drive a pseudo-terminal and sign; hardware custody has no such path.

A process running as your user can also read the passphrase from a compromised terminal or from process memory while a Sign runs, and a core dump taken in that window can contain it. Cascade zeroes the buffers it owns, but the Go runtime and the key-derivation and signing libraries keep internal copies it cannot reach. Hardware-backed custody has none of these weaknesses. Use a stronger source when one is available.

## Older file-tier enrolments

`cascade elevate-helper --enroll --presence` refuses when the trust record belongs to another key, such as a key from an earlier file-tier enrolment, because replacing a trust record needs more than a new passphrase. On Linux there is no command yet that moves such an enrolment to the presence tier; until one exists the owner has to remove the old trust record by hand and enrol again.

Enrolment leaves two small files beside `presence.key`: `.enroll.lock` (the one-at-a-time lock) and, only after a failed rotation, `presence.key.previous.<id>` (the old key, kept when it is unclear whether the trust record was written). A rotation briefly writes `presence.key.new.<id>` before renaming it over the key. Remove any left over once you have checked `cascade doctor`.
