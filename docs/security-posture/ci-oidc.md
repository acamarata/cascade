# CI OIDC Token Verification (P1-CI-14)

## Purpose

This document describes the security posture of the GitHub Actions OIDC
token verifier shipped under ticket P1-CI-14. The verifier accepts only
RS256-signed compact JWS tokens issued by
`https://token.actions.githubusercontent.com` with audience `cascade-ci`,
and binds each accepted token to the repository, commit SHA, workflow
reference and run ID declared in the attestation want. Every verification
failure returns the unattested class with a named check so callers can
distinguish rejection reasons without leaking token contents.

## Egress Policy

The JWKS fetch transits the `ci-oidc-jwks` egress class registered in
`internal/hooks/egress/classes.go`. The class admits only the public
sensitivity tier, refuses restricted and local-only content, and is owned
by P1-CI-14. No vaulted value may appear in the JWKS request; this is
enforced both by the egress policy and by the red-team test
`TestRedTeamNoVaultInJWKSRequest` in `internal/secrets/red_team_test.go`.

## Verification Checks

`VerifyGitHubOIDC` enforces the following checks, each producing a named
`ErrOIDCInvalid{Check}` on failure:

- `alg`: header algorithm must be exactly `RS256`.
- `kid`: token kid must be present in the fetched JWKS.
- `signature`: RS256 signature must validate against the matched key.
- `iss`: issuer must be exactly `https://token.actions.githubusercontent.com`.
- `aud`: audience must be exactly `cascade-ci` (string or single-element array).
- `repository`: claim must equal the wanted repository slug.
- `repository_id`: claim must equal the wanted repository ID.
- `repository_owner_id`: claim must equal the wanted owner ID.
- `job_workflow_ref`: claim must equal the configured workflow reference.
- `sha`: claim must equal the wanted commit SHA.
- `run_id`: claim must equal the wanted run ID.
- `exp`: expiration must be after the current time.
- `iat`: issued-at must be at most 60 seconds in the future.
- `nbf`: not-before must be at most 60 seconds in the future.
- `jwks`: any fetch, status, size or parse failure during JWKS retrieval.

Only when every check passes does the verifier return
`ClassEphemeralOIDC`; every other outcome returns `ClassUnattested`.

## Countersigning

`CountersignOIDC` is the sole producer of `ephemeral-oidc` attestation
records. It runs `VerifyGitHubOIDC`, records the token SHA-256 and run ID
on the attestation record, and countersigns with the controller key.
`DeriveClass` accepts `ephemeral-oidc` only for records carrying these
countersigned fields; a record that merely asserts the class without the
verifier's endorsement derives `unverified`.

## Constraints

- No third-party JWT library is used; all parsing and signature
  verification is implemented in `internal/ci/attest_oidc.go`.
- The JWKS body is capped at 64 KiB and cached for one hour.
- Clocks are injected via `runtime.Clock`; no production code imports
  `time.Now` directly.
- Tests generate ephemeral RSA keys and never embed long-lived
  credentials.