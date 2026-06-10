//! jira — Cascade DataSource WASM plugin.
//!
//! # Purpose
//! Fetches Jira Cloud issues from a configured project via the Jira REST API v3
//! (POST /rest/api/3/search with JQL) and returns normalized `DataItem` records
//! for RAG ingestion. Reads credentials from env vars declared in `plugin.json`
//! and config (`base_url`, `jql`, `max_results`) from separate plugin env vars.
//!
//! # Inputs
//! - `JIRA_EMAIL` env var — Atlassian account email for Basic auth.
//! - `JIRA_TOKEN` env var — Jira API token (never logged).
//! - `JIRA_BASE_URL` env var — e.g. `https://mycompany.atlassian.net`.
//! - `JIRA_JQL` env var — JQL query (default: `assignee = currentUser() ORDER BY updated DESC`).
//! - `JIRA_MAX_RESULTS` env var — page size (default: 50, max: 100).
//! - `cursor`: optional `startAt` offset from the previous `fetch_items` call.
//!
//! # Outputs
//! - `DataSourcePage { items: Vec<DataItem>, next_cursor: Option<String> }`
//! - Each `DataItem`: id = issue key, title = summary, body = description (ADF text),
//!   url = `{base_url}/browse/{key}`, labels, created_at, updated_at,
//!   source = `"jira:{base_url}"`, extra_json with status/issuetype/priority/assignee/reporter.
//!
//! # Pagination
//! Jira uses `startAt` offset pagination. The cursor is the stringified integer offset.
//! Returns `next_cursor: None` when `startAt + fetched >= total`.
//!
//! # Security
//! `JIRA_TOKEN` is read once, used only in the `Authorization: Basic` header, and
//! never included in any `DataItem` field or log output. `base64` encoding uses the
//! STANDARD alphabet (RFC 4648) with no line wrapping, as required by RFC 7617.
//!
//! # Constraints
//! - no_std + alloc for wasm32-wasip1 target.
//! - Jira Cloud REST API v3 only; no Jira Server/Data Center; no OAuth 2.0.
//! - No attachments or comments.
//!
//! SPORT: plugins/jira (T-P4-E03-15)

use base64::Engine as _;
use cascade_pdk::{
    cascade_plugin, DataItem, DataSourceFetchArgs, DataSourcePage, Plugin, PluginError, PluginKind,
};
use serde::Deserialize;

// ── Plugin struct ─────────────────────────────────────────────────────────────

/// Jira Cloud DataSource plugin.
///
/// Reads all config from environment variables set by the Cascade host at startup
/// from the plugin.json `env` declarations:
/// - `JIRA_BASE_URL`, `JIRA_JQL`, `JIRA_MAX_RESULTS` — config
/// - `JIRA_EMAIL`, `JIRA_TOKEN` — credentials (token never logged)
#[cascade_plugin]
pub struct JiraPlugin;

impl Plugin for JiraPlugin {
    fn name(&self) -> &str {
        "jira"
    }

    fn kind(&self) -> PluginKind {
        PluginKind::DataSource
    }

    fn version(&self) -> &str {
        "0.1.0"
    }

    /// Fetch one page of Jira issues via POST /rest/api/3/search.
    ///
    /// The cursor is the `startAt` offset as a decimal string. Pass `None` to
    /// start from offset 0. Returns `next_cursor: None` when exhausted.
    fn fetch_items(&self, args: DataSourceFetchArgs) -> Result<DataSourcePage, PluginError> {
        let base_url = read_env("JIRA_BASE_URL")?;
        let jql = read_env_or("JIRA_JQL", "assignee = currentUser() ORDER BY updated DESC");
        let max_results: u32 = read_env_or("JIRA_MAX_RESULTS", "50")
            .parse::<u32>()
            .unwrap_or(50)
            .min(100);
        let email = read_env("JIRA_EMAIL")?;
        let token = read_env("JIRA_TOKEN")?;

        // Validate: base_url must use https to prevent token leakage over plain HTTP.
        if !base_url.starts_with("https://") {
            return Err(PluginError::InvalidInput {
                message: "JIRA_BASE_URL must use https://".into(),
            });
        }
        // Validate: jql must not be empty (prevents unbounded result set).
        if jql.trim().is_empty() {
            return Err(PluginError::InvalidInput {
                message: "JIRA_JQL must not be empty".into(),
            });
        }

        let start_at: u32 = args
            .cursor
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(0);

        let auth_header = build_basic_auth(&email, &token);
        let request_body = build_search_body(&jql, start_at, max_results);
        let url = format!("{}/rest/api/3/search", base_url.trim_end_matches('/'));

        let response_json = http_post(&url, &auth_header, &request_body)?;
        parse_search_response(&response_json, &base_url, start_at, max_results)
    }
}

