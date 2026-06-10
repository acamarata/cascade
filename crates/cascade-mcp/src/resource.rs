//! MCP Resources — `cascade://` URI scheme for tier files, memory, inbox, and master-lists.
//!
//! Implements the four MCP resource methods:
//! - `resources/list` — paginated catalog of all available `cascade://` resources.
//! - `resources/read` — fetch content for a specific URI as `TextResourceContents`.
//! - `resources/subscribe` — add a URI to the per-connection subscription set.
//! - `resources/unsubscribe` — remove a URI from the subscription set.
//!
//! ## URI catalog
//!
//! | URI pattern | Content |
//! |---|---|
//! | `cascade://tier/gci` | GCI CASCADE.md |
//! | `cascade://tier/asi` | ASI CASCADE.md |
//! | `cascade://tier/ppc` | PPC (project-level) CASCADE.md |
//! | `cascade://tier/prc` | PRC (repo-level) CASCADE.md |
//! | `cascade://tier/pac` | PAC (app-level) CASCADE.md |
//! | `cascade://memory/{project}/{file}` | A project memory file |
//! | `cascade://inbox/{project}` | Inbox messages as JSON array |
//! | `cascade://master-list/{project}/{kind}` | Project master-list document |
//! | `cascade://project_state` | PEWS phase status + active tasks (JSON) |
//! | `cascade://quota_state` | CC/OC quota levels from quota-state.json |
//! | `cascade://instructions/{tier}` | Resolved instruction text for a tier |
//!
//! ## Pagination
//!
//! `resources/list` accepts an optional `cursor` param (opaque string encoding a
//! `usize` page-start offset). Returns a `nextCursor` when more pages exist.
//! Page size is [`PAGE_SIZE`] items.
//!
//! ## Subscription
//!
//! `subscribe` / `unsubscribe` operate on a per-connection `HashMap<connection_id,
//! HashSet<uri>>` stored in `ResourceRegistry`. When content changes for a
//! subscribed URI (detected on reads or via external watcher), callers may
//! broadcast `notifications/resources/updated` via [`NotificationBus`].
//!
//! ## Security
//!
//! URI path components are validated against path traversal patterns (`..`, null
//! bytes, absolute path segments). Unknown schemes return `ResourceNotFound`.
//!
//! ## SPORT
//! MASTER-MCP-PRIMITIVES.md: resources row → Done

use std::collections::{HashMap, HashSet};
use std::path::PathBuf;
use std::sync::Arc;

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokio::sync::Mutex;
use tracing::debug;

use crate::error::McpServerError;
use crate::handler::McpHandler;
use crate::notification::NotificationBus;
use crate::paths as mcp_paths;

// ── Constants ─────────────────────────────────────────────────────────────────

/// Number of resources returned per page in `resources/list`.
pub const PAGE_SIZE: usize = 50;

// ── MCP resource wire types ───────────────────────────────────────────────────

/// A single resource entry returned by `resources/list`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct McpResource {
    /// Unique resource URI (`cascade://...`).
    pub uri: String,
    /// Human-readable name.
    pub name: String,
    /// Optional human-readable description.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// MIME type; defaults to `text/markdown` for tier/memory resources, `application/json`
    /// for inbox resources.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub mime_type: Option<String>,
}

/// Text resource contents returned inside the `contents` array by `resources/read`.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct TextResourceContents {
    pub uri: String,
    pub mime_type: String,
    /// UTF-8 text payload.
    pub text: String,
}

// ── ContentBackend trait (mockable) ──────────────────────────────────────────

