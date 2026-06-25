//! PBD (Phase-Based Development) tool handlers.
//!
//! Implements: get_current, update_ticket_status, append_event, get_sprint,
//! read_phase_status, list_tickets, check_routes, scan_inbox.

use serde_json::Value;

use cascade_core::pbd::schema::{PbdEvent, TicketStatus};
use cascade_core::pbd::store::{resolve_phases_root, PbdStore};

use crate::paths as mcp_paths;
use crate::server::JsonRpcError;

// ── PBD tool handlers (E-P8-04) ───────────────────────────────────────────────

/// `cascade.get_current` — return current.yaml active pointers.
///
/// Returns a compact JSON object bounded to <=200 tokens for session-boot use.
pub(super) async fn handle_get_current(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let current = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        store.read_current()
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("read_current: {e}")))?;

    // Build compact output (<=200 tokens: JSON with only populated fields)
    let mut obj = serde_json::Map::new();
    if let Some(p) = &current.active_phase {
        obj.insert("phase".into(), Value::String(p.clone()));
    }
    if let Some(e) = &current.active_epic {
        obj.insert("epic".into(), Value::String(e.clone()));
    }
    if let Some(w) = &current.active_wave {
        obj.insert("wave".into(), Value::String(w.clone()));
    }
    if let Some(s) = &current.active_sprint {
        obj.insert("sprint".into(), Value::String(s.clone()));
    }
    if !current.active_tickets.is_empty() {
        obj.insert(
            "tickets".into(),
            Value::Array(
                current
                    .active_tickets
                    .iter()
                    .map(|t| Value::String(t.clone()))
                    .collect(),
            ),
        );
    }

    let text = serde_json::to_string(&obj).unwrap_or_else(|_| "{}".into());

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "current": obj
    }))
}

/// `cascade.update_ticket_status` — transition a ticket status.
///
/// Looks up the ticket in the INDEX to find its full path, then delegates to
/// `PbdStore::transition_ticket`. Validates the status enum and transition graph.
pub(super) async fn handle_update_ticket_status(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let ticket_id = args
        .get("ticket_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'ticket_id' is required"))?
        .to_string();

    let status_str = args
        .get("status")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'status' is required"))?
        .to_string();

    let note = args
        .get("note")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());

    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());
    let ticket_id_for_resp = ticket_id.clone();

    let new_status = parse_ticket_status(&status_str)
        .ok_or_else(|| JsonRpcError::invalid_params(format!("invalid status: '{status_str}'")))?;

    let result = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        // Walk the index to locate ticket coordinates
        let index = store.read_index()?;
        let entry = index
            .entries
            .iter()
            .find(|e| e.kind == "ticket" && e.id == ticket_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "ticket '{ticket_id}' not found in INDEX.yaml"
                ))
            })?;
        // Resolve parent chain: ticket -> sprint -> wave -> epic -> phase
        let coords = resolve_ticket_coords(&store, &entry.id, entry.parent.as_deref())?;
        store.transition_ticket(
            &coords.phase_id,
            &coords.epic_id,
            &coords.wave_id,
            &coords.sprint_id,
            &ticket_id,
            new_status.clone(),
            note.as_deref(),
        )?;
        Ok::<String, cascade_types::error::CascadeError>(new_status.as_str().to_string())
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("transition: {e}")))?;

    Ok(serde_json::json!({
        "content": [{
            "type": "text",
            "text": format!("ticket '{}' -> '{}'", ticket_id_for_resp, result)
        }],
        "ticket_id": ticket_id_for_resp,
        "new_status": result
    }))
}

/// `cascade.append_event` — append a raw event to events.jsonl.
pub(super) async fn handle_append_event(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let event_val = args
        .get("event")
        .cloned()
        .ok_or_else(|| JsonRpcError::invalid_params("'event' is required"))?;

    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    // Inject ts if missing before deserializing (PbdEvent requires ts)
    let mut event_val = event_val;
    if let Value::Object(ref mut map) = event_val {
        map.entry("ts")
            .or_insert_with(|| Value::String(chrono::Utc::now().to_rfc3339()));
    }
    let event: PbdEvent = serde_json::from_value(event_val)
        .map_err(|e| JsonRpcError::invalid_params(format!("invalid event JSON: {e}")))?;

    tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        store.init()?;
        store.append_event(&event)
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("append_event: {e}")))?;
    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": "event appended" }]
    }))
}

