//! linear — Cascade DataSource WASM plugin.
//!
//! # Purpose
//! Fetches Linear issues (with state, priority, assignee, labels) via the
//! Linear GraphQL API and returns normalized `DataItem` records for RAG
//! ingestion. Reads `LINEAR_API_KEY` from the env capability and `team_id` /
//! `state_filter` from plugin config.
//!
//! # Inputs
//! - `LINEAR_API_KEY` env var (Bearer auth for `https://api.linear.app/graphql`).
//! - `LINEAR_PLUGIN_TEAM_ID` (optional): filter by team UUID.
//! - `LINEAR_PLUGIN_STATE_FILTER` (optional): comma-separated state names to
//!   include (e.g. `"In Progress,Todo"`).
//! - `cursor`: Linear `pageInfo.endCursor` from the previous `fetch_items` call.
//!
//! # Outputs
//! - `DataSourcePage { items: Vec<DataItem>, next_cursor: Option<String> }`
//! - Each `DataItem` has: id, title, body (description), url, labels, created_at,
//!   updated_at, source = `"linear:{team_id}"`, extra_json containing
//!   `{"priority":"High","state":"In Progress","assignee":"Alice"}`.
//!
//! # Pagination
//! Linear uses cursor-based pagination (`pageInfo.hasNextPage` + `endCursor`).
//! The cursor is Linear's opaque `endCursor` string. When `hasNextPage` is
//! false, `next_cursor` is `None`.
//!
//! # Priority mapping
//! Linear returns priority as an integer (0–4). This plugin converts it to a
//! human-readable label per the DataItem spec requirement:
//! - 0 → "No Priority"
//! - 1 → "Urgent"
//! - 2 → "High"
//! - 3 → "Medium"
//! - 4 → "Low"
//!
//! # Security
//! `LINEAR_API_KEY` is read once, used in the Authorization header, and never
//! included in any `DataItem` field or log output.
//!
//! # Constraints
//! - no_std + alloc for wasm32-wasip1 target.
//! - No webhooks, comments, attachments, or multiple workspace support.
//! - Single batched GraphQL query — no N+1 patterns.
//!
//! SPORT: linear plugin (T-P4-E03-14)

use cascade_pdk::{
    cascade_plugin, DataItem, DataSourceFetchArgs, DataSourcePage, Plugin, PluginError, PluginKind,
};
use serde::{Deserialize, Serialize};

// ── Priority mapping ──────────────────────────────────────────────────────────

/// Convert a Linear priority integer to a human-readable label.
///
/// Linear priority enum: 0 = No Priority, 1 = Urgent, 2 = High, 3 = Medium, 4 = Low.
pub fn priority_label(priority: u32) -> &'static str {
    match priority {
        1 => "Urgent",
        2 => "High",
        3 => "Medium",
        4 => "Low",
        _ => "No Priority",
    }
}

// ── GraphQL response shapes ───────────────────────────────────────────────────

/// Top-level GraphQL response envelope.
#[derive(Debug, Deserialize)]
pub struct GraphQlResponse {
    pub data: Option<GraphQlData>,
    pub errors: Option<Vec<GraphQlError>>,
}

#[derive(Debug, Deserialize)]
pub struct GraphQlData {
    pub issues: IssueConnection,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct IssueConnection {
    pub nodes: Vec<LinearIssue>,
    pub page_info: PageInfo,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PageInfo {
    pub has_next_page: bool,
    pub end_cursor: Option<String>,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct LinearIssue {
    pub id: String,
    pub title: String,
    pub description: Option<String>,
    pub url: String,
    /// Priority as Linear integer (0–4).
    pub priority: u32,
    pub state: Option<IssueState>,
    pub labels: Option<LabelConnection>,
    pub assignee: Option<User>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Deserialize)]
pub struct IssueState {
    pub name: String,
}

#[derive(Debug, Deserialize)]
pub struct LabelConnection {
    pub nodes: Vec<IssueLabel>,
}

#[derive(Debug, Deserialize)]
pub struct IssueLabel {
    pub name: String,
}

#[derive(Debug, Deserialize)]
pub struct User {
    pub name: String,
}

#[derive(Debug, Deserialize)]
pub struct GraphQlError {
    pub message: String,
}

// ── Extra metadata shape ──────────────────────────────────────────────────────

/// Serialised into `DataItem.extra_json` for Linear-specific fields.
#[derive(Debug, Serialize)]
struct LinearExtra<'a> {
    priority: &'a str,
    state: &'a str,
    assignee: &'a str,
}

// ── Plugin struct ─────────────────────────────────────────────────────────────

/// Linear DataSource plugin.
#[cascade_plugin]
pub struct LinearPlugin;

impl Plugin for LinearPlugin {
    fn name(&self) -> &str {
        "linear"
    }