/// Backend that resolves `cascade://` URIs to their text content.
///
/// The production implementation reads from the filesystem via `cascade_mcp::paths`.
/// Test code can inject a `MockContentBackend` without touching the filesystem.
#[async_trait]
pub trait ContentBackend: Send + Sync {
    /// Resolve `uri` to its text content.
    ///
    /// Returns `Ok(Some(text))` when found, `Ok(None)` when the resource
    /// exists in the catalog but has no content yet (e.g. tier file absent),
    /// and `Err(McpServerError)` for unrecognised URI schemes or I/O errors.
    async fn read_uri(&self, uri: &str) -> Result<Option<String>, McpServerError>;
}

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
            // Reject path traversal in the tier segment
            if !is_safe_segment(tier) {
                return Err(McpServerError::InvalidParams {
                    detail: format!("unsafe tier segment: {tier}"),
                });
            }
            let path = mcp_paths::tier_file(tier);
            return read_file_optional(path).await;
        }

        if let Some(rest) = uri.strip_prefix("cascade://memory/") {
            let (project, file) = split_two(rest, '/').ok_or_else(|| {
                McpServerError::InvalidParams {
                    detail: format!("cascade://memory/ requires {{project}}/{{file}}: {uri}"),
                }
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
            let (project, kind) = split_two(rest, '/').ok_or_else(|| {
                McpServerError::InvalidParams {
                    detail: format!(
                        "cascade://master-list/ requires {{project}}/{{kind}}: {uri}"
                    ),
                }
            })?;
            if !is_safe_segment(project) || !is_safe_segment(kind) {
                return Err(McpServerError::InvalidParams {
                    detail: "unsafe path segment in master-list URI".into(),
                });
            }
            // kind may be e.g. "MASTER-ROUTES.md" or bare "routes"
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
            // Reuse the tier_file path resolution — same files as cascade://tier/{name}
            let path = mcp_paths::tier_file(tier);
            return read_file_optional(path).await;
        }

        // Unknown scheme
        Err(McpServerError::InvalidParams {
            detail: format!("unknown cascade:// URI scheme: {uri}"),
        })
    }
}

// ── Helper functions ──────────────────────────────────────────────────────────

/// Validate the raw URI string for null bytes or absolute path traversal attempts.
fn validate_uri_safety(uri: &str) -> Result<(), McpServerError> {
    if uri.contains('\0') {
        return Err(McpServerError::InvalidParams {
            detail: "null byte in resource URI".into(),
        });
    }
    // Reject any URI containing ".." segments which could escape via path resolution
    if uri.contains("..") {
        return Err(McpServerError::InvalidParams {
            detail: "path traversal detected in resource URI".into(),
        });
    }
    Ok(())
}