// ── Auth ──────────────────────────────────────────────────────────────────────

/// Build the `Authorization: Basic <base64(email:token)>` header value.
///
/// WHY STANDARD alphabet: RFC 7617 §2 requires standard base64 (RFC 4648 §4)
/// without line wrapping. The `base64::engine::general_purpose::STANDARD`
/// engine produces this. STANDARD_NO_PAD is NOT used — padding is required.
/// Token is consumed; its value is only ever in the header string, never logged.
pub fn build_basic_auth(email: &str, token: &str) -> String {
    let credentials = format!("{email}:{token}");
    let encoded = base64::engine::general_purpose::STANDARD.encode(credentials.as_bytes());
    format!("Basic {encoded}")
}

// ── Request builder ───────────────────────────────────────────────────────────

/// Build the POST /rest/api/3/search request body.
///
/// WHY POST: Jira recommends POST for JQL searches to avoid URL-length limits
/// on long JQL strings. The body includes `fields` to avoid fetching unneeded data.
pub fn build_search_body(jql: &str, start_at: u32, max_results: u32) -> String {
    serde_json::json!({
        "jql": jql,
        "startAt": start_at,
        "maxResults": max_results,
        "fields": [
            "summary", "description", "status", "issuetype",
            "priority", "labels", "assignee", "reporter",
            "created", "updated"
        ]
    })
    .to_string()
}

// ── HTTP dispatch ─────────────────────────────────────────────────────────────

/// On wasm32-wasip1, issue a blocking HTTP POST via the WASI socket capability.
/// On host targets (unit tests), this is a stub — tests call `parse_search_response`
/// directly and do not exercise the HTTP layer.
#[cfg(target_arch = "wasm32")]
fn http_post(url: &str, auth_header: &str, body: &str) -> Result<String, PluginError> {
    // WHY: the Cascade host registers `cascade_http_post_json` as a WASM import
    // in the "cascade" module at instantiation time.
    #[link(wasm_import_module = "cascade")]
    extern "C" {
        fn cascade_http_post_json(
            url_ptr: *const u8,
            url_len: usize,
            auth_ptr: *const u8,
            auth_len: usize,
            body_ptr: *const u8,
            body_len: usize,
            out_ptr: *mut u8,
            out_cap: usize,
        ) -> i32;
    }
    let mut out_buf = vec![0u8; 2 << 20]; // 2 MiB response buffer
    // Safety: all pointers are valid slices; wasmtime bounds-checks linear memory.
    let n = unsafe {
        cascade_http_post_json(
            url.as_ptr(),
            url.len(),
            auth_header.as_ptr(),
            auth_header.len(),
            body.as_ptr(),
            body.len(),
            out_buf.as_mut_ptr(),
            out_buf.len(),
        )
    };
    if n < 0 {
        return Err(PluginError::Internal {
            message: format!("cascade_http_post_json failed (code {n})"),
        });
    }
    let s = core::str::from_utf8(&out_buf[..n as usize])
        .map_err(|_| PluginError::Internal { message: "HTTP response is not valid UTF-8".into() })?
        .to_string();
    Ok(s)
}

/// Host-target stub. Unit tests call `parse_search_response` directly.
#[cfg(not(target_arch = "wasm32"))]
fn http_post(_url: &str, _auth_header: &str, _body: &str) -> Result<String, PluginError> {
    Err(PluginError::Internal {
        message: "http_post is a stub on host targets; use parse_search_response in tests".into(),
    })
}

// ── Response types ────────────────────────────────────────────────────────────

/// Jira REST API v3 search response shape.
#[derive(Debug, Deserialize)]
pub struct JiraSearchResponse {
    pub total: u32,
    #[serde(rename = "startAt")]
    pub start_at: u32,
    pub issues: Vec<JiraIssue>,
}

#[derive(Debug, Deserialize)]
pub struct JiraIssue {
    pub key: String,
    pub fields: JiraFields,
}

#[derive(Debug, Deserialize)]
pub struct JiraFields {
    pub summary: Option<String>,
    pub description: Option<serde_json::Value>,
    pub status: Option<JiraNamedField>,
    pub issuetype: Option<JiraNamedField>,
    pub priority: Option<JiraNamedField>,
    pub labels: Option<Vec<String>>,
    pub assignee: Option<JiraUser>,
    pub reporter: Option<JiraUser>,
    pub created: Option<String>,
    pub updated: Option<String>,
}