/// `cascade.get_sprint` — return sprint YAML for a sprint ID.
///
/// Searches the entire phase/epic/wave tree for the matching sprint.
pub(super) async fn handle_get_sprint(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let sprint_id = args
        .get("sprint_id")
        .and_then(|v| v.as_str())
        .ok_or_else(|| JsonRpcError::invalid_params("'sprint_id' is required"))?
        .to_string();

    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let sprint_json = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        // Walk the INDEX for a sprint entry
        let index = store.read_index()?;
        let entry = index
            .entries
            .iter()
            .find(|e| e.kind == "sprint" && e.id == sprint_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "sprint '{sprint_id}' not found in INDEX.yaml"
                ))
            })?;
        // entry.parent is wave_id; wave.parent is epic_id; epic.parent is phase_id
        let wave_id = entry.parent.as_deref().ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "sprint '{sprint_id}' has no parent wave in INDEX"
            ))
        })?;
        let wave_entry = index
            .entries
            .iter()
            .find(|e| e.kind == "wave" && e.id == wave_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "wave '{wave_id}' not found in INDEX.yaml"
                ))
            })?;
        let epic_id = wave_entry.parent.as_deref().ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "wave '{wave_id}' has no parent epic in INDEX"
            ))
        })?;
        let epic_entry = index
            .entries
            .iter()
            .find(|e| e.kind == "epic" && e.id == epic_id)
            .ok_or_else(|| {
                cascade_types::error::CascadeError::Other(format!(
                    "epic '{epic_id}' not found in INDEX.yaml"
                ))
            })?;
        let phase_id = epic_entry.parent.as_deref().ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "epic '{epic_id}' has no parent phase in INDEX"
            ))
        })?;
        let sprint = store.load_sprint(phase_id, epic_id, wave_id, &sprint_id)?;
        serde_json::to_string(&sprint)
            .map_err(|e| cascade_types::error::CascadeError::Other(format!("json: {e}")))
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("get_sprint: {e}")))?;

    let sprint_val: Value = serde_json::from_str(&sprint_json).unwrap_or(Value::Null);

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": sprint_json }],
        "sprint": sprint_val
    }))
}

/// `cascade.read_phase_status` — compact status summary for a phase.
pub(super) async fn handle_read_phase_status(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let phase_id_filter = args
        .get("phase_id")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let summary = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        let phases = store.list_phases()?;
        let mut result = Vec::new();
        for phase in &phases {
            if let Some(ref filter) = phase_id_filter {
                if &phase.id != filter {
                    continue;
                }
            }
            // Skip archived phases unless explicitly requested
            if phase.status == cascade_core::pbd::schema::PhaseStatus::Archived
                && phase_id_filter.is_none()
            {
                continue;
            }
            let index = store.read_index().unwrap_or_default();
            let tickets: Vec<_> = index
                .entries
                .iter()
                .filter(|e| e.kind == "ticket")
                .collect();
            let mut counts = std::collections::HashMap::new();
            for t in &tickets {
                *counts.entry(t.status.clone()).or_insert(0u32) += 1;
            }
            result.push(serde_json::json!({
                "id": phase.id,
                "title": phase.title,
                "status": phase.status.as_str(),
                "ticket_counts": counts,
                "started_at": phase.started_at,
                "closed_at": phase.closed_at
            }));
        }
        Ok::<Vec<Value>, cascade_types::error::CascadeError>(result)
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("read_phase_status: {e}")))?;

    let text = serde_json::to_string(&summary).unwrap_or_else(|_| "[]".into());
    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "phases": summary
    }))
}

