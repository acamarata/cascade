//! Destructive-action deny-list patterns for the built-in policy evaluator.
//!
//! Purpose: Enumerate every literal substring whose presence in a serialized
//!   `PolicyAction` should trigger a `Decision::Deny`.  Organised by category
//!   so maintainers can audit coverage at a glance.
//! Inputs:  N/A — this module is pure data.
//! Outputs: `DANGEROUS_PATTERNS` — slice of `(pattern, reason)` pairs.
//! Constraints:
//!   - No regex; only literal `str::contains` checks (avoids ReDoS).
//!   - Patterns target the *minimum* dangerous token that cannot appear in a
//!     safe invocation (see safe-variant comments below).
//!   - Every category is documented with rationale.
//!
//! SPORT: MASTER-POLICIES.md

// ── Category 1 — Filesystem destruction ──────────────────────────────────────
//
// Patterns that wipe arbitrary parts of the local filesystem.  We match on the
// path argument suffix, not the bare `rm -rf` token, so that safe invocations
// such as `rm -rf node_modules`, `rm -rf .next`, `rm -rf target`, `rm -rf
// dist`, `rm -rf build` are NOT blocked (they appear in the safe-variants
// allowlist below and the absence of a match here confirms them).
//
// Safe variants (do NOT match): rm -rf node_modules  rm -rf .next
//   rm -rf target  rm -rf dist  rm -rf build  rm -rf .turbo  rm -rf .swc

/// Root-level recursive delete patterns.
pub const FS_PATTERNS: &[(&str, &str)] = &[
    // `rm -rf /` — recursive delete from filesystem root.
    (
        "rm -rf /",
        "recursive delete from filesystem root is forbidden",
    ),
    // `rm -rf ~` — recursive delete of home directory.
    (
        "rm -rf ~",
        "recursive delete of home directory is forbidden",
    ),
    // `rm -rf $HOME` — same via env variable.
    ("rm -rf $HOME", "recursive delete of $HOME is forbidden"),
    // `/Volumes` as a deletion target covers external drives (rm -rf /Volumes/*).
    (
        "rm -rf /Volumes",
        "recursive delete of /Volumes is forbidden",
    ),
    // `rm -rf .git` at repo root — destroys version-control history.
    // The pattern `rm -rf .git` is specific: safe .git-adjacent ops
    // (e.g. `rm -rf .git/hooks`) don't match because they contain `.git/`.
    ("\"rm -rf .git\"", "destroying .git history is forbidden"),
    // `dd of=/dev/` — raw device overwrite (matches any /dev/... target).
    // The space before `of=` ensures we match the argument, not e.g. a path
    // containing the string.
    (" of=/dev/", "raw device overwrite via dd is forbidden"),
    // `mkfs` — formats any filesystem; no safe form in normal agent work.
    ("mkfs", "filesystem formatting is forbidden"),
    // `diskutil eraseDisk` / `diskutil zeroDisk` — macOS disk erase.
    ("diskutil eraseDisk", "diskutil eraseDisk is forbidden"),
    ("diskutil zeroDisk", "diskutil zeroDisk is forbidden"),
    // `shred` on device nodes (e.g. shred /dev/sda).
    ("shred /dev/", "shred on a device node is forbidden"),
];

// ── Category 2 — Git destructive operations ───────────────────────────────────
//
// Force-push, hard-reset, and history rewrite on protected branches.
// "Protected branches" here means: main, master, production, develop, release/*.
// Safe variant: `git branch -D feature/foo` is allowed (not a protected branch).
// Safe variant: `git reset --hard HEAD` on a feature branch is allowed — we
// match only the branch-name suffix to avoid false positives.

/// Git destructive operation patterns.
pub const GIT_PATTERNS: &[(&str, &str)] = &[
    // Force-push to main / master.
    (
        "push --force origin main",
        "force-push to main branch is forbidden",
    ),
    (
        "push --force origin master",
        "force-push to master branch is forbidden",
    ),
    (
        "push --force origin production",
        "force-push to production branch is forbidden",
    ),
    // `git push --delete` removes a remote branch; block on protected names.
    (
        "push --delete origin main",
        "deleting main branch on remote is forbidden",
    ),
    (
        "push --delete origin master",
        "deleting master branch on remote is forbidden",
    ),
    // `git reset --hard` on protected branches — commonly run as
    // `git reset --hard origin/main`; match the protected suffix.
    (
        "reset --hard origin/main",
        "hard-reset to origin/main is forbidden",
    ),
    (
        "reset --hard origin/master",
        "hard-reset to origin/master is forbidden",
    ),
    // `git branch -D main` — delete local protected branch.
    ("branch -D main", "deleting local main branch is forbidden"),
    (
        "branch -D master",
        "deleting local master branch is forbidden",
    ),
    // `git filter-branch` — rewrites published history.
    (
        "filter-branch",
        "filter-branch rewrites published history and is forbidden",
    ),
    // `git push --force-with-lease origin main` — still a force-push variant.
    (
        "push --force-with-lease origin main",
        "force-with-lease push to main is forbidden",
    ),
    (
        "push --force-with-lease origin master",
        "force-with-lease push to master is forbidden",
    ),
];

