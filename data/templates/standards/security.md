---
id = "standard-security"
version = "1.0.0"
tier = "any"
stacks = []
project_shapes = []
description = "Universal security baseline: no committed credentials, .env gitignored before writing, dependency auditing, least-privilege."
---

# Security Baseline

## Credentials

- Never hardcode secrets, tokens, API keys, or passwords in source code.
- Load secrets from environment variables or a secrets manager at runtime.
- `.env` and any file containing real credentials must be in `.gitignore`
  *before* the file is created. An `.env.example` with placeholder values is
  the only committed reference.
- A secret committed to git — even once, even in a private repo — is compromised.
  Rotate it immediately; do not rely on git history rewriting to protect it.

## Dependency Auditing

- Run the package manager's audit tool in CI: `npm audit`, `cargo audit`,
  `pip-audit`, `go list -json -deps ./... | nancy`. Block on high/critical severity.
- Pin direct dependencies to exact or tight ranges; commit lockfiles.
- Review the diff of any lockfile change before merging.
- Remove unused dependencies — they are attack surface with no benefit.

## Least-Privilege Principle

- Service accounts, API tokens, and IAM roles get the minimum permissions needed.
- Prefer read-only tokens for read-only tasks.
- Scope tokens to the smallest resource set possible (one bucket, one repo, one
  secret) rather than account-wide access.
- Rotate credentials on a schedule, and immediately after any personnel change.

## Input Validation

- Treat all external input as untrusted: user data, query parameters, environment
  variables, files from disk, network responses.
- Validate and sanitize at the boundary before passing data inward.
- Use parameterized queries (never string interpolation) for database access.
- Reject or escape data before writing it to HTML, shell commands, or file paths.

## Network and Transport

- HTTPS only for all external communication. Never downgrade to HTTP in production.
- Verify TLS certificates; do not disable certificate validation in any non-test code.
- Set timeouts on all outbound HTTP/network calls.

## Logging

- Never log secrets, tokens, passwords, or full credit-card numbers.
- Sanitize error messages returned to clients — stack traces and internal paths
  are information for attackers.
