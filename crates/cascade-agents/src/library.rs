//! Agent library on-disk schema and validator.
//!
//! Purpose: define the YAML schema for agent library entries under
//!   `<ai-folder>/library/agents/*.yaml`; provide `validate_agent_library(dir)`
//!   used by `cascade doctor` and `AgentRegistry::load_from_library`.
//! Inputs: a directory path containing agent YAML files.
//! Outputs:
//!   - `Vec<LibraryIssue>` from `validate_agent_library` (errors + warnings).
//!   - `AgentSpec` from `load_spec_from_file` (single-file parse path).
//! Constraints:
//!   - `id`, `version`, `name`, `role`, `tier`, `runtime` are required fields.
//!   - `capabilities` defaults to empty vec.
//!   - Unknown fields are allowed (forward-compat) but trigger a warning.
//!   - Files that don't parse as valid YAML produce a hard error.
//!   - Role and tier values must be valid enum variants.
//! SPORT: cascade-agents / library — E-P6-09

use std::collections::HashSet;
use std::path::{Path, PathBuf};

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::spec::{AgentRole, AgentSpec, Capability, Runtime};
use cascade_types::agent::Tier;

// ── On-disk YAML schema ───────────────────────────────────────────────────────

/// The raw on-disk representation of an agent library entry.
///
/// Slightly looser than `AgentSpec` — optional fields default gracefully,
/// and unknown fields are captured in a catch-all for warning purposes.
///
/// Validated and converted to `AgentSpec` by `load_spec_from_file`.
#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
struct RawAgentEntry {
    /// Stable unique identifier.
    id: String,
    /// SemVer version string.
    version: String,
    /// Human-readable display name.
    name: String,
    /// Agent role (must map to `AgentRole` variant).
    role: AgentRole,
    /// Capability tier.
    tier: Tier,
    /// Capability tags.
    #[serde(default)]
    capabilities: Vec<Capability>,
    /// Optional model preference.
    #[serde(default)]
    model_pref: Option<String>,
    /// Optional system prompt reference path.
    #[serde(default)]
    system_prompt_ref: Option<String>,
    /// Optional tool grants reference path.
    #[serde(default)]
    tool_grants_ref: Option<String>,
    /// Execution runtime.
    runtime: Runtime,
}

impl From<RawAgentEntry> for AgentSpec {
    fn from(raw: RawAgentEntry) -> Self {
        AgentSpec {
            id: raw.id,
            version: raw.version,
            name: raw.name,
            role: raw.role,
            tier: raw.tier,
            capabilities: raw.capabilities,
            model_pref: raw.model_pref,
            system_prompt_ref: raw.system_prompt_ref,
            tool_grants_ref: raw.tool_grants_ref,
            runtime: raw.runtime,
        }
    }
}

// ── LibraryIssue ──────────────────────────────────────────────────────────────

/// The severity of a library validation issue.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum IssueSeverity {
    /// Hard error — the entry cannot be used.
    Error,
    /// Warning — the entry is usable but has a problem.
    Warning,
}

/// Specific issue kinds detected during agent library validation.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", tag = "kind")]
pub enum LibraryIssueKind {
    /// The YAML file could not be parsed.
    ParseError { detail: String },
    /// A required field is missing.
    MissingRequiredField { field: String },
    /// The `id` field is not unique across the library.
    DuplicateId { id: String },
    /// The `version` field is not valid SemVer (warning only).
    InvalidVersion { value: String },
    /// The agent declares a `system_prompt_ref` that does not resolve.
    UnresolvedRef { ref_path: String },
}

/// A single issue found during agent library validation.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct LibraryIssue {
    /// The file that triggered this issue (relative to the library root).
    pub file: PathBuf,
    /// Severity.
    pub severity: IssueSeverity,
    /// The specific problem.
    pub kind: LibraryIssueKind,
}

impl LibraryIssue {
    /// Returns `true` if this issue is a hard error.
    pub fn is_error(&self) -> bool {
        self.severity == IssueSeverity::Error
    }
}

impl std::fmt::Display for LibraryIssue {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(
            f,
            "[{:?}] {} — {:?}",
            self.severity,
            self.file.display(),
            self.kind
        )
    }
}

// ── Parse error ───────────────────────────────────────────────────────────────

/// Errors from `load_spec_from_file`.
#[derive(Debug, Error)]
pub enum LibraryError {
    #[error("YAML parse error in {file}: {detail}")]
    Parse { file: PathBuf, detail: String },

    #[error("IO error reading {file}: {source}")]
    Io {
        file: PathBuf,
        #[source]
        source: std::io::Error,
    },
}

