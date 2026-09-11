# Signing-key rotation

The procedure for rotating the minisign key that signs every release
artifact, and the custody rules around the private half.

## Key custody

**Public key.** Published out of band, in four independent places, so a
verifier never has to trust only the copy the installer itself carries:

1. `README.md`
2. `SECURITY.md`
3. The Homebrew formula
4. A pinned well-known URL

`install.sh` fetches and pins the public key from that independent
source (or prints a manual verification step when it cannot), never
trusting only its own embedded copy.

**Private key.** Generated in an owner ceremony, stored in the owner's
password manager plus an escrow copy, and injected into CI as a secret.
The private key is never committed to the repository, in any form, at
any point. This custody procedure is owner prerequisite #7 and is a
precondition of cutting a release, not something automated by this
project's tooling.

## Rotation procedure: dual-sign window

When the signing key is rotated:

1. A new keypair is generated under the same ceremony and custody rules
   above.
2. During a transition window, releases are **dual-signed**: each
   release artifact is signed by both the new key and the outgoing key.
3. Verifiers that have not yet picked up the new public key can still
   verify against the old signature; verifiers that have picked up the
   new key verify against the new signature. Neither is treated as
   optional during the window.
4. **N-1 keys are accepted** during the transition: a verifier configured
   with the previous key still succeeds, so a rotation never breaks an
   installation that has not yet re-pinned the new public key.
5. Once the transition window closes, the outgoing key is retired: new
   releases are signed only with the current key, and the four out-of-band
   channels above are updated to publish only the current key.

The new key is itself signed by the old key at the start of the
transition, so the chain of trust from an already-verified old key to
the new one is unbroken, rather than requiring a fresh out-of-band trust
decision at every rotation.

## Node transfers

Binary transfers to fleet nodes (see
[node-skew.md](node-skew.md)) verify the same signature this page
governs; a rotation in progress affects node provisioning exactly the
way it affects any other install path, with no separate rule.
