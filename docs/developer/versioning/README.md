# Versioning policy

The full set of versioning-related policies: how releases are numbered
and tagged, what stability promises they carry, and how the signing key
behind them is rotated.

- [version-scheme.md](version-scheme.md) - the dual-tag scheme
  (`v2.Y.Z` release artifact, `v0.Y.Z` Go module), semver on both sides,
  alpha/prerelease tags, and install channels.
- [deprecation-and-abi.md](deprecation-and-abi.md) - the one-minor
  deprecation warning window, and the `cascade.plugin/v2` manifest ABI
  stability promise.
- [crash-reporting.md](crash-reporting.md) - the opt-in, default-off
  crash-reporting policy (no crash reporter ships in this release).
- [node-skew.md](node-skew.md) - the controller-node same-minor version
  window, negotiation points, and the `cascade node upgrade` remediation
  path.
- [signing-key-rotation.md](signing-key-rotation.md) - the dual-sign
  rotation procedure, N-1 key acceptance, and private-key custody rules.

These pages state policy. Where a policy has a separate mechanics
document, this directory cross-references it rather than duplicating it:
release mechanics live in `../release.md`, the `pkg/` Go SDK's own
stability classification lives in `../api-stability/`, and version-skew
*enforcement* (as opposed to the policy stated here) lives in the fleet
controller's own code and tests.