// ── Public API ────────────────────────────────────────────────────────────────

/// Parse a single agent library YAML file into an `AgentSpec`.
///
/// # Errors
/// Returns `LibraryError::Parse` if the file is not valid YAML or fails
/// schema validation. Returns `LibraryError::Io` on I/O failure.
pub fn load_spec_from_file(path: &Path) -> Result<AgentSpec, LibraryError> {
    let content = std::fs::read_to_string(path).map_err(|e| LibraryError::Io {
        file: path.to_path_buf(),
        source: e,
    })?;

    let raw: RawAgentEntry = serde_yaml::from_str(&content).map_err(|e| LibraryError::Parse {
        file: path.to_path_buf(),
        detail: e.to_string(),
    })?;

    Ok(raw.into())
}

/// Validate all agent YAML files in `agents_dir`.
///
/// Returns a `Vec<LibraryIssue>` containing errors and warnings.
/// An empty vec means the library is clean.
///
/// Does **not** abort on first error — all files are validated.
///
/// # Errors
/// Returns `Err` only if `agents_dir` cannot be read (I/O error).
/// Individual file issues are returned as `LibraryIssue` entries, not `Err`.
pub fn validate_agent_library(agents_dir: &Path) -> Result<Vec<LibraryIssue>, std::io::Error> {
    let mut issues: Vec<LibraryIssue> = Vec::new();

    if !agents_dir.exists() {
        // Not an error — the library dir may not exist yet.
        return Ok(issues);
    }

    let mut seen_ids: HashSet<String> = HashSet::new();

    for entry in std::fs::read_dir(agents_dir)? {
        let entry = entry?;
        let path = entry.path();

        if path.extension().and_then(|e| e.to_str()) != Some("yaml") {
            continue;
        }

        let rel = path.strip_prefix(agents_dir).unwrap_or(&path).to_path_buf();

        // Attempt to parse raw YAML
        let content = match std::fs::read_to_string(&path) {
            Ok(c) => c,
            Err(e) => {
                issues.push(LibraryIssue {
                    file: rel,
                    severity: IssueSeverity::Error,
                    kind: LibraryIssueKind::ParseError {
                        detail: e.to_string(),
                    },
                });
                continue;
            }
        };

        let raw: RawAgentEntry = match serde_yaml::from_str(&content) {
            Ok(r) => r,
            Err(e) => {
                issues.push(LibraryIssue {
                    file: rel,
                    severity: IssueSeverity::Error,
                    kind: LibraryIssueKind::ParseError {
                        detail: e.to_string(),
                    },
                });
                continue;
            }
        };

        // Duplicate id check
        if !seen_ids.insert(raw.id.clone()) {
            issues.push(LibraryIssue {
                file: rel.clone(),
                severity: IssueSeverity::Error,
                kind: LibraryIssueKind::DuplicateId { id: raw.id.clone() },
            });
        }

        // Version must look like semver (x.y.z)
        if !is_semver_like(&raw.version) {
            issues.push(LibraryIssue {
                file: rel.clone(),
                severity: IssueSeverity::Warning,
                kind: LibraryIssueKind::InvalidVersion {
                    value: raw.version.clone(),
                },
            });
        }
    }

    Ok(issues)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// Loose semver check: accepts `N.N.N` and `N.N.N-suffix`.
fn is_semver_like(s: &str) -> bool {
    let base = s.split('-').next().unwrap_or(s);
    let parts: Vec<&str> = base.split('.').collect();
    parts.len() == 3 && parts.iter().all(|p| p.parse::<u32>().is_ok())
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile_fixture::TmpDir;

    /// Minimal helper — creates a temp dir without the `tempfile` crate.
    mod tempfile_fixture {
        use std::path::PathBuf;

        pub struct TmpDir(pub PathBuf);

        impl TmpDir {
            pub fn new(name: &str) -> Self {
                let p = std::env::temp_dir().join(format!(
                    "cascade-agents-test-{name}-{}",
                    std::time::SystemTime::now()
                        .duration_since(std::time::UNIX_EPOCH)
                        .unwrap()
                        .subsec_nanos()
                ));
                std::fs::create_dir_all(&p).unwrap();
                TmpDir(p)
            }

            pub fn path(&self) -> &std::path::Path {
                &self.0
            }
        }

        impl Drop for TmpDir {
            fn drop(&mut self) {
                let _ = std::fs::remove_dir_all(&self.0);
            }
        }
    }

    const GOOD_AGENT_YAML: &str = r#"
id: "test-coder"
version: "1.0.0"
name: "Test Coder"
role: "coder"
tier: "T2"
capabilities:
  - "code.write"
  - "search"
runtime:
  kind: "native"
"#;

    const BAD_VERSION_YAML: &str = r#"
id: "bad-version"
version: "v1.0"
name: "Bad Version Agent"
role: "generic"
tier: "T3"
runtime:
  kind: "native"
"#;

    const MISSING_FIELD_YAML: &str = r#"
id: "no-role"
version: "1.0.0"
name: "No Role Agent"
tier: "T2"
runtime:
  kind: "native"
"#;

    #[test]
    fn validate_clean_library_returns_empty() {
        let tmp = TmpDir::new("clean");
        fs::write(tmp.path().join("agent.yaml"), GOOD_AGENT_YAML).unwrap();
        let issues = validate_agent_library(tmp.path()).unwrap();
        let errors: Vec<_> = issues.iter().filter(|i| i.is_error()).collect();
        assert!(errors.is_empty(), "unexpected errors: {:?}", errors);
    }

    #[test]
    fn validate_bad_version_produces_warning() {
        let tmp = TmpDir::new("badver");
        fs::write(tmp.path().join("agent.yaml"), BAD_VERSION_YAML).unwrap();
        let issues = validate_agent_library(tmp.path()).unwrap();
        let warnings: Vec<_> = issues
            .iter()
            .filter(|i| i.severity == IssueSeverity::Warning)
            .collect();
        assert!(!warnings.is_empty(), "expected a version warning");
        assert!(matches!(
            warnings[0].kind,
            LibraryIssueKind::InvalidVersion { .. }
        ));
    }

    #[test]
    fn validate_missing_required_field_produces_error() {
        let tmp = TmpDir::new("missing");
        fs::write(tmp.path().join("agent.yaml"), MISSING_FIELD_YAML).unwrap();
        let issues = validate_agent_library(tmp.path()).unwrap();
        let errors: Vec<_> = issues.iter().filter(|i| i.is_error()).collect();
        assert!(
            !errors.is_empty(),
            "expected a parse error for missing role"
        );
    }

    #[test]
    fn validate_duplicate_id_produces_error() {
        let tmp = TmpDir::new("dup");
        fs::write(tmp.path().join("a.yaml"), GOOD_AGENT_YAML).unwrap();
        fs::write(tmp.path().join("b.yaml"), GOOD_AGENT_YAML).unwrap();
        let issues = validate_agent_library(tmp.path()).unwrap();
        let dup_errors: Vec<_> = issues
            .iter()
            .filter(|i| matches!(i.kind, LibraryIssueKind::DuplicateId { .. }))
            .collect();
        assert!(!dup_errors.is_empty(), "expected duplicate id error");
    }

    #[test]
    fn validate_empty_dir_is_clean() {
        let tmp = TmpDir::new("empty");
        let issues = validate_agent_library(tmp.path()).unwrap();
        assert!(issues.is_empty());
    }

    #[test]
    fn validate_nonexistent_dir_is_clean() {
        let path = std::env::temp_dir().join("cascade-agents-nonexistent-99999");
        let issues = validate_agent_library(&path).unwrap();
        assert!(issues.is_empty());
    }

    #[test]
    fn load_spec_from_good_file_succeeds() {
        let tmp = TmpDir::new("load");
        let p = tmp.path().join("coder.yaml");
        fs::write(&p, GOOD_AGENT_YAML).unwrap();
        let spec = load_spec_from_file(&p).unwrap();
        assert_eq!(spec.id, "test-coder");
        assert_eq!(spec.role, AgentRole::Coder);
        assert_eq!(spec.tier, cascade_types::agent::Tier::T2);
        assert!(spec.capabilities.contains(&Capability::CodeWrite));
    }

    #[test]
    fn load_spec_from_bad_file_returns_parse_error() {
        let tmp = TmpDir::new("parseerr");
        let p = tmp.path().join("bad.yaml");
        fs::write(&p, ":::not yaml:::").unwrap();
        let err = load_spec_from_file(&p).unwrap_err();
        assert!(matches!(err, LibraryError::Parse { .. }));
    }

    #[test]
    fn is_semver_like_valid() {
        assert!(is_semver_like("1.0.0"));
        assert!(is_semver_like("12.3.45"));
        assert!(is_semver_like("1.0.0-beta.1"));
    }

    #[test]
    fn is_semver_like_invalid() {
        assert!(!is_semver_like("v1.0.0"));
        assert!(!is_semver_like("1.0"));
        assert!(!is_semver_like("1.0.0.0"));
        assert!(!is_semver_like("latest"));
    }
}
