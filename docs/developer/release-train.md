# Release signing

The release trust root is the public half of the existing encrypted minisign
release key. Custody keeps the secret file and passphrase in the release vault
with separate escrow copies. Reuse that key when verifying or signing a release.
The committed `docs/keys/minisign.pub` and embedded
`internal/minisign/release.pub` must match the public key recomputed from custody.

First-party registry indexes are signed with
`registry-gen --derive-from-release`. The generator derives an Ed25519 key
from the release seed using HKDF-SHA256, salt `cascade`, and info
`cascade/registry-index/v1`. Every first-party index signing therefore needs
the release master secret. The generator never writes the derived private key.

`--derive-from-release` reads `MINISIGN_RELEASE_SECKEY_B64` and
`MINISIGN_RELEASE_PASSPHRASE` from the process environment for one run. The
passphrase can remain visible to same-user process inspection (`ps -E`) during
that invocation. Do not put either value in command arguments or logs.

Rotating the release key also rotates the derived registry key. An embedded
release key has no revocation path other than a signed update carrying a new
key. Coordinate both public trust roots when preparing that update.

The installer accepts only signed checksum comments of the form
`cascade <V> checksums`. The version must match the selected archive and the
requested release tag, including a resolved latest tag. The installation
receipt records that signed version as its release tag.

See [Plugin Registry](../../.github/wiki/Plugin-Registry.md) for the opt-in
registry URL, embedded default key, and explicit public-key override.