/// `cascade.list_tickets` — list tickets with optional filters.
pub(super) async fn handle_list_tickets(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let status_filter = args
        .get("status")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let sprint_filter = args
        .get("sprint_id")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let phase_filter = args
        .get("phase_id")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let root = resolve_phases_root(pbd_root_from_args(args).as_deref());

    let tickets = tokio::task::spawn_blocking(move || {
        let store = PbdStore::new(&root);
        let index = store.read_index()?;
        let mut result = Vec::new();
        for entry in &index.entries {
            if entry.kind != "ticket" {
                continue;
            }
            if let Some(ref sf) = status_filter {
                if &entry.status != sf {
                    continue;
                }
            }
            if let Some(ref spf) = sprint_filter {
                if entry.parent.as_deref() != Some(spf.as_str()) {
                    continue;
                }
            }
            if let Some(ref pf) = phase_filter {
                // Ticket parent is sprint; sprint parent is wave; wave parent is epic; epic parent is phase.
                // Fast check: look up sprint -> wave -> epic -> phase in index.
                if !ticket_in_phase(&index.entries, &entry.id, pf) {
                    continue;
                }
            }
            result.push(serde_json::json!({
                "id": entry.id,
                "title": entry.title,
                "status": entry.status,
                "sprint": entry.parent,
            }));
        }
        Ok::<Vec<Value>, cascade_types::error::CascadeError>(result)
    })
    .await
    .map_err(|e| JsonRpcError::internal(format!("spawn_blocking: {e}")))?
    .map_err(|e| JsonRpcError::internal(format!("list_tickets: {e}")))?;

    let text = serde_json::to_string(&tickets).unwrap_or_else(|_| "[]".into());
    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "tickets": tickets,
        "count": tickets.len()
    }))
}

/// `cascade.check_routes` — check api-routes.yaml and return per-route ok/fail.
///
/// Reads a minimal YAML file of the form:
/// ```yaml
/// routes:
///   - path: /api/health
///     method: GET
///     expected_status: 200
/// ```
/// and issues HTTP checks. `base_url` overrides the host. In tests, callers
/// can point `routes_file` at a seeded temp file and `base_url` at a mock server.
pub(super) async fn handle_check_routes(
    args: &Value,
) -> std::result::Result<Value, JsonRpcError> {
    let routes_file = args
        .get("routes_file")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let base_url = args
        .get("base_url")
        .and_then(|v| v.as_str())
        .map(|s| s.to_string());
    let timeout_ms = args
        .get("timeout_ms")
        .and_then(|v| v.as_u64())
        .unwrap_or(5000)
        .clamp(100, 30000);

    // Resolve routes file path
    let path = if let Some(p) = routes_file {
        std::path::PathBuf::from(p)
    } else {
        let cwd = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
        cwd.join(".claude").join("docs").join("api-routes.yaml")
    };

    if !path.exists() {
        return Ok(serde_json::json!({
            "content": [{ "type": "text", "text": format!("routes file not found: {}", path.display()) }],
            "routes": [],
            "not_found": true
        }));
    }

    let yaml_text = tokio::fs::read_to_string(&path)
        .await
        .map_err(|e| JsonRpcError::internal(format!("read routes file: {e}")))?;

    // Parse routes — minimal YAML parse without full serde dependency
    let routes = parse_routes_yaml(&yaml_text);

    if routes.is_empty() {
        return Ok(serde_json::json!({
            "content": [{ "type": "text", "text": "no routes found in api-routes.yaml" }],
            "routes": []
        }));
    }

    // Check each route
    let client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_millis(timeout_ms))
        .build()
        .map_err(|e| JsonRpcError::internal(format!("build http client: {e}")))?;

    let mut results = Vec::new();
    for route in &routes {
        let url = if let Some(ref base) = base_url {
            format!("{}{}", base.trim_end_matches('/'), route.path)
        } else {
            route.path.clone()
        };

        let method = route.method.to_uppercase();
        let req = match method.as_str() {
            "POST" => client.post(&url),
            "PUT" => client.put(&url),
            "DELETE" => client.delete(&url),
            "PATCH" => client.patch(&url),
            _ => client.get(&url),
        };

        let (ok, status_code, error_msg) = match req.send().await {
            Ok(resp) => {
                let code = resp.status().as_u16();
                let expected = route.expected_status.unwrap_or(200);
                (code == expected, Some(code), None)
            }
            Err(e) => (false, None, Some(e.to_string())),
        };

        results.push(serde_json::json!({
            "path": route.path,
            "method": route.method,
            "ok": ok,
            "status_code": status_code,
            "expected_status": route.expected_status,
            "error": error_msg
        }));
    }

    let all_ok = results.iter().all(|r| r["ok"].as_bool().unwrap_or(false));
    let text = format!(
        "{}/{} routes ok",
        results
            .iter()
            .filter(|r| r["ok"].as_bool().unwrap_or(false))
            .count(),
        results.len()
    );

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "routes": results,
        "all_ok": all_ok
    }))
}

