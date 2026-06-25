//! `FsContentBackend` — production filesystem backend, helper functions, and
//! the static resource catalog builder.

use std::path::PathBuf;

use async_trait::async_trait;
use serde_json::Value;

use cascade_core::pbd::store::{resolve_phases_root, PbdStore};

use crate::error::McpServerError;
use crate::paths as mcp_paths;

use super::types::{ContentBackend, McpResource, PAGE_SIZE};

// ── FsContentBackend (production) ────────────────────────────────────────────

/// Production backend: reads from the filesystem using `cascade_mcp::paths`.
///
/// No in-memory cache — every read reflects current file content.
pub struct FsContentBackend;

#[async_trait]
impl ContentBackend for FsContentBackend {
    async fn read_uri(&self, uri: &str) -> Result<Option<String>, McpServerError> {
        validate_uri_safety(uri)?;

        if let Some(tier) = uri.strip_prefix("cascade://tier/") {
            if !is_safe_segment(tier) {
                return Err(McpServerError::InvalidParams {
                    detail: format!("unsafe tier segment: {tier}"),
                });
            }
            let path = mcp_paths::tier_file(tier);
            return read_file_optional(path).await;
        }

        if let Some(rest) = uri.strip_prefix("cascade://memory/") {
            let (project, file) =
                split_two(rest, '/').ok_or_else(|| McpServerError::InvalidParams {
                    detail: format!("cascade://memory/ requires {{project}}/{{file}}: {uri}"),
                })?;
            if !is_safe_segment(project) || !is_safe_segment(file) {
                return Err(McpServerError::InvalidParams {
                    detail: "unsafe path segment in memory URI".into(),
                });
            }
            let path = mcp_paths::memory_file(project, file);
            return read_file_optional(path).await;
        }

        if let Some(project) = uri.strip_prefix("cascade://inbox/") {
            if !is_safe_segment(project) {
                return Err(McpServerError::InvalidParams {
                    detail: format!("unsafe project segment in inbox URI: {project}"),
                });
            }
            let inbox = mcp_paths::inbox_dir(project);
            return read_inbox_as_json(inbox).await;
        }

        if let Some(rest) = uri.strip_prefix("cascade://master-list/") {
            let (project, kind) =
                split_two(rest, '/').ok_or_else(|| McpServerError::InvalidParams {
                    detail: format!("cascade://master-list/ requires {{project}}/{{kind}}: {uri}"),
                })?;
            if !is_safe_segment(project) || !is_safe_segment(kind) {
                return Err(McpServerError::InvalidParams {
                    detail: "unsafe path segment in master-list URI".into(),
                });
            }
            let file = if kind.ends_with(".md") {
                kind.to_string()
            } else {
                format!("MASTER-{}.md", kind.to_uppercase())
            };
            let path = mcp_paths::docs_file(project, &file);
            return read_file_optional(path).await;
        }

        if uri == "cascade://project_state" {
            return read_project_state().await;
        }

        if uri == "cascade://quota_state" {
            return read_quota_state().await;
        }

        if let Some(tier) = uri.strip_prefix("cascade://instructions/") {
            if !is_safe_segment(tier) {
                return Err(McpServerError::InvalidParams {
                    detail: format!("unsafe tier segment in instructions URI: {tier}"),
                });
            }
            const VALID_TIERS: &[&str] = &["gci", "pci", "apc", "ppc", "prc", "pac"];
            if !VALID_TIERS.contains(&tier) {
                return Err(McpServerError::InvalidParams {
                    detail: format!(
                        "unknown tier '{tier}'; valid values: gci, pci, apc, ppc, prc, pac"
                    ),
                });
            }
            let path = mcp_paths::tier_file(tier);
            return read_file_optional(path).await;
        }

        if let Some(rest) = uri.strip_prefix("cascade://pbd/") {
            return read_pbd_resource(rest).await;
        }

        Err(McpServerError::InvalidParams {
            detail: format!("unknown cascade:// URI scheme: {uri}"),
        })
    }
}

// ── Helper functions ──────────────────────────────────────────────────────────

/// Validate the raw URI string for null bytes or absolute path traversal attempts.
pub(super) fn validate_uri_safety(uri: &str) -> Result<(), McpServerError> {
    if uri.contains('\0') {
        return Err(McpServerError::InvalidParams {
            detail: "null byte in resource URI".into(),
        });
    }
    if uri.contains("..") {
        return Err(McpServerError::InvalidParams {
            detail: "path traversal detected in resource URI".into(),
        });
    }
    Ok(())
}

/// Returns `true` if `seg` is safe to use as a filesystem path segment.
pub(super) fn is_safe_segment(seg: &str) -> bool {
    !seg.is_empty()
        && !seg.contains('\0')
        && !seg.contains('\\')
        && seg != ".."
        && seg != "."
        && !seg.starts_with('/')
}

/// Split `s` at the first occurrence of `sep`, returning the two halves.
fn split_two(s: &str, sep: char) -> Option<(&str, &str)> {
    let pos = s.find(sep)?;
    Some((&s[..pos], &s[pos + 1..]))
}