    fn kind(&self) -> PluginKind {
        PluginKind::DataSource
    }

    fn version(&self) -> &str {
        "0.1.0"
    }

    /// Fetch one page of Linear issues via GraphQL.
    ///
    /// The cursor is Linear's `pageInfo.endCursor` from the previous call.
    /// Pass `None` to start from the beginning.
    fn fetch_items(&self, args: DataSourceFetchArgs) -> Result<DataSourcePage, PluginError> {
        let api_key = read_env("LINEAR_API_KEY")?;
        let team_id = read_env_opt("LINEAR_PLUGIN_TEAM_ID");
        let state_filter_raw = read_env_opt("LINEAR_PLUGIN_STATE_FILTER");

        // Build GraphQL query with optional team + cursor filters.
        let query = build_graphql_query(team_id.as_deref(), args.cursor.as_deref());
        let response_body = post_graphql_impl(&api_key, &query)?;
        let (items, next_cursor) = parse_graphql_response(&response_body, team_id.as_deref())?;

        // Optional client-side state filter (post-parse, not via GraphQL filter to
        // keep the query simple and avoid N+1).
        let items = if let Some(ref filter) = state_filter_raw {
            let states: Vec<&str> = filter.split(',').map(|s| s.trim()).collect();
            items
                .into_iter()
                .filter(|item| {
                    // extra_json contains state; parse it to check.
                    let extra: serde_json::Value =
                        serde_json::from_str(&item.extra_json).unwrap_or_default();
                    let state = extra["state"].as_str().unwrap_or("");
                    states.contains(&state)
                })
                .collect()
        } else {
            items
        };

        Ok(DataSourcePage { items, next_cursor })
    }
}

// ── GraphQL query builder ─────────────────────────────────────────────────────

/// Build the Linear GraphQL query string.
///
/// Uses a single batched query — no N+1 patterns. The team and cursor
/// filters are injected as inline literals (not variables) because the WASM
/// environment has no prepared-statement infrastructure. The values are
/// sanitized to disallow control characters and quote injection.
pub fn build_graphql_query(team_id: Option<&str>, cursor: Option<&str>) -> String {
    let team_filter = team_id
        .map(|id| {
            format!(
                r#"filter: {{team: {{id: {{eq: "{}"}}}}}}, "#,
                sanitize_str(id)
            )
        })
        .unwrap_or_default();

    let after_arg = cursor
        .map(|c| format!(r#", after: "{}""#, sanitize_str(c)))
        .unwrap_or_default();

    format!(
        r#"{{
  issues(first: 50{after_arg}, {team_filter}orderBy: updatedAt) {{
    nodes {{
      id
      title
      description
      url
      priority
      state {{ name }}
      labels {{ nodes {{ name }} }}
      assignee {{ name }}
      createdAt
      updatedAt
    }}
    pageInfo {{
      hasNextPage
      endCursor
    }}
  }}
}}"#
    )
}

/// Strip characters that could break GraphQL string literals injected inline.
fn sanitize_str(s: &str) -> String {
    s.chars()
        .filter(|&c| c != '"' && c != '\\' && c != '\n' && c != '\r')
        .collect()
}

// ── HTTP dispatch ─────────────────────────────────────────────────────────────

#[cfg(target_arch = "wasm32")]
fn post_graphql_impl(api_key: &str, query: &str) -> Result<String, PluginError> {
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

    let endpoint = "https://api.linear.app/graphql";
    let auth_header = format!("Bearer {api_key}");
    let body = format!(
        r#"{{"query":{}}}"#,
        serde_json::to_string(query).unwrap_or_default()
    );
    let mut out_buf = vec![0u8; 2 << 20]; // 2 MiB
                                          // Safety: all slices are valid; wasmtime bounds-checks linear memory accesses.
    let n = unsafe {
        cascade_http_post_json(
            endpoint.as_ptr(),
            endpoint.len(),
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
        .map_err(|_| PluginError::Internal {
            message: "HTTP response is not valid UTF-8".into(),
        })?
        .to_string();
    Ok(s)
}

#[cfg(not(target_arch = "wasm32"))]
fn post_graphql_impl(_api_key: &str, _query: &str) -> Result<String, PluginError> {
    Err(PluginError::Internal {
        message: "post_graphql_impl is a stub on host targets; use parse_graphql_response in tests"
            .into(),
    })
}

// ── Pure parsing logic (testable on host) ─────────────────────────────────────

/// Parse a Linear GraphQL JSON response body into (Vec<DataItem>, next_cursor).
///
/// This function contains the entire data-mapping logic and is the primary
/// unit under test — no network access required.
pub fn parse_graphql_response(
    json_body: &str,
    team_id: Option<&str>,
) -> Result<(Vec<DataItem>, Option<String>), PluginError> {
    let resp: GraphQlResponse =
        serde_json::from_str(json_body).map_err(|e| PluginError::Internal {
            message: format!("JSON parse error: {e}"),
        })?;

    // Surface GraphQL errors as PluginError::Internal.
    if let Some(errors) = resp.errors {
        if !errors.is_empty() {
            let msg = errors
                .iter()
                .map(|e| e.message.as_str())
                .collect::<Vec<_>>()
                .join("; ");
            return Err(PluginError::Internal {
                message: format!("GraphQL errors: {msg}"),
            });
        }
    }

    let data = resp.data.ok_or_else(|| PluginError::Internal {
        message: "GraphQL response missing `data` field".into(),
    })?;

    let source_team = team_id.unwrap_or("default");
    let source = format!("linear:{source_team}");

    let next_cursor = if data.issues.page_info.has_next_page {
        data.issues.page_info.end_cursor
    } else {
        None
    };

    let items: Vec<DataItem> = data
        .issues
        .nodes
        .into_iter()
        .map(|issue| {
            let priority_str = priority_label(issue.priority);
            let state_name = issue
                .state
                .as_ref()
                .map(|s| s.name.as_str())
                .unwrap_or("Unknown");
            let assignee_name = issue
                .assignee
                .as_ref()
                .map(|u| u.name.as_str())
                .unwrap_or("");
            let labels: Vec<String> = issue
                .labels
                .as_ref()
                .map(|lc| lc.nodes.iter().map(|l| l.name.clone()).collect())
                .unwrap_or_default();

            // Build extra_json with Linear-specific fields.
            let extra = LinearExtra {
                priority: priority_str,
                state: state_name,
                assignee: assignee_name,
            };
            let extra_json = serde_json::to_string(&extra).unwrap_or_else(|_| "{}".into());

            DataItem {
                id: issue.id.clone(),
                title: issue.title.clone(),
                body: issue.description.unwrap_or_default(),
                url: issue.url.clone(),
                labels,
                created_at: issue.created_at.clone(),
                updated_at: issue.updated_at.clone(),
                source: source.clone(),
                extra_json,
            }
        })
        .collect();

    Ok((items, next_cursor))
}

// ── Environment helpers ───────────────────────────────────────────────────────

fn read_env(key: &str) -> Result<String, PluginError> {
    std::env::var(key).map_err(|_| PluginError::InvalidInput {
        message: format!("Required env var '{key}' is not set"),
    })
}

fn read_env_opt(key: &str) -> Option<String> {
    std::env::var(key).ok()
}

// ── Unit tests (host-side; pure logic) ───────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    const FIXTURE_TWO_ISSUES: &str = include_str!("../tests/fixtures/issues_page1.json");
    const FIXTURE_HAS_NEXT_PAGE: &str = include_str!("../tests/fixtures/issues_has_next_page.json");
    const FIXTURE_GRAPHQL_ERROR: &str = include_str!("../tests/fixtures/graphql_error.json");

    // ── priority_label ────────────────────────────────────────────────────────

    #[test]
    fn priority_label_maps_all_values() {
        assert_eq!(priority_label(0), "No Priority");
        assert_eq!(priority_label(1), "Urgent");
        assert_eq!(priority_label(2), "High");
        assert_eq!(priority_label(3), "Medium");
        assert_eq!(priority_label(4), "Low");
        assert_eq!(priority_label(99), "No Priority"); // unknown → default
    }

    // ── parse_graphql_response ────────────────────────────────────────────────

    #[test]
    fn parse_two_issues_returns_correct_count() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        assert_eq!(items.len(), 2);
    }

    #[test]
    fn parse_issue_fields_are_mapped_correctly() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        let first = &items[0];

        assert_eq!(first.id, "LIN-001");
        assert_eq!(first.title, "Implement dark mode");
        assert_eq!(first.url, "https://linear.app/team/issue/LIN-001");
        assert_eq!(first.created_at, "2024-03-01T08:00:00.000Z");
        assert_eq!(first.updated_at, "2024-03-02T10:00:00.000Z");
        assert_eq!(first.body, "Add dark mode support to the dashboard.");
        assert_eq!(first.source, "linear:default");
    }