/// `cascade.scan_inbox` — scan an inbox directory and return file summaries.
pub(super) async fn handle_scan_inbox(args: &Value) -> std::result::Result<Value, JsonRpcError> {
    let inbox_path = if let Some(p) = args.get("inbox_path").and_then(|v| v.as_str()) {
        std::path::PathBuf::from(p)
    } else if let Some(proj) = args.get("project").and_then(|v| v.as_str()) {
        mcp_paths::inbox_dir(proj)
    } else {
        // Auto-discover: CWD/.claude/inbox or CWD/.cascade/inbox
        let cwd = std::env::current_dir().unwrap_or_else(|_| std::path::PathBuf::from("."));
        let claude_inbox = cwd.join(".claude").join("inbox");
        if claude_inbox.is_dir() {
            claude_inbox
        } else {
            cwd.join(".cascade").join("inbox")
        }
    };

    if !inbox_path.exists() {
        return Ok(serde_json::json!({
            "content": [{ "type": "text", "text": "inbox directory not found" }],
            "messages": [],
            "count": 0
        }));
    }

    let mut messages = Vec::new();
    let mut rd = tokio::fs::read_dir(&inbox_path)
        .await
        .map_err(|e| JsonRpcError::internal(format!("read inbox dir: {e}")))?;

    while let Ok(Some(entry)) = rd.next_entry().await {
        let path = entry.path();
        if path.extension().and_then(|e| e.to_str()) != Some("md") {
            continue;
        }
        let filename = path
            .file_name()
            .and_then(|n| n.to_str())
            .unwrap_or("")
            .to_string();
        let meta = tokio::fs::metadata(&path).await.ok();
        let size = meta.map(|m| m.len()).unwrap_or(0);

        // Read first non-empty line as subject
        let subject = if let Ok(content) = tokio::fs::read_to_string(&path).await {
            content
                .lines()
                .find(|l| !l.trim().is_empty())
                .unwrap_or("")
                .trim_start_matches('#')
                .trim()
                .to_string()
        } else {
            String::new()
        };

        messages.push(serde_json::json!({
            "file": filename,
            "subject": subject,
            "size_bytes": size
        }));
    }

    // Sort by filename (chronological for date-prefixed names)
    messages.sort_by(|a, b| {
        a["file"]
            .as_str()
            .unwrap_or("")
            .cmp(b["file"].as_str().unwrap_or(""))
    });

    let count = messages.len();
    let text = format!("{count} message(s) in inbox");

    Ok(serde_json::json!({
        "content": [{ "type": "text", "text": text }],
        "messages": messages,
        "count": count
    }))
}

// ── PBD helpers ───────────────────────────────────────────────────────────────

/// Extract optional phases_root from args.
pub(super) fn pbd_root_from_args(args: &Value) -> Option<std::path::PathBuf> {
    args.get("phases_root")
        .and_then(|v| v.as_str())
        .map(std::path::PathBuf::from)
}

/// Parse a status string into a TicketStatus enum value.
pub(super) fn parse_ticket_status(s: &str) -> Option<TicketStatus> {
    match s {
        "planned" => Some(TicketStatus::Planned),
        "queue" => Some(TicketStatus::Queue),
        "active" => Some(TicketStatus::Active),
        "review" => Some(TicketStatus::Review),
        "blocked" => Some(TicketStatus::Blocked),
        "done" => Some(TicketStatus::Done),
        "archived" => Some(TicketStatus::Archived),
        _ => None,
    }
}

/// Ticket coordinate resolution: given ticket_id and its sprint parent from INDEX,
/// walk the index upward to find (phase_id, epic_id, wave_id, sprint_id).
pub(super) struct TicketCoords {
    pub phase_id: String,
    pub epic_id: String,
    pub wave_id: String,
    pub sprint_id: String,
}

