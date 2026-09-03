# Elevation

Some verbs change what cascade is allowed to do rather than what it is
doing: enabling remote elevation, turning on the remote plugin runtime,
widening a policy or sensitivity tier, purging data on uninstall. Those
verbs are *elevated*. They require an attestation signed by a key enrolled
on the device, produced only after the device owner authenticates.

This page states what that protection is and, just as importantly, what it
is not.

## Read this first: elevation is unavailable in the standard release binaries

The published release artifacts are built with cgo disabled. Both
hardware-assisted keystores need cgo:

- macOS keychain access needs the Security and LocalAuthentication
  frameworks.
- Linux authentication needs PAM, and is additionally behind a `pam` build
  tag.

Neither is compiled into a cgo-free binary. What ships instead is a
fallback keystore that refuses every operation: it reports itself
unavailable, and key generation, public-key retrieval and signing all
return an unavailable error.

The practical consequence is that **a standard release binary cannot enroll
a key and cannot sign an attestation, so no elevated verb can be
authorized.** Elevated verbs refuse. Non-elevated verbs are unaffected.

This is deliberate in one respect and a real limitation in another. The
fallback refuses rather than quietly writing a signing key to a file, so
nothing weaker than advertised is shipped and no one is told they have
local authentication when they do not. But the feature genuinely does not
work in the artifact most people install.

To use elevation today, build from source with cgo enabled, and on Linux
with the `pam` tag and PAM headers present:

```bash
CGO_ENABLED=1 go build ./cmd/cascade                   # macOS
CGO_ENABLED=1 go build -tags pam ./cmd/cascade         # Linux
```

Windows is not supported: elevated verbs refuse there by design.

## What the key protection actually is

The signing key is Ed25519. On macOS it is generated in Go and its private
half is stored in the keychain behind an access control that requires
device-owner presence, restricted to this device and available only while
the device is unlocked. It is never written in plaintext, and never stored
in cascade's own configuration or secret storage.

**The key is not hardware-bound.** Apple's Secure Enclave generates only
P-256 keys and cannot hold an Ed25519 key, and the attestation format is
verified with Ed25519. A hardware-backed Ed25519 signing operation is
therefore not available from Apple's public APIs. The key is generated in
software, and its material is present in process memory when it is created
and each time it signs.

That is a weaker guarantee than a key that never leaves a secure element.
An attacker who can execute code as your user at the moment you approve a
signature is in a meaningfully better position than they would be against
hardware-held key material. The operating system still gates access, and
approval still requires your presence, so this is far from no protection.
It is simply not the protection that the phrase "hardware-backed" would
imply, which is why this page does not use it.

## Trust on first use

The daemon records the fingerprint of the first key enrolled on the device.

A later enrollment presenting a *different* key is refused, not accepted
and not silently substituted. That refusal is the entire value of the
scheme: without it, anyone able to run the enrollment step could replace
the trusted key with their own and mint valid attestations from then on.

If the legitimate key is genuinely lost, recovery means removing the
existing trust record deliberately, not overwriting it by accident.

## Authentication and signing are one step

Authentication and signing happen in the same call, and no authentication
result is stored between calls. There is no window in which a prior
approval can be reused for a later signature.

Each attestation is bound to one request, one action, and one nonce, and
expires five minutes after it is issued. A nonce is single-use: consuming
it twice fails, including under concurrent attempts. An attestation issued
for one method or one set of parameters is rejected against another.

## When a check cannot decide

Every check refuses when it cannot prove the safe case. An unparseable
request, an unrecognised value, a missing enrollment, a fingerprint
mismatch, an unavailable keystore: each one refuses rather than proceeding.

The reason is narrow and worth stating. A check that returns "allowed" for
input it could not read hands an attacker a bypass, because the attacker
picks the input. Refusing on unclear input costs a legitimate user one
authorization prompt on a malformed request. Allowing it costs the user the
protection.