    #[test]
    fn parse_priority_field_is_human_readable_string() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        let first = &items[0]; // priority: 2 in fixture
        let extra: serde_json::Value = serde_json::from_str(&first.extra_json).unwrap();
        // Priority 2 → "High"
        assert_eq!(extra["priority"].as_str().unwrap(), "High");
    }

    #[test]
    fn parse_state_name_in_extra_json() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        let first = &items[0];
        let extra: serde_json::Value = serde_json::from_str(&first.extra_json).unwrap();
        assert_eq!(extra["state"].as_str().unwrap(), "In Progress");
    }

    #[test]
    fn parse_assignee_in_extra_json() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        let first = &items[0];
        let extra: serde_json::Value = serde_json::from_str(&first.extra_json).unwrap();
        assert_eq!(extra["assignee"].as_str().unwrap(), "Alice Smith");
    }

    #[test]
    fn parse_labels_list() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        let first = &items[0];
        assert_eq!(first.labels, vec!["Frontend", "UX"]);
    }

    #[test]
    fn cursor_advances_when_has_next_page_true() {
        let (_, next_cursor) = parse_graphql_response(FIXTURE_HAS_NEXT_PAGE, None).unwrap();
        assert_eq!(next_cursor, Some("cursor-abc-xyz".to_string()));
    }

    #[test]
    fn cursor_none_when_has_next_page_false() {
        let (_, next_cursor) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        assert!(next_cursor.is_none());
    }

    #[test]
    fn graphql_errors_surface_as_internal_error() {
        let err = parse_graphql_response(FIXTURE_GRAPHQL_ERROR, None).unwrap_err();
        match err {
            PluginError::Internal { message } => {
                assert!(message.contains("GraphQL errors"));
                assert!(message.contains("Unauthorized"));
            }
            other => panic!("expected Internal error, got {other:?}"),
        }
    }

    #[test]
    fn team_id_appears_in_source_field() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, Some("ENG")).unwrap();
        assert_eq!(items[0].source, "linear:ENG");
    }

    #[test]
    fn api_key_not_in_data_item_fields() {
        let (items, _) = parse_graphql_response(FIXTURE_TWO_ISSUES, None).unwrap();
        let serialised = serde_json::to_string(&items[0]).unwrap();
        // Fixtures don't contain API keys; verify no env-like leak.
        assert!(!serialised.contains("lin_api_"), "api key pattern leaked");
    }

    // ── build_graphql_query ───────────────────────────────────────────────────

    #[test]
    fn query_with_no_team_and_no_cursor() {
        let q = build_graphql_query(None, None);
        assert!(q.contains("first: 50"));
        assert!(!q.contains("after:"));
        assert!(!q.contains("filter:"));
    }

    #[test]
    fn query_with_team_includes_filter() {
        let q = build_graphql_query(Some("TEAM-ID-123"), None);
        assert!(q.contains("TEAM-ID-123"));
        assert!(q.contains("filter:"));
    }

    #[test]
    fn query_with_cursor_includes_after() {
        let q = build_graphql_query(None, Some("cursor-xyz"));
        assert!(q.contains("after:"));
        assert!(q.contains("cursor-xyz"));
    }

    #[test]
    fn sanitize_str_strips_injection_chars() {
        // A malicious cursor containing a quote should be stripped.
        let q = build_graphql_query(None, Some(r#"abc"def"#));
        // The injected quote must not appear in the query string.
        let after_pos = q.find("after:").unwrap();
        let after_fragment = &q[after_pos..after_pos + 60];
        assert!(
            !after_fragment.contains('"') || after_fragment.matches('"').count() == 2,
            "injection not sanitized: {after_fragment}"
        );
    }
}