pub(super) fn resolve_ticket_coords(
    store: &PbdStore,
    ticket_id: &str,
    sprint_id_opt: Option<&str>,
) -> std::result::Result<TicketCoords, cascade_types::error::CascadeError> {
    let index = store.read_index()?;
    let sprint_id = sprint_id_opt.ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!(
            "ticket '{ticket_id}' has no sprint parent in INDEX"
        ))
    })?;
    let sprint_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "sprint" && e.id == sprint_id)
        .ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "sprint '{sprint_id}' not found in INDEX"
            ))
        })?;
    let wave_id = sprint_entry.parent.as_deref().ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!(
            "sprint '{sprint_id}' has no wave in INDEX"
        ))
    })?;
    let wave_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "wave" && e.id == wave_id)
        .ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "wave '{wave_id}' not found in INDEX"
            ))
        })?;
    let epic_id = wave_entry.parent.as_deref().ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!("wave '{wave_id}' has no epic in INDEX"))
    })?;
    let epic_entry = index
        .entries
        .iter()
        .find(|e| e.kind == "epic" && e.id == epic_id)
        .ok_or_else(|| {
            cascade_types::error::CascadeError::Other(format!(
                "epic '{epic_id}' not found in INDEX"
            ))
        })?;
    let phase_id = epic_entry.parent.as_deref().ok_or_else(|| {
        cascade_types::error::CascadeError::Other(format!("epic '{epic_id}' has no phase in INDEX"))
    })?;
    Ok(TicketCoords {
        phase_id: phase_id.to_string(),
        epic_id: epic_id.to_string(),
        wave_id: wave_id.to_string(),
        sprint_id: sprint_id.to_string(),
    })
}

/// Check whether ticket_id belongs to phase_id by walking the index parent chain.
pub(super) fn ticket_in_phase(
    entries: &[cascade_core::pbd::schema::IndexEntry],
    ticket_id: &str,
    phase_id: &str,
) -> bool {
    // ticket -> sprint -> wave -> epic -> phase
    let ticket = entries
        .iter()
        .find(|e| e.kind == "ticket" && e.id == ticket_id);
    let sprint_id = match ticket.and_then(|t| t.parent.as_deref()) {
        Some(s) => s,
        None => return false,
    };
    let sprint = entries
        .iter()
        .find(|e| e.kind == "sprint" && e.id == sprint_id);
    let wave_id = match sprint.and_then(|s| s.parent.as_deref()) {
        Some(w) => w,
        None => return false,
    };
    let wave = entries.iter().find(|e| e.kind == "wave" && e.id == wave_id);
    let epic_id = match wave.and_then(|w| w.parent.as_deref()) {
        Some(e) => e,
        None => return false,
    };
    let epic = entries.iter().find(|e| e.kind == "epic" && e.id == epic_id);
    match epic.and_then(|e| e.parent.as_deref()) {
        Some(p) => p == phase_id,
        None => false,
    }
}

/// Minimal YAML-parsing for api-routes.yaml.
///
/// Parses a routes file of the form:
/// ```yaml
/// routes:
///   - path: /api/health
///     method: GET
///     expected_status: 200
/// ```
/// This avoids pulling in a heavy serde_yaml dependency specifically for this function.
pub(super) struct RouteEntry {
    pub path: String,
    pub method: String,
    pub expected_status: Option<u16>,
}

pub(super) fn parse_routes_yaml(yaml: &str) -> Vec<RouteEntry> {
    let mut routes = Vec::new();
    let mut in_routes = false;
    let mut current: Option<(String, String, Option<u16>)> = None;

    for line in yaml.lines() {
        let trimmed = line.trim();
        if trimmed == "routes:" {
            in_routes = true;
            continue;
        }
        if !in_routes {
            continue;
        }
        // New route entry
        if trimmed.starts_with("- path:") || trimmed.starts_with("-path:") {
            // Flush previous
            if let Some((path, method, expected)) = current.take() {
                if !path.is_empty() {
                    routes.push(RouteEntry {
                        path,
                        method,
                        expected_status: expected,
                    });
                }
            }
            let path = trimmed
                .trim_start_matches('-')
                .trim()
                .strip_prefix("path:")
                .unwrap_or("")
                .trim()
                .to_string();
            current = Some((path, "GET".into(), None));
        } else if let Some(ref mut c) = current {
            if trimmed.starts_with("method:") {
                c.1 = trimmed
                    .strip_prefix("method:")
                    .unwrap_or("GET")
                    .trim()
                    .to_string();
            } else if trimmed.starts_with("expected_status:") {
                if let Ok(code) = trimmed
                    .strip_prefix("expected_status:")
                    .unwrap_or("")
                    .trim()
                    .parse::<u16>()
                {
                    c.2 = Some(code);
                }
            }
        }
    }
    // Flush last entry
    if let Some((path, method, expected)) = current {
        if !path.is_empty() {
            routes.push(RouteEntry {
                path,
                method,
                expected_status: expected,
            });
        }
    }
    routes
}
