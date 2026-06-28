# /deps-audit — Dependency Audit

Runs `cascade security audit` across all package ecosystems in the current project and surfaces high/critical CVEs.

## What it does

Detects the package manager(s) in use and audits accordingly:

| Ecosystem | Command run |
|---|---|
| Rust (cargo) | `cascade security audit` -> `cargo audit` |
| npm / pnpm | `cascade security audit` -> `pnpm audit --audit-level high` |
| Python (pip) | `cascade security audit` -> `pip-audit` |

## Output format

High and critical findings only (informational/low omitted unless asked):

| Package | Version | CVE | Severity | Fix |
|---|---|---|---|---|
| openssl | 0.10.55 | CVE-2023-0286 | HIGH | Upgrade to 0.10.60 |
| lodash | 4.17.20 | CVE-2021-23337 | HIGH | Upgrade to 4.17.21 |

Nothing to report = "No high/critical CVEs found."

## What to do with findings

- **CRITICAL** — block the current task; fix or pin a safe version before continuing.
- **HIGH** — file a task and fix within the current phase; document if deferring.
- **MEDIUM/LOW** — log for awareness; do not block on them.

## When to use

After adding any dependency. Before any release. When a CVE advisory lands in your feed that might affect this project.