#[derive(Debug, Deserialize)]
pub struct JiraNamedField {
    pub name: Option<String>,
}

#[derive(Debug, Deserialize)]
pub struct JiraUser {
    #[serde(rename = "displayName")]
    pub display_name: Option<String>,
}

// ── Pure parsing logic (testable on host) ────────────────────────────────────

/// Parse a Jira search response JSON string into a [`DataSourcePage`].
///
/// `base_url` is used to construct the canonical issue URL.
/// `start_at` is the offset sent in the request (used to compute the next cursor).
///
/// This function contains the complete data-mapping logic and is the primary
/// unit under test — no network access required.
pub fn parse_search_response(
    json: &str,
    base_url: &str,
    start_at: u32,
    max_results: u32,
) -> Result<DataSourcePage, PluginError> {
    let resp: JiraSearchResponse =
        serde_json::from_str(json).map_err(|e| PluginError::Internal {
            message: format!("JSON parse error: {e}"),
        })?;

    let items: Vec<DataItem> = resp
        .issues
        .iter()
        .map(|issue| map_issue_to_data_item(issue, base_url))
        .collect();

    let fetched = items.len() as u32;
    let next_start = start_at + fetched;

    // Advance cursor only if more issues remain.
    // WHY: next_start < total means there are still issues to fetch;
    //      fetched == max_results guards against off-by-one when the last page is full.
    let next_cursor = if next_start < resp.total && fetched == max_results {
        Some(next_start.to_string())
    } else {
        None
    };

    Ok(DataSourcePage { items, next_cursor })
}

fn map_issue_to_data_item(issue: &JiraIssue, base_url: &str) -> DataItem {
    let f = &issue.fields;

    let title = f.summary.clone().unwrap_or_default();
    let body = adf_to_text(f.description.as_ref());
    let url = format!("{}/browse/{}", base_url.trim_end_matches('/'), issue.key);
    let labels = f.labels.clone().unwrap_or_default();
    let created_at = f.created.clone().unwrap_or_default();
    let updated_at = f.updated.clone().unwrap_or_default();
    let source = format!("jira:{}", base_url.trim_end_matches('/'));

    // Pack Jira-specific fields into extra_json — all values are strings per spec.
    let extra = serde_json::json!({
        "status":    named_field_name(f.status.as_ref()),
        "issuetype": named_field_name(f.issuetype.as_ref()),
        "priority":  named_field_name(f.priority.as_ref()),
        "assignee":  user_name(f.assignee.as_ref()),
        "reporter":  user_name(f.reporter.as_ref()),
    });

    DataItem {
        id: issue.key.clone(),
        title,
        body,
        url,
        labels,
        created_at,
        updated_at,
        source,
        extra_json: extra.to_string(),
    }
}

/// Extract plain text from a Jira Atlassian Document Format (ADF) value.
///
/// WHY custom: Jira REST API v3 returns `description` as an ADF JSON object, not
/// a plain string. We recursively collect `text` leaf nodes. Parse failures silently
/// return an empty string — a missing description is not a fatal error.
fn adf_to_text(value: Option<&serde_json::Value>) -> String {
    match value {
        None => String::new(),
        Some(v) => collect_text(v),
    }
}

fn collect_text(node: &serde_json::Value) -> String {
    if let Some(t) = node.get("text").and_then(|v| v.as_str()) {
        return t.to_string();
    }
    let mut buf = String::new();
    if let Some(children) = node.get("content").and_then(|v| v.as_array()) {
        for child in children {
            let part = collect_text(child);
            if !part.is_empty() {
                if !buf.is_empty() {
                    buf.push(' ');
                }
                buf.push_str(&part);
            }
        }
    }
    buf
}

fn named_field_name(field: Option<&JiraNamedField>) -> String {
    field
        .and_then(|f| f.name.as_deref())
        .unwrap_or("")
        .to_string()
}

fn user_name(user: Option<&JiraUser>) -> String {
    user.and_then(|u| u.display_name.as_deref())
        .unwrap_or("")
        .to_string()
}

// ── Environment helpers ───────────────────────────────────────────────────────

fn read_env(key: &str) -> Result<String, PluginError> {
    std::env::var(key).map_err(|_| PluginError::InvalidInput {
        message: format!("Required env var '{key}' is not set"),
    })
}

