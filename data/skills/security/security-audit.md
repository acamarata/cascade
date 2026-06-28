# /security-audit — Security Audit

Runs a full security audit: secret scanning, dependency CVEs, OWASP/headers review.

## What it does

1. **Secret scan** — runs `cascade security secret-scan` on the working tree. Any hit is a blocker; report file + line.
2. **Dependency audit** — runs `cascade security audit` (cargo/npm/pnpm/pip). Surfaces high/critical CVEs only; ignore informational noise.
3. **OWASP/headers review** — spawns the `security-reviewer` agent for deep analysis: missing security headers, insecure defaults, error leaks, auth gaps, input validation.

## Output format

Report findings as a structured table per category:

| Category | Severity | Location | Finding |
|---|---|---|---|
| secrets | CRITICAL | src/config.rs:42 | Hardcoded API key |
| dep-cve | HIGH | lodash@4.17.20 | CVE-2021-23337 |
| owasp | MEDIUM | /api/errors | Stack trace in response |

Blockers (CRITICAL/HIGH) listed first. Nothing to report = "All checks passed."

## When to use

Before any PR merge touching auth, config, deps, or API routes. Also run after adding a new dependency or changing environment variable handling.

## Delegation

Deep OWASP analysis delegates to the `security-reviewer` agent — it reads source files and runs `cascade security` tools directly. Top chat receives a structured findings summary only.