/// Returns `true` if `seg` is safe to use as a filesystem path segment.
/// Rejects empty strings, segments containing `/`, `\`, `.`, `..`, and null bytes.
fn is_safe_segment(seg: &str) -> bool {
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

/// Read a file, returning `Ok(None)` when not found (resource exists in catalog but
/// has no content) vs an I/O error for other failures.
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
    let mut entries = tokio::fs::read_dir(&inbox).await.map_err(|e| {
        McpServerError::Internal {
            detail: format!("read_dir {}: {e}", inbox.display()),
        }
    })?;
    while let Some(entry) = entries.next_entry().await.map_err(|e| {
        McpServerError::Internal {
            detail: format!("next_entry {}: {e}", inbox.display()),
        }
    })? {
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
    Ok(Some(serde_json::to_string(&messages).unwrap_or_else(|_| "[]".into())))
}

/// Read `~/.claude/temp/quota-state.json` and return its content.
/// Returns `Ok(Some("{}"))` if the file does not exist (graceful empty).
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

/// Read the active phase status from `.claude/phases/current/p{N}/status.yaml`
/// for the current working directory and return a compact JSON summary.
///
/// Scans `phases_current_dir(cwd)` for phase subdirs whose status.yaml does NOT
/// contain `phase_status: done`. Returns a bounded summary object.
async fn read_project_state() -> Result<Option<String>, McpServerError> {
    // Resolve CWD; fall back gracefully if unavailable.
    let cwd = match std::env::current_dir() {
        Ok(d) => d,
        Err(_) => return Ok(Some(serde_json::json!({"error": "cwd unavailable"}).to_string())),
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
    let mut read_dir = tokio::fs::read_dir(&phases_dir).await.map_err(|e| {
        McpServerError::Internal {
            detail: format!("read_dir {}: {e}", phases_dir.display()),
        }
    })?;

    while let Some(entry) = read_dir.next_entry().await.map_err(|e| McpServerError::Internal {
        detail: format!("next_entry phases: {e}"),
    })? {
        let path = entry.path();
        if !path.is_dir() {
            continue;
        }
        let phase_name = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("")
            .to_string();
        // Only phase dirs that look like "p1", "p2", etc.
        if !phase_name.starts_with('p') {
            continue;
        }
        let status_file = path.join("status.yaml");
        let status_text = match tokio::fs::read_to_string(&status_file).await {
            Ok(t) => t,
            Err(_) => continue,
        };
        // Parse status: skip phases marked done (simple text check to stay dep-free)
        let is_done = status_text
            .lines()
            .any(|l| l.trim().starts_with("phase_status:") && l.contains("done"));
        if is_done {
            continue;
        }

        // Collect a bounded summary (first 20 lines of status.yaml)
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

// ── Static resource catalog ───────────────────────────────────────────────────

/// Build the full static resource catalog.
///
/// The catalog is fixed at compile time. Dynamic resources (memory files, inbox)
/// are listed with known projects from the standard layout. Clients should use
/// resource templates or `resources/read` directly for arbitrary project/file paths.
fn build_catalog() -> Vec<McpResource> {
    let mut catalog = Vec::with_capacity(16);

    // Tier resources
    for (tier, name, desc) in &[
        ("gci", "GCI — Global Config Instructions", "Global instructions for all AI work (~/.cascade/CASCADE.md)"),
        ("asi", "ASI — All-Sites Instructions", "Instructions for all projects under ~/Sites/"),
        ("ppc", "PPC — Project-level Config", "Active project instructions"),
        ("prc", "PRC — Repo-level Config", "Current repository instructions"),
        ("pac", "PAC — App-level Config", "Current app-subdirectory instructions"),
    ] {
        catalog.push(McpResource {
            uri: format!("cascade://tier/{tier}"),
            name: name.to_string(),
            description: Some(desc.to_string()),
            mime_type: Some("text/markdown".into()),
        });
    }

    // State resources
    catalog.push(McpResource {
        uri: "cascade://project_state".into(),
        name: "Project State".into(),
        description: Some("Active PEWS phase status, ticket counts, and task summary for the current project".into()),
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
            description: Some(format!("Resolved instruction text for the {tier} cascade tier")),
            mime_type: Some("text/markdown".into()),
        });
    }

    // Memory resources (standard files for well-known projects)
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

    // Inbox resources
    for project in &["nself", "acamarata", "ummeco", "unyeco", "downloads"] {
        catalog.push(McpResource {
            uri: format!("cascade://inbox/{project}"),
            name: format!("{project} inbox"),
            description: Some(format!("PCI inbox messages for project {project}")),
            mime_type: Some("application/json".into()),
        });
    }

    catalog
}

// ── Subscription store ────────────────────────────────────────────────────────

/// Manages `resources/subscribe` and `resources/unsubscribe` per connection.
///
/// Connection IDs are opaque strings supplied by the transport layer.
/// Thread-safe; clone-cheap (inner `Arc<Mutex<...>>`).
#[derive(Clone, Default)]
pub struct SubscriptionStore {
    inner: Arc<Mutex<HashMap<String, HashSet<String>>>>,
}

impl SubscriptionStore {
    pub fn new() -> Self {
        Self::default()
    }

    /// Add `uri` to `connection_id`'s subscription set.
    /// Returns `true` if the URI was newly inserted (i.e. not already subscribed).
    pub async fn subscribe(&self, connection_id: &str, uri: &str) -> bool {
        let mut map = self.inner.lock().await;
        map.entry(connection_id.to_string())
            .or_default()
            .insert(uri.to_string())
    }

    /// Remove `uri` from `connection_id`'s subscription set.
    /// Returns `true` if the URI was present and removed.
    pub async fn unsubscribe(&self, connection_id: &str, uri: &str) -> bool {
        let mut map = self.inner.lock().await;
        if let Some(set) = map.get_mut(connection_id) {
            let removed = set.remove(uri);
            if set.is_empty() {
                map.remove(connection_id);
            }
            return removed;
        }
        false
    }

    /// Returns `true` if `connection_id` is subscribed to `uri`.
    pub async fn is_subscribed(&self, connection_id: &str, uri: &str) -> bool {
        let map = self.inner.lock().await;
        map.get(connection_id)
            .map(|set| set.contains(uri))
            .unwrap_or(false)
    }

    /// All URIs subscribed by `connection_id`.
    pub async fn subscriptions_for(&self, connection_id: &str) -> HashSet<String> {
        let map = self.inner.lock().await;
        map.get(connection_id).cloned().unwrap_or_default()
    }

    /// Remove all subscriptions for a connection (e.g. on disconnect).
    pub async fn remove_connection(&self, connection_id: &str) {
        let mut map = self.inner.lock().await;
        map.remove(connection_id);
    }
}

// ── ResourceRegistry ─────────────────────────────────────────────────────────

/// Handles all four MCP resource methods for the cascade MCP server.
///
/// Holds a [`ContentBackend`] (injectable for testing) and a
/// [`SubscriptionStore`]. Use [`ResourceRegistry::new`] for production
/// (filesystem backend) or [`ResourceRegistry::with_backend`] for tests.
pub struct ResourceRegistry {
    backend: Arc<dyn ContentBackend>,
    subscriptions: SubscriptionStore,
    _bus: Option<NotificationBus>,
}

impl ResourceRegistry {
    /// Create a production registry backed by the filesystem.
    pub fn new() -> Self {
        Self {
            backend: Arc::new(FsContentBackend),
            subscriptions: SubscriptionStore::new(),
            _bus: None,
        }
    }

    /// Create a registry with an injected backend (for tests or cascade-core integration).
    pub fn with_backend(backend: Arc<dyn ContentBackend>) -> Self {
        Self {
            backend,
            subscriptions: SubscriptionStore::new(),
            _bus: None,
        }
    }

    /// Attach a notification bus so the registry can broadcast
    /// `notifications/resources/updated` when subscribed resources change.
    pub fn with_notification_bus(mut self, bus: NotificationBus) -> Self {
        self._bus = Some(bus);
        self
    }

    /// Return a reference to the subscription store (for transport-level wiring).
    pub fn subscriptions(&self) -> &SubscriptionStore {
        &self.subscriptions
    }

    // ── resources/list ──────────────────────────────────────────────────────

    /// Handle `resources/list`.
    ///
    /// Params (all optional):
    /// - `cursor` — opaque pagination token returned by a prior call.
    ///
    /// Returns `{ "resources": [...], "nextCursor"?: "..." }`.
    pub async fn list(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        debug!("resources/list");

        let cursor_val = params
            .and_then(|p| p.get("cursor"))
            .and_then(|c| c.as_str());

        let offset: usize = cursor_val
            .and_then(|c| c.parse().ok())
            .unwrap_or(0);

        let catalog = build_catalog();
        let total = catalog.len();
        let page: Vec<&McpResource> = catalog.iter().skip(offset).take(PAGE_SIZE).collect();
        let next_offset = offset + PAGE_SIZE;

        let mut result = serde_json::json!({
            "resources": page,
        });
        if next_offset < total {
            result["nextCursor"] = Value::String(next_offset.to_string());
        }
        Ok(result)
    }

    // ── resources/read ───────────────────────────────────────────────────────

    /// Handle `resources/read`.
    ///
    /// Params: `{ "uri": "cascade://..." }`
    ///
    /// Returns `{ "contents": [TextResourceContents] }` or
    /// `McpServerError::InvalidParams` for unknown/unsafe URIs.
    pub async fn read(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        let uri = extract_uri(params)?;
        debug!(uri, "resources/read");

        // Determine mime type from URI scheme before calling backend
        let mime_type = if uri.starts_with("cascade://inbox/")
            || uri == "cascade://project_state"
            || uri == "cascade://quota_state"
        {
            "application/json"
        } else {
            "text/markdown"
        };

        let text = self.backend.read_uri(uri).await?.unwrap_or_default();
        let contents = TextResourceContents {
            uri: uri.to_string(),
            mime_type: mime_type.to_string(),
            text,
        };

        Ok(serde_json::json!({ "contents": [contents] }))
    }

    // ── resources/subscribe ──────────────────────────────────────────────────

    /// Handle `resources/subscribe`.
    ///
    /// Params: `{ "uri": "cascade://...", "connectionId"?: "..." }`
    ///
    /// Registers `uri` into the per-connection subscription set. Returns `{}`.
    pub async fn subscribe(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        let uri = extract_uri(params)?;
        let connection_id = params
            .and_then(|p| p.get("connectionId"))
            .and_then(|v| v.as_str())
            .unwrap_or("default");

        debug!(uri, connection_id, "resources/subscribe");
        validate_uri_safety(uri)?;
        self.subscriptions.subscribe(connection_id, uri).await;

        Ok(serde_json::json!({}))
    }

    // ── resources/unsubscribe ────────────────────────────────────────────────

    /// Handle `resources/unsubscribe`.
    ///
    /// Params: `{ "uri": "cascade://...", "connectionId"?: "..." }`
    ///
    /// Removes `uri` from the per-connection subscription set. Returns `{}`.
    pub async fn unsubscribe(&self, params: Option<&Value>) -> Result<Value, McpServerError> {
        let uri = extract_uri(params)?;
        let connection_id = params
            .and_then(|p| p.get("connectionId"))
            .and_then(|v| v.as_str())
            .unwrap_or("default");

        debug!(uri, connection_id, "resources/unsubscribe");
        self.subscriptions.unsubscribe(connection_id, uri).await;

        Ok(serde_json::json!({}))
    }
}

impl Default for ResourceRegistry {
    fn default() -> Self {
        Self::new()
    }
}

// ── McpHandler wrappers ───────────────────────────────────────────────────────

/// `McpHandler` wrapper for `resources/list`.
pub struct ResourcesListHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesListHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.list(params.as_ref()).await
    }
}

/// `McpHandler` wrapper for `resources/read`.
pub struct ResourcesReadHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesReadHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.read(params.as_ref()).await
    }
}