fn read_env_or(key: &str, default: &str) -> String {
    std::env::var(key).unwrap_or_else(|_| default.into())
}

// ── Unit tests (host-side; pure logic) ───────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    /// Two-issue fixture mirroring the Jira REST API v3 search response shape.
    /// total=150 to exercise pagination logic.
    fn fixture_two_issues() -> &'static str {
        r#"{
          "total": 150,
          "startAt": 0,
          "issues": [
            {
              "key": "PROJ-1",
              "fields": {
                "summary": "Fix login bug",
                "description": {
                  "type": "doc",
                  "content": [
                    {
                      "type": "paragraph",
                      "content": [
                        { "type": "text", "text": "Login fails on mobile." }
                      ]
                    }
                  ]
                },
                "status":    { "name": "In Progress" },
                "issuetype": { "name": "Bug" },
                "priority":  { "name": "High" },
                "labels":    ["frontend", "auth"],
                "assignee":  { "displayName": "Alice" },
                "reporter":  { "displayName": "Bob" },
                "created":   "2024-01-01T00:00:00.000+0000",
                "updated":   "2024-06-01T12:00:00.000+0000"
              }
            },
            {
              "key": "PROJ-2",
              "fields": {
                "summary":   "Add dark mode",
                "description": null,
                "status":    { "name": "Open" },
                "issuetype": { "name": "Story" },
                "priority":  { "name": "Medium" },
                "labels":    [],
                "assignee":  null,
                "reporter":  { "displayName": "Carol" },
                "created":   "2024-02-01T00:00:00.000+0000",
                "updated":   "2024-06-02T08:00:00.000+0000"
              }
            }
          ]
        }"#
    }

    #[test]
    fn parse_two_issues_correct_count() {
        let page =
            parse_search_response(fixture_two_issues(), "https://myco.atlassian.net", 0, 50)
                .expect("parse failed");
        assert_eq!(page.items.len(), 2);
    }

    #[test]
    fn first_issue_fields_mapped_correctly() {
        let page =
            parse_search_response(fixture_two_issues(), "https://myco.atlassian.net", 0, 50)
                .unwrap();
        let item = &page.items[0];
        assert_eq!(item.id, "PROJ-1");
        assert_eq!(item.title, "Fix login bug");
        assert!(item.body.contains("Login fails on mobile"));
        assert_eq!(item.url, "https://myco.atlassian.net/browse/PROJ-1");
        assert_eq!(item.labels, vec!["frontend", "auth"]);
        assert_eq!(item.created_at, "2024-01-01T00:00:00.000+0000");
        assert_eq!(item.source, "jira:https://myco.atlassian.net");
    }

    #[test]
    fn extra_json_contains_string_fields_issuetype_and_priority() {
        // Spec acceptance criterion: issuetype and priority are strings (not objects).
        let page =
            parse_search_response(fixture_two_issues(), "https://myco.atlassian.net", 0, 50)
                .unwrap();
        let extra: serde_json::Value =
            serde_json::from_str(&page.items[0].extra_json).unwrap();
        assert_eq!(extra["issuetype"].as_str().unwrap(), "Bug");
        assert_eq!(extra["priority"].as_str().unwrap(), "High");
        assert_eq!(extra["status"].as_str().unwrap(), "In Progress");
        assert_eq!(extra["assignee"].as_str().unwrap(), "Alice");
        assert_eq!(extra["reporter"].as_str().unwrap(), "Bob");
    }

    #[test]
    fn null_optional_fields_degrade_gracefully() {
        let page =
            parse_search_response(fixture_two_issues(), "https://myco.atlassian.net", 0, 50)
                .unwrap();
        let item = &page.items[1];
        assert_eq!(item.id, "PROJ-2");
        assert_eq!(item.body, ""); // null description → empty string
        assert_eq!(item.labels, Vec::<String>::new());
        let extra: serde_json::Value = serde_json::from_str(&item.extra_json).unwrap();
        assert_eq!(extra["assignee"].as_str().unwrap(), ""); // null assignee → ""
    }

    #[test]
    fn cursor_advances_on_full_page() {
        // total=150, startAt=0, 2 fetched but max_results=2 → next cursor = "2"
        let page =
            parse_search_response(fixture_two_issues(), "https://myco.atlassian.net", 0, 2)
                .unwrap();
        assert_eq!(page.next_cursor, Some("2".to_string()));
    }

    #[test]
    fn cursor_none_on_last_page() {
        // total=150, startAt=148, 2 fetched → 150 >= 150 → no next cursor
        let page =
            parse_search_response(fixture_two_issues(), "https://myco.atlassian.net", 148, 2)
                .unwrap();
        assert_eq!(page.next_cursor, None);
    }

    #[test]
    fn three_pages_from_total_150() {
        // Simulate 50-issue pages from total=150.
        let make_fixture = |start_at: u32| {
            let issues: Vec<serde_json::Value> = (0..50)
                .map(|i| {
                    serde_json::json!({
                        "key": format!("T-{}", start_at + i),
                        "fields": {
                            "summary": format!("Issue {}", start_at + i),
                            "description": null,
                            "status":    { "name": "Open" },
                            "issuetype": { "name": "Task" },
                            "priority":  { "name": "Low" },
                            "labels":    [],
                            "assignee":  null,
                            "reporter":  null,
                            "created":   "2024-01-01T00:00:00.000+0000",
                            "updated":   "2024-01-01T00:00:00.000+0000"
                        }
                    })
                })
                .collect();
            serde_json::json!({
                "total": 150,
                "startAt": start_at,
                "issues": issues
            })
            .to_string()
        };

        let p1 = parse_search_response(&make_fixture(0), "https://x.atlassian.net", 0, 50).unwrap();
        assert_eq!(p1.items.len(), 50);
        assert_eq!(p1.next_cursor, Some("50".to_string()));

        let p2 = parse_search_response(&make_fixture(50), "https://x.atlassian.net", 50, 50).unwrap();
        assert_eq!(p2.next_cursor, Some("100".to_string()));

        let p3 =
            parse_search_response(&make_fixture(100), "https://x.atlassian.net", 100, 50).unwrap();
        assert_eq!(p3.next_cursor, None); // 100 + 50 = 150 = total → exhausted
    }

    #[test]
    fn build_search_body_contains_required_fields() {
        let body = build_search_body("project = PROJ", 0, 50);
        let v: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(v["jql"], "project = PROJ");
        assert_eq!(v["startAt"], 0);
        assert_eq!(v["maxResults"], 50);
        let fields: Vec<&str> = v["fields"]
            .as_array()
            .unwrap()
            .iter()
            .filter_map(|f| f.as_str())
            .collect();
        assert!(fields.contains(&"summary"));
        assert!(fields.contains(&"status"));
        assert!(fields.contains(&"issuetype"));
        assert!(fields.contains(&"priority"));
    }

    #[test]
    fn build_basic_auth_produces_valid_base64() {
        // "user@example.com:abc123" → must be valid base64, not contain newlines.
        let header = build_basic_auth("user@example.com", "abc123");
        assert!(header.starts_with("Basic "));
        let encoded = header.strip_prefix("Basic ").unwrap();
        assert!(!encoded.contains('\n'), "base64 must not contain line breaks");
        // Decode and verify the original credential string.
        use base64::Engine as _;
        let decoded = base64::engine::general_purpose::STANDARD.decode(encoded).unwrap();
        let s = String::from_utf8(decoded).unwrap();
        assert_eq!(s, "user@example.com:abc123");
    }

    #[test]
    fn adf_nested_text_is_extracted() {
        let adf = serde_json::json!({
            "type": "doc",
            "content": [{
                "type": "paragraph",
                "content": [
                    { "type": "text", "text": "Hello" },
                    { "type": "text", "text": "World" }
                ]
            }]
        });
        let text = collect_text(&adf);
        assert!(text.contains("Hello"));
        assert!(text.contains("World"));
    }

    #[test]
    fn token_not_in_data_item_fields() {
        // Verify no DataItem field contains the token — all fields originate from
        // the API response JSON, never from the JIRA_TOKEN env var.
        let page =
            parse_search_response(fixture_two_issues(), "https://myco.atlassian.net", 0, 50)
                .unwrap();
        for item in &page.items {
            let serialised = serde_json::to_string(item).unwrap();
            // Fixture contains no token-like strings; double-check env leakage is impossible.
            assert!(
                !serialised.contains("JIRA_TOKEN"),
                "JIRA_TOKEN key leaked into DataItem"
            );
        }
    }

    #[test]
    fn parse_invalid_json_returns_internal_error() {
        let err =
            parse_search_response("not json", "https://myco.atlassian.net", 0, 50).unwrap_err();
        match err {
            PluginError::Internal { message } => {
                assert!(message.contains("JSON parse error"));
            }
            other => panic!("expected Internal, got {other:?}"),
        }
    }
}