/// Read a file, returning `Ok(None)` when not found.
async fn read_file_optional(path: PathBuf) -> Result<Option<String>, McpServerError> {
    match tokio::fs::read_to_string(&path).await {
        Ok(content) => Ok(Some(content)),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(McpServerError::Internal {
            detail: format!("I/O error reading {}: {e}", path.display()),
        }),
    }
}

/// Read all `.md` files in an inbox directory, returning them as a JSON array string.
async fn read_inbox_as_json(inbox: PathBuf) -> Result<Option<String>, McpServerError> {
    if !inbox.exists() {
        return Ok(Some("[]".into()));
    }
    let mut messages: Vec<Value> = Vec::new();
    let mut entries = tokio::fs::read_dir(&inbox)
        .await
        .map_err(|e| McpServerError::Internal {
            detail: format!("read_dir {}: {e}", inbox.display()),
        })?;
    while let Some(entry) = entries
        .next_entry()
        .await
        .map_err(|e| McpServerError::Internal {
            detail: format!("next_entry {}: {e}", inbox.display()),
        })?
    {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) == Some("md") {
            if let Ok(content) = tokio::fs::read_to_string(&path).await {
                let name = path
                    .file_name()
                    .and_then(|n| n.to_str())
                    .unwrap_or("")
                    .to_string();
                messages.push(serde_json::json!({ "file": name, "content": content }));
            }
        }
    }
    Ok(Some(
        serde_json::to_string(&messages).unwrap_or_else(|_| "[]".into()),
    ))
}

/// Read `~/.claude/temp/quota-state.json` and return its content.
async fn read_quota_state() -> Result<Option<String>, McpServerError> {
    let path = mcp_paths::quota_state_file();
    match tokio::fs::read_to_string(&path).await {
        Ok(content) => Ok(Some(content)),
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Some("{}".into())),
        Err(e) => Err(McpServerError::Internal {
            detail: format!("I/O error reading quota-state: {e}"),
        }),
    }
}

/// Read the active phase status from `.claude/phases/current/` and return compact JSON.
async fn read_project_state() -> Result<Option<String>, McpServerError> {
    let cwd = match std::env::current_dir() {
        Ok(d) => d,
        Err(_) => {
            return Ok(Some(
                serde_json::json!({"error": "cwd unavailable"}).to_string(),
            ))
        }
    };
    let phases_dir = mcp_paths::phases_current_dir(&cwd);

    if !phases_dir.exists() {
        return Ok(Some(
            serde_json::json!({
                "project": cwd.file_name().and_then(|n| n.to_str()).unwrap_or("unknown"),
                "phases_found": 0,
                "active_phases": [],
                "note": "no .claude/phases/current/ directory"
            })
            .to_string(),
        ));
    }

    let mut phases = Vec::new();
    let mut read_dir =
        tokio::fs::read_dir(&phases_dir)
            .await
            .map_err(|e| McpServerError::Internal {
                detail: format!("read_dir {}: {e}", phases_dir.display()),
            })?;

    while let Some(entry) = read_dir
        .next_entry()
        .await
        .map_err(|e| McpServerError::Internal {
            detail: format!("next_entry phases: {e}"),
        })?
    {
        let path = entry.path();
        if !path.is_dir() {
            continue;
        }
        let phase_name = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("")
            .to_string();
        if !phase_name.starts_with('p') {
            continue;
        }
        let status_file = path.join("status.yaml");
        let status_text = match tokio::fs::read_to_string(&status_file).await {
            Ok(t) => t,
            Err(_) => continue,
        };
        let is_done = status_text
            .lines()
            .any(|l| l.trim().starts_with("phase_status:") && l.contains("done"));
        if is_done {
            continue;
        }

        let summary_lines: Vec<&str> = status_text.lines().take(20).collect();
        phases.push(serde_json::json!({
            "phase": phase_name,
            "status_summary": summary_lines.join("\n"),
        }));
    }

    let result = serde_json::json!({
        "project": cwd.file_name().and_then(|n| n.to_str()).unwrap_or("unknown"),
        "cwd": cwd.to_string_lossy(),
        "phases_found": phases.len(),
        "active_phases": phases,
    });
    Ok(Some(result.to_string()))
}