// ── Category 3 — Database destructive operations ─────────────────────────────
//
// DDL statements that irreversibly destroy data or schema.
// We match upper-case tokens because SQL DDL is conventionally uppercase, and
// the serialized JSON will preserve the casing the agent emits.  The evaluator
// also normalises to uppercase before matching (see simple.rs).
// Safe variant: `TRUNCATE TABLE staging_import` is project-specific; we block
// all TRUNCATE because there is no safe agent-driven TRUNCATE.

/// Database DDL destructive patterns (matched case-insensitively via normalisation).
///
/// Note: `DELETE FROM` is intentionally absent here.  The evaluator handles it
/// separately in `is_delete_without_where`, which allows `DELETE FROM … WHERE`
/// while blocking unbounded deletes.  Adding `DELETE FROM` here would catch
/// even safe, WHERE-guarded deletes.
pub const DB_PATTERNS: &[(&str, &str)] = &[
    ("DROP TABLE", "DROP TABLE is forbidden"),
    ("DROP DATABASE", "DROP DATABASE is forbidden"),
    ("DROP SCHEMA", "DROP SCHEMA is forbidden"),
    // TRUNCATE with or without TABLE keyword.
    ("TRUNCATE", "TRUNCATE is forbidden"),
    // DROP INDEX / DROP VIEW round out common DDL destruction.
    ("DROP INDEX", "DROP INDEX is forbidden"),
    ("DROP VIEW", "DROP VIEW is forbidden"),
];

// ── Category 4 — Infrastructure destruction ───────────────────────────────────
//
// Cloud/container operations that permanently delete running infrastructure.

/// Infrastructure destruction patterns.
pub const INFRA_PATTERNS: &[(&str, &str)] = &[
    // Terraform.
    ("terraform destroy", "terraform destroy is forbidden"),
    // kubectl on namespaces and persistent volumes.
    (
        "kubectl delete namespace",
        "deleting a Kubernetes namespace is forbidden",
    ),
    (
        "kubectl delete pvc",
        "deleting a Kubernetes PVC is forbidden",
    ),
    // Docker volume removal on named data volumes (rm -f on anonymous volumes
    // for ephemeral containers is fine, but named volumes are data stores).
    // We match `docker volume rm` which requires an explicit volume name.
    ("docker volume rm", "docker volume rm is forbidden"),
    // `docker system prune` wipes all stopped containers + dangling volumes.
    ("docker system prune", "docker system prune is forbidden"),
];

// ── Category 5 — Publishing & version bumps ───────────────────────────────────
//
// Publishing to public registries or creating public GitHub releases.
// These are irreversible within 72 h and may expose broken or insecure code.

/// Publishing and release patterns.
pub const PUBLISH_PATTERNS: &[(&str, &str)] = &[
    // npm / pnpm publish.
    ("npm publish", "npm publish requires explicit user approval"),
    (
        "pnpm publish",
        "pnpm publish requires explicit user approval",
    ),
    // Cargo publish.
    (
        "cargo publish",
        "cargo publish requires explicit user approval",
    ),
    // pip / PyPI.
    ("pip publish", "pip publish requires explicit user approval"),
    (
        "twine upload",
        "twine upload to PyPI requires explicit user approval",
    ),
    // GitHub release creation.
    (
        "gh release create",
        "gh release create requires explicit user approval",
    ),
];

// ── Category 6 — Secret exfiltration ─────────────────────────────────────────
//
// Commands that print or commit credential files to stdout/git.
// We match on the filename patterns; the `cat` command with these paths is a
// common exfiltration vector.

/// Secret-file access patterns.
pub const SECRET_PATTERNS: &[(&str, &str)] = &[
    // Printing .env files (any variant: .env, .env.local, .env.production …).
    ("cat .env", "printing .env files is forbidden"),
    ("cat vault.env", "printing vault.env is forbidden"),
    // Committing credential files.
    ("git add .env", "staging .env files for commit is forbidden"),
    (
        "git add vault.env",
        "staging vault.env for commit is forbidden",
    ),
    // Grep/find for private keys in common output-to-stdout invocations.
    // Match `-----BEGIN` which is the PEM header present in all private key types.
    (
        "-----BEGIN",
        "PEM private key material in output is forbidden",
    ),
];

// ── Aggregated slice ──────────────────────────────────────────────────────────

/// All dangerous patterns, concatenated in category order.
///
/// Each element is `(literal_substring, human_readable_reason)`.
/// The evaluator iterates this slice and denies on the first match.
pub const ALL_PATTERNS: &[&[(&str, &str)]] = &[
    FS_PATTERNS,
    GIT_PATTERNS,
    DB_PATTERNS,
    INFRA_PATTERNS,
    PUBLISH_PATTERNS,
    SECRET_PATTERNS,
];

/// Iterator over all `(pattern, reason)` pairs across every category.
pub fn all_patterns() -> impl Iterator<Item = (&'static str, &'static str)> {
    ALL_PATTERNS
        .iter()
        .flat_map(|cat| cat.iter().map(|(p, r)| (*p, *r)))
}

/// Total number of patterns across all categories.
pub const fn pattern_count() -> usize {
    FS_PATTERNS.len()
        + GIT_PATTERNS.len()
        + DB_PATTERNS.len()
        + INFRA_PATTERNS.len()
        + PUBLISH_PATTERNS.len()
        + SECRET_PATTERNS.len()
}
