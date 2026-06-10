# Security Policy

## STRIDE Threat Model

Cascade is a local-first AI orchestration daemon. The following table maps each
STRIDE threat to the primary attack surfaces and the mitigations in place.

### S — Spoofing

| Surface | Threat | Mitigation |
|---------|--------|-----------|
| MCP client connections | A process claims to be a trusted MCP client | OS peer credentials (`SO_PEERCRED` on Unix, named-pipe SID on Windows) are extracted at connection time and stored as the audit actor. Caller-supplied identity fields are ignored. |
| OAuth callbacks | An attacker forges or replays an authorization callback | 32-byte random nonce, HMAC-SHA256 signed with a per-session key. Callback validated with `constant_time_eq`. Redirect URI must match registered list exactly. PKCE enforced. Callbacks are single-use (replay guard). |
| Plugin identity | A WASM plugin claims broader capabilities | Plugin manifests are verified against a content-addressed registry. Host calls are gated by the capability set declared at install time. |

### T — Tampering

| Surface | Threat | Mitigation |
|---------|--------|-----------|
| Tool-call parameters | Path traversal, shell injection in tool arguments | `ScanPipeline` with `PathTraversalDetector` and `CommandInjectionDetector` runs before every tool dispatch. |
| MCP envelope | Malformed JSON-RPC messages designed to confuse the dispatcher | Every incoming message is validated against the MCP 2025-03 JSON-RPC schema. Unknown methods are rejected with error -32601. |
| RAG knowledge base | Injected documents containing prompt directives | `RagPoisoningScanner` runs at both ingestion and retrieval time with two named profiles: `agent-strict` (default) and `doc-permissive` (trusted sources). |
| Config and binary | Modification of `cascade.toml` or the installed binary | Sensitive config fields are encrypted with AES-256-GCM. Update binaries are verified against SLSA Level 3 attestations and a pinned signing certificate before installation. |

### R — Repudiation

| Surface | Threat | Mitigation |
|---------|--------|-----------|
| All security-relevant actions | An actor denies having performed an action | Append-only hash-chain audit log (SQLite WAL, no UPDATE/DELETE). Each entry carries the SHA-256 of the previous entry. `cascade audit verify` detects any retrospective modification. Actor is OS-derived, not caller-supplied. |

### I — Information Disclosure

| Surface | Threat | Mitigation |
|---------|--------|-----------|
| Agent outputs | Secrets leaking through model responses | `OutputSanitizer` scans all responses before delivery. `SecretsScanner` covers AWS access keys, GCP service accounts, Azure connection strings, PEM private keys, JWTs, SSH keys, and high-entropy tokens (≥4.5 bits/char). |
| Audit log | Secrets in event payloads | Secret values are never written to the audit log. Only key IDs and match counts are recorded. |
| OS keychain | Credential access by other processes | Credentials are stored via the OS keychain API (macOS Keychain Services, Linux Secret Service, Windows Credential Manager). The daemon requests only the permissions it needs at runtime. |

### D — Denial of Service

| Surface | Threat | Mitigation |
|---------|--------|-----------|
| WASM plugin execution | Infinite loop, memory exhaustion, fuel bypass via host-call recursion | Wasmtime fuel is armed before any guest code runs. Host functions that re-enter guest code are prohibited. Memory growth is capped. File-descriptor exhaustion is prevented via per-session fd limits. |
| Daemon environment | Malicious env vars altering process behavior | `EnvVarAllowlist` strips all env vars not on the explicit allowlist at startup. |

### E — Elevation of Privilege

| Surface | Threat | Mitigation |
|---------|--------|-----------|
| Plugin host calls | A plugin invokes host functions outside its declared capabilities | Host calls are dispatched through a capability gate that checks the plugin's granted capability set. Attempts to call undeclared host functions are rejected and logged. |
| Daemon environment | `LD_PRELOAD`, `DYLD_INSERT_LIBRARIES`, `PYTHONPATH` injection | Stripped by `EnvVarAllowlist` at startup (see D column above). |

---

## Supported Versions

Only the latest release on the `main` branch receives security fixes. Older
releases are unsupported.

## Reporting a Vulnerability

Report security vulnerabilities via GitHub Security Advisories:

1. Go to `https://github.com/acamarata/cascade/security/advisories`.
2. Click **New draft security advisory**.
3. Fill in the title, description, affected versions, and CVSS score if known.
4. Submit.

The maintainer will acknowledge within 72 hours and aim to publish a patch
within 14 days for critical issues.

Do not open a public GitHub issue for security vulnerabilities.

## Severity Matrix

| Severity | CVSS base score | Response target |
|----------|----------------|----------------|
| Critical | 9.0 – 10.0 | Patch within 7 days |
| High | 7.0 – 8.9 | Patch within 14 days |
| Medium | 4.0 – 6.9 | Patch within 60 days |
| Low | 0.1 – 3.9 | Next regular release |

## Key Rotation Cadence

| Key type | Rotation period | Trigger for immediate rotation |
|----------|----------------|-------------------------------|
| API keys (MCP server tokens, provider keys) | 90 days | Any suspected compromise |
| Database encryption key (sqlite-vec RAG index) | 12 months | Any suspected compromise |
| Release signing certificate | On expiry | Certificate revocation or compromise |

Rotation runbook: see `.github/docs/security-response.md`.

## Dependency Auditing

`cargo audit` and `cargo deny` run on every pull request and on a daily
scheduled scan (see `.github/workflows/security.yml`). Any RUSTSEC advisory
rated high or critical blocks merge.

`gitleaks` scans every commit for accidental secret inclusion.

## Release signing

Release artifacts are signed with the Cascade release key.

- Public key: [.github/RELEASE_KEY.asc](.github/RELEASE_KEY.asc)
- Fingerprint: `3C46 3D90 DF30 61AA 752F  B850 0F57 2977 3E69 4CEA`

Verify a download with:

```bash
gpg --import RELEASE_KEY.asc
gpg --verify cascade-<version>-<target>.tar.gz.asc
```