/// Read a `cascade://pbd/...` resource.
async fn read_pbd_resource(rest: &str) -> Result<Option<String>, McpServerError> {
    if rest == "current" {
        return tokio::task::spawn_blocking(|| {
            let store = PbdStore::new(resolve_phases_root(None));
            match store.read_current() {
                Ok(current) => {
                    let json = serde_json::to_string(&current).unwrap_or_else(|_| "{}".into());
                    Ok(Some(json))
                }
                Err(e) => Err(McpServerError::Internal {
                    detail: format!("read_current: {e}"),
                }),
            }
        })
        .await
        .map_err(|e| McpServerError::Internal {
            detail: format!("spawn_blocking: {e}"),
        })?;
    }

    let parts: Vec<&str> = rest.splitn(5, '/').collect();
    if parts.len() == 5 {
        for seg in &parts {
            if !is_safe_segment(seg) {
                return Err(McpServerError::InvalidParams {
                    detail: format!("unsafe PBD path segment: {seg}"),
                });
            }
        }
        let (phase_id, epic_id, wave_id, sprint_id, ticket_id) = (
            parts[0].to_string(),
            parts[1].to_string(),
            parts[2].to_string(),
            parts[3].to_string(),
            parts[4].to_string(),
        );
        return tokio::task::spawn_blocking(move || {
            let store = PbdStore::new(resolve_phases_root(None));
            match store.load_ticket(&phase_id, &epic_id, &wave_id, &sprint_id, &ticket_id) {
                Ok(ticket) => {
                    let yaml = serde_yaml::to_string(&ticket)
                        .unwrap_or_else(|_| format!("id: {ticket_id}"));
                    Ok(Some(yaml))
                }
                Err(e) => {
                    if e.to_string().contains("No such file")
                        || e.to_string().contains("os error 2")
                    {
                        Ok(None)
                    } else {
                        Err(McpServerError::Internal {
                            detail: format!("load_ticket: {e}"),
                        })
                    }
                }
            }
        })
        .await
        .map_err(|e| McpServerError::Internal {
            detail: format!("spawn_blocking: {e}"),
        })?;
    }

    Err(McpServerError::InvalidParams {
        detail: format!(
            "cascade://pbd/ path must be 'current' or '{{phase}}/{{epic}}/{{wave}}/{{sprint}}/{{ticket}}'; got: '{rest}'"
        ),
    })
}

// ── Static resource catalog ───────────────────────────────────────────────────

/// Build the full static resource catalog.
pub fn build_catalog() -> Vec<McpResource> {
    let mut catalog = Vec::with_capacity(16);

    for (tier, name, desc) in &[
        (
            "gci",
            "GCI — Global Config Instructions",
            "Global instructions for all AI work (~/.cascade/CASCADE.md)",
        ),
        (
            "asi",
            "ASI — All-Sites Instructions",
            "Instructions for all projects under ~/Sites/",
        ),
        (
            "ppc",
            "PPC — Project-level Config",
            "Active project instructions",
        ),
        (
            "prc",
            "PRC — Repo-level Config",
            "Current repository instructions",
        ),
        (
            "pac",
            "PAC — App-level Config",
            "Current app-subdirectory instructions",
        ),
    ] {
        catalog.push(McpResource {
            uri: format!("cascade://tier/{tier}"),
            name: name.to_string(),
            description: Some(desc.to_string()),
            mime_type: Some("text/markdown".into()),
        });
    }

    catalog.push(McpResource {
        uri: "cascade://project_state".into(),
        name: "Project State".into(),
        description: Some(
            "Active PEWS phase status, ticket counts, and task summary for the current project"
                .into(),
        ),
        mime_type: Some("application/json".into()),
    });
    catalog.push(McpResource {
        uri: "cascade://quota_state".into(),
        name: "Quota State".into(),
        description: Some("CC/OC quota levels, available accounts, and reset timestamps".into()),
        mime_type: Some("application/json".into()),
    });
    for tier in &["gci", "pci", "apc", "ppc", "prc", "pac"] {
        catalog.push(McpResource {
            uri: format!("cascade://instructions/{tier}"),
            name: format!("Instructions — {}", tier.to_uppercase()),
            description: Some(format!(
                "Resolved instruction text for the {tier} cascade tier"
            )),
            mime_type: Some("text/markdown".into()),
        });
    }

    for project in &["nself", "acamarata", "ummeco", "unyeco"] {
        for file in &["decisions.md", "lessons.md", "patterns.md"] {
            catalog.push(McpResource {
                uri: format!("cascade://memory/{project}/{file}"),
                name: format!("{project} memory/{file}"),
                description: Some(format!("Memory file {file} for project {project}")),
                mime_type: Some("text/markdown".into()),
            });
        }
    }

    for project in &["nself", "acamarata", "ummeco", "unyeco", "downloads"] {
        catalog.push(McpResource {
            uri: format!("cascade://inbox/{project}"),
            name: format!("{project} inbox"),
            description: Some(format!("PCI inbox messages for project {project}")),
            mime_type: Some("application/json".into()),
        });
    }

    catalog.push(McpResource {
        uri: "cascade://pbd/current".into(),
        name: "PBD current pointers".into(),
        description: Some(
            "current.yaml active pointers (phase/epic/wave/sprint/tickets) as JSON".into(),
        ),
        mime_type: Some("application/json".into()),
    });
    catalog.push(McpResource {
        uri: "cascade://pbd/{phase}/{epic}/{wave}/{sprint}/{ticket}".into(),
        name: "PBD ticket YAML".into(),
        description: Some(
            "ticket.yaml for a specific ticket (substitute real IDs for {phase}/{epic}/{wave}/{sprint}/{ticket})"
                .into(),
        ),
        mime_type: Some("text/yaml".into()),
    });

    // Unused PAGE_SIZE reference to satisfy the import
    let _ = PAGE_SIZE;

    catalog
}