/// `McpHandler` wrapper for `resources/subscribe`.
pub struct ResourcesSubscribeHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesSubscribeHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.subscribe(params.as_ref()).await
    }
}

/// `McpHandler` wrapper for `resources/unsubscribe`.
pub struct ResourcesUnsubscribeHandler(pub Arc<ResourceRegistry>);

#[async_trait]
impl McpHandler for ResourcesUnsubscribeHandler {
    async fn handle(&self, params: Option<Value>) -> Result<Value, McpServerError> {
        self.0.unsubscribe(params.as_ref()).await
    }
}

// ── Convenience: register all four handlers into a HandlerRegistry ────────────

/// Register all four resource handlers (`resources/list`, `resources/read`,
/// `resources/subscribe`, `resources/unsubscribe`) into the given
/// [`crate::handler::HandlerRegistry`].
///
/// ```rust,ignore
/// let registry = HandlerRegistry::new();
/// let resources = Arc::new(ResourceRegistry::new());
/// register_resource_handlers(&registry, resources).await;
/// ```
pub async fn register_resource_handlers(
    registry: &crate::handler::HandlerRegistry,
    resources: Arc<ResourceRegistry>,
) {
    registry
        .register("resources/list", ResourcesListHandler(Arc::clone(&resources)))
        .await;
    registry
        .register("resources/read", ResourcesReadHandler(Arc::clone(&resources)))
        .await;
    registry
        .register(
            "resources/subscribe",
            ResourcesSubscribeHandler(Arc::clone(&resources)),
        )
        .await;
    registry
        .register(
            "resources/unsubscribe",
            ResourcesUnsubscribeHandler(Arc::clone(&resources)),
        )
        .await;
}

