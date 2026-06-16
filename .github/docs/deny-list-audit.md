# Deny-List Audit: Destructive-Action Patterns

Maps every pattern category in `cascade-harness/src/policy/patterns.rs` to the
relevant OWASP LLM Top-10 2025 risk and documents the injection-resilience
coverage added in the same module.

---

## Pattern Categories

### 1. Filesystem Destruction (9 patterns)

| Pattern token | Risk |
|---|---|
| `rm -rf /` | Root filesystem wipe |
| `rm -rf ~` | Home directory wipe |
| `rm -rf $HOME` | Home directory via env var |
| `rm -rf /Volumes` | External drive wipe |
| `rm -rf .git` | Destroy version-control history |
| ` of=/dev/` | Raw device overwrite via `dd` |
| `mkfs` | Filesystem format |
| `diskutil eraseDisk` | macOS disk erase |
| `diskutil zeroDisk` | macOS disk zero-fill |

**OWASP LLM mapping:** LLM04 — Data and Model Poisoning (agent irreversibly alters the
environment); LLM08 — Excessive Agency (agent takes unsanctioned destructive action beyond
its authorised scope).

---

### 2. Git Destructive Operations (10 patterns)

| Pattern token | Risk |
|---|---|
| `push --force origin main/master/production` | Overwrite published history |
| `push --force-with-lease origin main/master` | Force-push variant, still destructive |
| `push --delete origin main/master` | Remove protected remote branch |
| `reset --hard origin/main` / `origin/master` | Destroy local commits irreversibly |
| `branch -D main` / `branch -D master` | Delete local protected branch |
| `filter-branch` | Rewrite published history |

**OWASP LLM mapping:** LLM08 — Excessive Agency (agent acts beyond sanctioned permissions
on a shared repository); LLM06 — Sensitive Information Disclosure (filter-branch may expose
then republish filtered secrets).

---

### 3. Database Destructive Operations (6 patterns + 1 contextual rule)

| Pattern token | Risk |
|---|---|
| `DROP TABLE` | Permanent schema + data loss |
| `DROP DATABASE` | Full database deletion |
| `DROP SCHEMA` | Full schema deletion |
| `TRUNCATE` | Bulk data erasure |
| `DROP INDEX` | Index removal |
| `DROP VIEW` | View removal |
| `DELETE FROM` without `WHERE` | Unbounded row deletion (contextual rule) |

All DB patterns are matched case-insensitively (input uppercased before match).
`DELETE FROM … WHERE …` is allowed; unbounded `DELETE FROM` is denied via a
separate per-segment check that verifies the absence of a `WHERE` clause.

**OWASP LLM mapping:** LLM08 — Excessive Agency; LLM04 — Data and Model Poisoning
(irreversible data destruction).

---

### 4. Infrastructure Destruction (5 patterns)

| Pattern token | Risk |
|---|---|
| `terraform destroy` | Full infrastructure teardown |
| `kubectl delete namespace` | Delete all workloads in a namespace |
| `kubectl delete pvc` | Delete persistent volume — data loss |
| `docker volume rm` | Delete named data volume |
| `docker system prune` | Wipe all stopped containers + dangling volumes |

**OWASP LLM mapping:** LLM08 — Excessive Agency (agent destroys production infra);
LLM07 — System Prompt Leakage (agent executing injected infra-destroy commands).

---

### 5. Publishing and Release (6 patterns)

| Pattern token | Risk |
|---|---|
| `npm publish` | Irreversible package release |
| `pnpm publish` | Irreversible package release |
| `cargo publish` | Irreversible crate release |
| `pip publish` | PyPI release |
| `twine upload` | PyPI upload |
| `gh release create` | GitHub public release |

**OWASP LLM mapping:** LLM08 — Excessive Agency (agent publishes on behalf of user without
explicit approval); LLM02 — Sensitive Information Disclosure (accidentally bundled secrets in
published artifact).

---

### 6. Secret Exfiltration (5 patterns)

| Pattern token | Risk |
|---|---|
| `cat .env` | Print credential file |
| `cat vault.env` | Print vault credentials |
| `git add .env` | Stage credential file for commit |
| `git add vault.env` | Stage vault file for commit |
| `-----BEGIN` | PEM private key material in output |

**OWASP LLM mapping:** LLM06 — Sensitive Information Disclosure (credentials leaked via
stdout or committed to VCS); LLM01 — Prompt Injection (injection payload extracts secrets
via an innocuous-looking shell command).

---

## Injection-Resilience Coverage

### Encoding normalisation

Applied to the full serialised `PolicyAction` JSON before pattern matching.
Implemented in `simple.rs::normalise()`.

| Vector | Technique | Example |
|---|---|---|
| Unicode homoglyphs | Map full-width chars, en/em dashes, full-width slash/tilde to ASCII | `rm \u{2013}rf /` → `rm -rf /` |
| URL percent-encoding | Inline percent-decoder (no external deps) | `rm%20-rf%20/` → `rm -rf /` |
| Base64 token expansion | Inline base64 decoder; each whitespace-delimited token decoded if valid | `cm0gLXJmIC8=` → `rm -rf /` |

**OWASP LLM mapping:** LLM01 — Prompt Injection (encoding-based evasion is a primary
injection technique catalogued under LLM01); LLM05 — Improper Output Handling.

### Chained-command splitting

Shell chains (`&&`, `||`, `;`, `|`, newlines) are split into individual segments
before pattern matching.  Each segment is evaluated independently.
Implemented in `simple.rs::split_chain()`.

| Operator | Example | Detection |
|---|---|---|
| `&&` | `cargo build && rm -rf /` | `rm -rf /` segment matched |
| `;` | `ls /tmp ; rm -rf ~` | `rm -rf ~` segment matched |
| `\|` | `cat file \| mkfs.ext4 /dev/sdb` | `mkfs.ext4 /dev/sdb` segment matched |
| `\|\|` | `build \|\| rm -rf $HOME` | `rm -rf $HOME` segment matched |
| newline | `cargo build\nrm -rf /` | `rm -rf /` segment matched |

**OWASP LLM mapping:** LLM01 — Prompt Injection (chained-command injection is the most
common technique for bypassing single-command deny-lists; explicitly addressed in OWASP
LLM01 remediation guidance).

---

## Pattern Count Summary

| Category | Count |
|---|---|
| Filesystem destruction | 9 |
| Git destructive operations | 10 |
| Database DDL (+ contextual DELETE rule) | 6 + 1 |
| Infrastructure destruction | 5 |
| Publishing and release | 6 |
| Secret exfiltration | 5 |
| **Total literal patterns** | **41** |
| **Total pattern rules (incl. contextual)** | **42** |

---

## Limitations and Non-Goals

- **No runtime sandboxing.** The evaluator is a pre-flight check, not a sandbox.
  A process that bypasses the harness entirely (e.g. direct kernel syscall) is
  out of scope.
- **No semantic analysis.** Patterns are literal substrings; a sufficiently
  obfuscated command using multi-step variable substitution
  (`A="rm "; B="-rf /"; $A$B`) will not be caught.  Defence-in-depth (OS-level
  permissions, CI gating, code review) is expected alongside this evaluator.
- **Allowlisting.** Safe ephemeral-directory variants (`rm -rf node_modules`,
  `rm -rf .next`, `rm -rf target`, etc.) are not blocked because the patterns
  are scoped to the minimum dangerous suffix (e.g. `rm -rf /`, not `rm -rf`).