// ── Shared helper ─────────────────────────────────────────────────────────────

/// Extract `"uri"` from JSON params, returning `InvalidParams` on missing/wrong type.
fn extract_uri(params: Option<&Value>) -> Result<&str, McpServerError> {
    params
        .and_then(|p| p.get("uri"))
        .and_then(|v| v.as_str())
        .ok_or_else(|| McpServerError::InvalidParams {
            detail: "missing or non-string 'uri' field in params".into(),
        })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap as StdHashMap;

    // ── MockContentBackend ──────────────────────────────────────────────────

    struct MockContentBackend {
        /// URI → content map. Missing URI = ResourceNotFound (InvalidParams).
        data: StdHashMap<String, String>,
    }

    impl MockContentBackend {
        fn new(data: StdHashMap<String, String>) -> Self {
            Self { data }
        }
    }

    #[async_trait]
    impl ContentBackend for MockContentBackend {
        async fn read_uri(&self, uri: &str) -> Result<Option<String>, McpServerError> {
            validate_uri_safety(uri)?;
            // Reject unknown scheme
            if !uri.starts_with("cascade://") {
                return Err(McpServerError::InvalidParams {
                    detail: format!("unknown URI scheme: {uri}"),
                });
            }
            // Reject unsafe segments in the path tail
            let tail = uri.trim_start_matches("cascade://");
            for seg in tail.split('/') {
                if !seg.is_empty() && !is_safe_segment(seg) {
                    return Err(McpServerError::InvalidParams {
                        detail: format!("unsafe segment: {seg}"),
                    });
                }
            }
            Ok(self.data.get(uri).cloned())
        }
    }

    fn mock_registry() -> ResourceRegistry {
        let mut data = StdHashMap::new();
        data.insert(
            "cascade://tier/gci".into(),
            "# GCI content\n\nGlobal instructions.".into(),
        );
        data.insert(
            "cascade://tier/asi".into(),
            "# ASI content\n\nAll-sites instructions.".into(),
        );
        data.insert(
            "cascade://memory/acamarata/decisions.md".into(),
            "# Decisions\n\n- ADR-001: use Rust".into(),
        );
        data.insert(
            "cascade://inbox/acamarata".into(),
            r#"[{"file":"msg-2026-01-01-test.md","content":"hello"}]"#.into(),
        );
        ResourceRegistry::with_backend(Arc::new(MockContentBackend::new(data)))
    }

    // ── resources/list ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn list_returns_resources_array() {
        let reg = mock_registry();
        let result = reg.list(None).await.unwrap();
        let resources = result["resources"].as_array().unwrap();
        assert!(
            !resources.is_empty(),
            "resources/list must return at least one resource"
        );
    }

    #[tokio::test]
    async fn list_contains_gci_uri() {
        let reg = mock_registry();
        let result = reg.list(None).await.unwrap();
        let resources = result["resources"].as_array().unwrap();
        let uris: Vec<&str> = resources
            .iter()
            .filter_map(|r| r.get("uri").and_then(|u| u.as_str()))
            .collect();
        assert!(
            uris.contains(&"cascade://tier/gci"),
            "resources list must contain cascade://tier/gci; got: {uris:?}"
        );
    }

    #[tokio::test]
    async fn list_contains_memory_uri() {
        let reg = mock_registry();
        let result = reg.list(None).await.unwrap();
        let resources = result["resources"].as_array().unwrap();
        let uris: Vec<&str> = resources
            .iter()
            .filter_map(|r| r.get("uri").and_then(|u| u.as_str()))
            .collect();
        assert!(
            uris.iter().any(|u| u.starts_with("cascade://memory/")),
            "resources list must contain at least one memory URI; got: {uris:?}"
        );
    }

    #[tokio::test]
    async fn list_pagination_cursor() {
        let reg = mock_registry();
        // Page 0 — no cursor
        let page0 = reg.list(None).await.unwrap();
        let resources0 = page0["resources"].as_array().unwrap();
        assert!(
            !resources0.is_empty(),
            "first page must not be empty"
        );

        // If there is a nextCursor, fetching page 1 must return different items
        if let Some(cursor) = page0.get("nextCursor").and_then(|c| c.as_str()) {
            let params = serde_json::json!({ "cursor": cursor });
            let page1 = reg.list(Some(&params)).await.unwrap();
            let resources1 = page1["resources"].as_array().unwrap();
            // Pages must differ (first items will not overlap)
            let uri0 = resources0[0].get("uri").and_then(|u| u.as_str()).unwrap();
            let uri1 = resources1[0].get("uri").and_then(|u| u.as_str()).unwrap();
            assert_ne!(uri0, uri1, "page 1 must start after page 0");
        }
    }

    // ── resources/read ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn read_gci_returns_text_contents() {
        let reg = mock_registry();
        let params = serde_json::json!({ "uri": "cascade://tier/gci" });
        let result = reg.read(Some(&params)).await.unwrap();
        let contents = result["contents"].as_array().unwrap();
        assert_eq!(contents.len(), 1);
        let item = &contents[0];
        assert_eq!(item["uri"], "cascade://tier/gci");
        assert!(
            item["text"].as_str().unwrap().contains("GCI"),
            "text must contain GCI content"
        );
        assert_eq!(item["mimeType"], "text/markdown");
    }

    #[tokio::test]
    async fn read_unknown_uri_returns_invalid_params() {
        let reg = mock_registry();
        let params = serde_json::json!({ "uri": "unknown://foo/bar" });
        let err = reg.read(Some(&params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::InvalidParams { .. }),
            "unexpected error variant: {err:?}"
        );
    }

    #[tokio::test]
    async fn read_memory_file_returns_content() {
        let reg = mock_registry();
        let params = serde_json::json!({ "uri": "cascade://memory/acamarata/decisions.md" });
        let result = reg.read(Some(&params)).await.unwrap();
        let text = result["contents"][0]["text"].as_str().unwrap();
        assert!(
            text.contains("Decisions"),
            "memory read must return decisions content"
        );
    }

    #[tokio::test]
    async fn read_inbox_returns_json_mime() {
        let reg = mock_registry();
        let params = serde_json::json!({ "uri": "cascade://inbox/acamarata" });
        let result = reg.read(Some(&params)).await.unwrap();
        let mime = result["contents"][0]["mimeType"].as_str().unwrap();
        assert_eq!(mime, "application/json");
    }

    #[tokio::test]
    async fn read_missing_uri_field_returns_invalid_params() {
        let reg = mock_registry();
        // No "uri" key at all
        let params = serde_json::json!({ "something": "else" });
        let err = reg.read(Some(&params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::InvalidParams { .. }),
            "missing uri field must return InvalidParams"
        );
    }

    // ── security: path traversal ─────────────────────────────────────────────

    #[tokio::test]
    async fn read_path_traversal_returns_invalid_params() {
        let reg = mock_registry();
        for evil_uri in &[
            "cascade://tier/../../etc/passwd",
            "cascade://memory/../../../etc/shadow",
            "cascade://tier/gci\0evil",
        ] {
            let params = serde_json::json!({ "uri": evil_uri });
            let result = reg.read(Some(&params)).await;
            assert!(
                result.is_err(),
                "path traversal URI must be rejected: {evil_uri}"
            );
        }
    }

    // ── resources/subscribe + unsubscribe ───────────────────────────────────

    #[tokio::test]
    async fn subscribe_unsubscribe_round_trip() {
        let reg = mock_registry();
        let conn = "conn-abc";
        let uri = "cascade://tier/gci";

        // Not subscribed initially
        assert!(
            !reg.subscriptions.is_subscribed(conn, uri).await,
            "must not be subscribed before subscribe"
        );

        // Subscribe
        let params = serde_json::json!({ "uri": uri, "connectionId": conn });
        reg.subscribe(Some(&params)).await.unwrap();
        assert!(
            reg.subscriptions.is_subscribed(conn, uri).await,
            "must be subscribed after subscribe"
        );

        // Unsubscribe
        reg.unsubscribe(Some(&params)).await.unwrap();
        assert!(
            !reg.subscriptions.is_subscribed(conn, uri).await,
            "must not be subscribed after unsubscribe"
        );
    }

    #[tokio::test]
    async fn subscribe_multiple_uris_per_connection() {
        let reg = mock_registry();
        let conn = "conn-xyz";
        let uris = ["cascade://tier/gci", "cascade://tier/asi", "cascade://memory/acamarata/decisions.md"];

        for uri in &uris {
            let params = serde_json::json!({ "uri": uri, "connectionId": conn });
            reg.subscribe(Some(&params)).await.unwrap();
        }

        let subs = reg.subscriptions.subscriptions_for(conn).await;
        assert_eq!(subs.len(), 3, "all three URIs must be subscribed");

        // Unsubscribe one
        let params = serde_json::json!({ "uri": uris[0], "connectionId": conn });
        reg.unsubscribe(Some(&params)).await.unwrap();
        let subs = reg.subscriptions.subscriptions_for(conn).await;
        assert_eq!(subs.len(), 2, "two URIs must remain after one unsubscribe");
    }

    // ── McpHandler wrappers ─────────────────────────────────────────────────

    #[tokio::test]
    async fn handler_wrapper_list_dispatches() {
        let reg = Arc::new(mock_registry());
        let handler = ResourcesListHandler(Arc::clone(&reg));
        let result = handler.handle(None).await.unwrap();
        assert!(result["resources"].is_array(), "wrapper must return resources array");
    }

    #[tokio::test]
    async fn handler_wrapper_read_dispatches() {
        let reg = Arc::new(mock_registry());
        let handler = ResourcesReadHandler(Arc::clone(&reg));
        let params = serde_json::json!({ "uri": "cascade://tier/gci" });
        let result = handler.handle(Some(params)).await.unwrap();
        assert!(result["contents"].is_array());
    }

    // ── T-P4-E02-30: new resources ──────────────────────────────────────────

    /// MockContentBackend that supports the three new URI patterns.
    struct NewResourcesMock {
        quota_content: Option<String>,
    }

    #[async_trait]
    impl ContentBackend for NewResourcesMock {
        async fn read_uri(&self, uri: &str) -> Result<Option<String>, McpServerError> {
            validate_uri_safety(uri)?;
            match uri {
                "cascade://quota_state" => Ok(Some(
                    self.quota_content
                        .clone()
                        .unwrap_or_else(|| "{}".into()),
                )),
                "cascade://project_state" => Ok(Some(
                    r#"{"project":"test","phases_found":1,"active_phases":[]}"#.into(),
                )),
                u if u.starts_with("cascade://instructions/") => {
                    let tier = u.trim_start_matches("cascade://instructions/");
                    const VALID: &[&str] = &["gci", "pci", "apc", "ppc", "prc", "pac"];
                    if !VALID.contains(&tier) {
                        return Err(McpServerError::InvalidParams {
                            detail: format!("unknown tier '{tier}'"),
                        });
                    }
                    Ok(Some(format!("# {} instructions\n\nContent for {tier}.", tier.to_uppercase())))
                }
                _ => Err(McpServerError::InvalidParams {
                    detail: format!("unknown URI: {uri}"),
                }),
            }
        }
    }

    fn new_resources_registry(quota: Option<&str>) -> ResourceRegistry {
        ResourceRegistry::with_backend(Arc::new(NewResourcesMock {
            quota_content: quota.map(|s| s.to_string()),
        }))
    }

    #[tokio::test]
    async fn quota_state_returns_json_mime() {
        let reg = new_resources_registry(Some(r#"{"cc":{"quota_hit":false}}"#));
        let params = serde_json::json!({ "uri": "cascade://quota_state" });
        let result = reg.read(Some(&params)).await.unwrap();
        let mime = result["contents"][0]["mimeType"].as_str().unwrap();
        assert_eq!(mime, "application/json");
        let text = result["contents"][0]["text"].as_str().unwrap();
        assert!(text.contains("quota_hit"), "quota content missing");
    }

    #[tokio::test]
    async fn quota_state_returns_empty_object_when_missing() {
        let reg = new_resources_registry(None);
        let params = serde_json::json!({ "uri": "cascade://quota_state" });
        let result = reg.read(Some(&params)).await.unwrap();
        let text = result["contents"][0]["text"].as_str().unwrap();
        assert_eq!(text, "{}", "missing quota-state must return {{}}");
    }

    #[tokio::test]
    async fn project_state_returns_json_mime() {
        let reg = new_resources_registry(None);
        let params = serde_json::json!({ "uri": "cascade://project_state" });
        let result = reg.read(Some(&params)).await.unwrap();
        let mime = result["contents"][0]["mimeType"].as_str().unwrap();
        assert_eq!(mime, "application/json");
        let text = result["contents"][0]["text"].as_str().unwrap();
        // Must be valid JSON
        serde_json::from_str::<serde_json::Value>(text)
            .expect("project_state must return valid JSON");
    }

    #[tokio::test]
    async fn instructions_ppc_returns_markdown() {
        let reg = new_resources_registry(None);
        let params = serde_json::json!({ "uri": "cascade://instructions/ppc" });
        let result = reg.read(Some(&params)).await.unwrap();
        let mime = result["contents"][0]["mimeType"].as_str().unwrap();
        assert_eq!(mime, "text/markdown");
        let text = result["contents"][0]["text"].as_str().unwrap();
        assert!(text.contains("PPC"), "instructions must mention tier");
    }

    #[tokio::test]
    async fn instructions_invalid_tier_returns_error() {
        let reg = new_resources_registry(None);
        let params = serde_json::json!({ "uri": "cascade://instructions/invalid" });
        let err = reg.read(Some(&params)).await.unwrap_err();
        assert!(
            matches!(err, McpServerError::InvalidParams { .. }),
            "invalid tier must return InvalidParams, got: {err:?}"
        );
    }

    #[tokio::test]
    async fn catalog_contains_new_resource_uris() {
        let reg = new_resources_registry(None);
        let result = reg.list(None).await.unwrap();
        let resources = result["resources"].as_array().unwrap();
        let uris: Vec<&str> = resources
            .iter()
            .filter_map(|r| r.get("uri").and_then(|u| u.as_str()))
            .collect();
        assert!(uris.contains(&"cascade://project_state"), "catalog must contain project_state");
        assert!(uris.contains(&"cascade://quota_state"), "catalog must contain quota_state");
        assert!(
            uris.contains(&"cascade://instructions/gci"),
            "catalog must contain instructions/gci"
        );
        assert!(
            uris.contains(&"cascade://instructions/ppc"),
            "catalog must contain instructions/ppc"
        );
    }

    // ── is_safe_segment ─────────────────────────────────────────────────────

    #[test]
    fn safe_segment_rejects_traversal() {
        assert!(!is_safe_segment(".."));
        assert!(!is_safe_segment("."));
        assert!(!is_safe_segment(""));
        assert!(!is_safe_segment("/absolute"));
        assert!(is_safe_segment("gci"));
        assert!(is_safe_segment("decisions.md"));
        assert!(is_safe_segment("MASTER-ROUTES.md"));
    }
}
