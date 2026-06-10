//! gitlab — Cascade DataSource WASM plugin.
//!
//! # Purpose
//! Fetches GitLab project issues via the GitLab GraphQL API with cursor-based
//! pagination (`pageInfo.endCursor`) and returns normalized `DataItem` records
//! for RAG ingestion. Reads `GITLAB_TOKEN` from the env capability declared in
//! `plugin.json` and config from separate plugin env vars.
//!
//! # Inputs
//! - `GITLAB_TOKEN` env var — GitLab personal access token (never logged).
//! - `GITLAB_URL` env var — GitLab instance URL (default: `https://gitlab.com`).
//! - `GITLAB_PROJECT_PATH` env var — full project path (e.g. `mygroup/myproject`).
//! - `GITLAB_STATE` env var — issue state: `opened`, `closed`, or `all` (default: `opened`).
//! - `cursor`: optional `endCursor` string from the previous `fetch_items` call.
//!
//! # Outputs
//! - `DataSourcePage { items: Vec<DataItem>, next_cursor: Option<String> }`
//! - Each `DataItem`: id = iid (internal issue ID), title, body = description,
//!   url = webUrl, labels = Vec<String> (not Vec<{title}>), created_at, updated_at,
//!   source = `"gitlab:{project_path}"`, extra_json with state/assignees/milestone.
//!
//! # Pagination
//! Uses GraphQL `pageInfo.endCursor` / `hasNextPage`. The cursor is the opaque
//! `endCursor` string. Returns `next_cursor: None` when `hasNextPage` is false.
//!
//! # Security
//! `GITLAB_TOKEN` is only ever used in the `Authorization: Bearer` header and is
//! never included in any `DataItem` field or log output. `gitlab_url` is validated
//! to use https to prevent token leakage over plain HTTP.
//!
//! # Constraints
//! - no_std + alloc for wasm32-wasip1 target.
//! - GitLab GraphQL API only; no REST API, no group-level queries.
//! - Issues only; no merge requests.
//!
//! SPORT: plugins/gitlab (T-P4-E03-16)

use cascade_pdk::{
    cascade_plugin, DataItem, DataSourceFetchArgs, DataSourcePage, Plugin, PluginError, PluginKind,
};
use serde::Deserialize;

// ── Plugin struct ─────────────────────────────────────────────────────────────

/// GitLab DataSource plugin.
///
/// Reads all config from environment variables set by the Cascade host at startup
/// from the plugin.json `env` declarations:
/// - `GITLAB_URL`, `GITLAB_PROJECT_PATH`, `GITLAB_STATE` — config
/// - `GITLAB_TOKEN` — credential (never logged)
#[cascade_plugin]
pub struct GitLabPlugin;

impl Plugin for GitLabPlugin {
    fn name(&self) -> &str {
        "gitlab"
    }

    fn kind(&self) -> PluginKind {
        PluginKind::DataSource
    }

    fn version(&self) -> &str {
        "0.1.0"
    }

    /// Fetch one page of GitLab issues via the GraphQL API.
    ///
    /// The cursor is the opaque `endCursor` string from `pageInfo`. Pass `None`
    /// to start from the first page. Returns `next_cursor: None` when exhausted.
    fn fetch_items(&self, args: DataSourceFetchArgs) -> Result<DataSourcePage, PluginError> {
        let gitlab_url = read_env_or("GITLAB_URL", "https://gitlab.com");
        let project_path = read_env("GITLAB_PROJECT_PATH")?;
        let state = read_env_or("GITLAB_STATE", "opened");
        let token = read_env("GITLAB_TOKEN")?;

        // Validate: gitlab_url must use https to prevent token leakage over plain HTTP.
        if !gitlab_url.starts_with("https://") {
            return Err(PluginError::InvalidInput {
                message: "GITLAB_URL must use https://".into(),
            });
        }
        // Validate: project_path must not be empty.
        if project_path.trim().is_empty() {
            return Err(PluginError::InvalidInput {
                message: "GITLAB_PROJECT_PATH must not be empty".into(),
            });
        }

        let query_body = build_graphql_query(&project_path, &state, args.cursor.as_deref());
        let endpoint = format!("{}/api/graphql", gitlab_url.trim_end_matches('/'));
        let auth_header = format!("Bearer {token}");

        let response_json = graphql_post(&endpoint, &auth_header, &query_body)?;
        parse_graphql_response(&response_json, &project_path)
    }
}

// ── GraphQL query builder ─────────────────────────────────────────────────────

/// Build the GraphQL POST body for fetching project issues.
///
/// WHY static query + variables JSON: composing the query body as a JSON object
/// with a `query` string and `variables` object is the canonical GraphQL-over-HTTP
/// pattern. Variables are used for all dynamic values to prevent injection — the
/// `$path`, `$state`, and `$cursor` parameters are GraphQL variable slots, not
/// string-interpolated into the query body.
pub fn build_graphql_query(project_path: &str, state: &str, cursor: Option<&str>) -> String {
    // WHY GraphQL variables object: the `after` and `state` inputs are passed as
    // typed variables, never interpolated into the query string. This ensures the
    // GraphQL server validates them and prevents injection through crafted inputs.
    let variables = serde_json::json!({
        "path":   project_path,
        "state":  state,
        "cursor": cursor,
    });

    serde_json::json!({
        "query": GITLAB_ISSUES_QUERY,
        "variables": variables,
    })
    .to_string()
}

/// The GitLab GraphQL query for project issues.
///
/// WHY `iid` not `id`: `iid` is the human-visible issue number within the project
/// (matching the URL); `id` is a global opaque GraphQL node ID. The spec requires
/// `iid` as the DataItem `id` field.
const GITLAB_ISSUES_QUERY: &str = r#"
query($path: ID!, $state: IssuableState, $cursor: String) {
  project(fullPath: $path) {
    issues(first: 50, after: $cursor, state: $state) {
      nodes {
        iid
        title
        description
        webUrl
        state
        labels { nodes { title } }
        assignees { nodes { name } }
        milestone { title }
        createdAt
        updatedAt
      }
      pageInfo {
        hasNextPage
        endCursor
      }
    }
  }
}
"#;

// ── HTTP dispatch ─────────────────────────────────────────────────────────────

/// On wasm32-wasip1, POST the GraphQL query body via the WASI socket capability.
/// On host targets (unit tests), this is a stub — tests call `parse_graphql_response`
/// directly and do not exercise the HTTP layer.
#[cfg(target_arch = "wasm32")]
fn graphql_post(endpoint: &str, auth_header: &str, body: &str) -> Result<String, PluginError> {
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

/// Host-target stub. Unit tests call `parse_graphql_response` directly.
#[cfg(not(target_arch = "wasm32"))]
fn graphql_post(_endpoint: &str, _auth_header: &str, _body: &str) -> Result<String, PluginError> {
    Err(PluginError::Internal {
        message: "graphql_post is a stub on host targets; use parse_graphql_response in tests"
            .into(),
    })
}

// ── Response types ────────────────────────────────────────────────────────────

/// Top-level GraphQL response envelope.
#[derive(Debug, Deserialize)]
pub struct GitLabGraphQLResponse {
    pub data: Option<GitLabData>,
}

#[derive(Debug, Deserialize)]
pub struct GitLabData {
    pub project: Option<GitLabProject>,
}

#[derive(Debug, Deserialize)]
pub struct GitLabProject {
    pub issues: GitLabIssueConnection,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GitLabIssueConnection {
    pub nodes: Vec<GitLabIssue>,
    pub page_info: GitLabPageInfo,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GitLabIssue {
    pub iid: String,
    pub title: String,
    pub description: Option<String>,
    pub web_url: String,
    pub state: String,
    pub labels: GitLabLabelConnection,
    pub assignees: GitLabAssigneeConnection,
    pub milestone: Option<GitLabMilestone>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Deserialize)]
pub struct GitLabLabelConnection {
    pub nodes: Vec<GitLabLabel>,
}

#[derive(Debug, Deserialize)]
pub struct GitLabLabel {
    pub title: String,
}

#[derive(Debug, Deserialize)]
pub struct GitLabAssigneeConnection {
    pub nodes: Vec<GitLabAssignee>,
}

#[derive(Debug, Deserialize)]
pub struct GitLabAssignee {
    pub name: String,
}

#[derive(Debug, Deserialize)]
pub struct GitLabMilestone {
    pub title: String,
}

#[derive(Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct GitLabPageInfo {
    pub has_next_page: bool,
    pub end_cursor: Option<String>,
}

// ── Pure parsing logic (testable on host) ────────────────────────────────────

/// Parse a GitLab GraphQL response JSON string into a [`DataSourcePage`].
///
/// `project_path` is used to construct the `source` identifier field.
///
/// This function contains all data-mapping logic and is the primary unit under
/// test — no network access required.
pub fn parse_graphql_response(
    json: &str,
    project_path: &str,
) -> Result<DataSourcePage, PluginError> {
    let resp: GitLabGraphQLResponse =
        serde_json::from_str(json).map_err(|e| PluginError::Internal {
            message: format!("JSON parse error: {e}"),
        })?;

    let issues_conn = resp
        .data
        .and_then(|d| d.project)
        .map(|p| p.issues)
        .ok_or_else(|| PluginError::Internal {
            message: "GraphQL response missing data.project.issues".into(),
        })?;

    let source = format!("gitlab:{project_path}");
    let items: Vec<DataItem> = issues_conn
        .nodes
        .iter()
        .map(|issue| map_issue_to_data_item(issue, &source))
        .collect();

    let next_cursor = if issues_conn.page_info.has_next_page {
        issues_conn.page_info.end_cursor
    } else {
        None
    };

    Ok(DataSourcePage { items, next_cursor })
}

fn map_issue_to_data_item(issue: &GitLabIssue, source: &str) -> DataItem {
    // labels: Vec<String> not Vec<{title: String}> — per spec acceptance criterion.
    let labels: Vec<String> = issue.labels.nodes.iter().map(|l| l.title.clone()).collect();

    let assignees: Vec<String> = issue
        .assignees
        .nodes
        .iter()
        .map(|a| a.name.clone())
        .collect();
    let milestone = issue
        .milestone
        .as_ref()
        .map(|m| m.title.clone())
        .unwrap_or_default();

    // Pack GitLab-specific fields into extra_json to keep DataItem shape stable.
    let extra = serde_json::json!({
        "state":     issue.state,
        "assignees": assignees,
        "milestone": milestone,
    });

    DataItem {
        id: issue.iid.clone(),
        title: issue.title.clone(),
        body: issue.description.clone().unwrap_or_default(),
        url: issue.web_url.clone(),
        labels,
        created_at: issue.created_at.clone(),
        updated_at: issue.updated_at.clone(),
        source: source.to_string(),
        extra_json: extra.to_string(),
    }
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

    /// Two-issue fixture with `hasNextPage: true` to test cursor advancement.
    fn fixture_open_issues() -> &'static str {
        r#"{
          "data": {
            "project": {
              "issues": {
                "nodes": [
                  {
                    "iid": "42",
                    "title": "Fix dark mode",
                    "description": "The dark mode toggle is broken on Firefox.",
                    "webUrl": "https://gitlab.com/mygroup/myproject/-/issues/42",
                    "state": "opened",
                    "labels": {
                      "nodes": [
                        { "title": "bug" },
                        { "title": "ux" }
                      ]
                    },
                    "assignees": {
                      "nodes": [
                        { "name": "Alice" }
                      ]
                    },
                    "milestone": { "title": "v2.0" },
                    "createdAt": "2024-01-15T10:00:00Z",
                    "updatedAt": "2024-06-01T12:30:00Z"
                  },
                  {
                    "iid": "43",
                    "title": "Add keyboard shortcuts",
                    "description": null,
                    "webUrl": "https://gitlab.com/mygroup/myproject/-/issues/43",
                    "state": "opened",
                    "labels": { "nodes": [] },
                    "assignees": { "nodes": [] },
                    "milestone": null,
                    "createdAt": "2024-02-01T00:00:00Z",
                    "updatedAt": "2024-06-02T08:00:00Z"
                  }
                ],
                "pageInfo": {
                  "hasNextPage": true,
                  "endCursor": "cursor_abc123"
                }
              }
            }
          }
        }"#
    }

    /// Fixture for the final page (hasNextPage: false).
    fn fixture_last_page() -> &'static str {
        r#"{
          "data": {
            "project": {
              "issues": {
                "nodes": [
                  {
                    "iid": "1",
                    "title": "Initial setup",
                    "description": "Project bootstrap.",
                    "webUrl": "https://gitlab.com/mygroup/myproject/-/issues/1",
                    "state": "closed",
                    "labels": { "nodes": [{ "title": "done" }] },
                    "assignees": { "nodes": [] },
                    "milestone": null,
                    "createdAt": "2023-01-01T00:00:00Z",
                    "updatedAt": "2023-06-01T00:00:00Z"
                  }
                ],
                "pageInfo": {
                  "hasNextPage": false,
                  "endCursor": null
                }
              }
            }
          }
        }"#
    }

    #[test]
    fn parse_two_issues_correct_count() {
        let page = parse_graphql_response(fixture_open_issues(), "mygroup/myproject").unwrap();
        assert_eq!(page.items.len(), 2);
    }

    #[test]
    fn first_issue_fields_mapped_correctly() {
        let page = parse_graphql_response(fixture_open_issues(), "mygroup/myproject").unwrap();
        let item = &page.items[0];
        assert_eq!(item.id, "42");
        assert_eq!(item.title, "Fix dark mode");
        assert_eq!(item.body, "The dark mode toggle is broken on Firefox.");
        assert_eq!(item.url, "https://gitlab.com/mygroup/myproject/-/issues/42");
        assert_eq!(item.created_at, "2024-01-15T10:00:00Z");
        assert_eq!(item.source, "gitlab:mygroup/myproject");
    }

    #[test]
    fn labels_are_vec_of_strings_not_objects() {
        // Spec acceptance criterion: labels field is Vec<String>, not Vec<{title: String}>.
        let page = parse_graphql_response(fixture_open_issues(), "mygroup/myproject").unwrap();
        let item = &page.items[0];
        assert_eq!(item.labels, vec!["bug", "ux"]);
        // Verify serialised form has no nested objects.
        let serialised = serde_json::to_string(item).unwrap();
        let v: serde_json::Value = serde_json::from_str(&serialised).unwrap();
        let labels = v["labels"].as_array().unwrap();
        assert!(
            labels.iter().all(|l| l.is_string()),
            "labels must be plain strings"
        );
    }

    #[test]
    fn null_optional_fields_degrade_gracefully() {
        let page = parse_graphql_response(fixture_open_issues(), "mygroup/myproject").unwrap();
        let item = &page.items[1]; // iid=43 has null description, empty labels/assignees, null milestone
        assert_eq!(item.body, ""); // null description → empty string
        assert_eq!(item.labels, Vec::<String>::new());
        let extra: serde_json::Value = serde_json::from_str(&item.extra_json).unwrap();
        let assignees = extra["assignees"].as_array().unwrap();
        assert!(assignees.is_empty());
        assert_eq!(extra["milestone"].as_str().unwrap(), "");
    }

    #[test]
    fn cursor_advances_when_has_next_page() {
        let page = parse_graphql_response(fixture_open_issues(), "mygroup/myproject").unwrap();
        assert_eq!(page.next_cursor, Some("cursor_abc123".to_string()));
    }

    #[test]
    fn cursor_none_on_last_page() {
        let page = parse_graphql_response(fixture_last_page(), "mygroup/myproject").unwrap();
        assert_eq!(page.next_cursor, None);
    }

    #[test]
    fn state_filter_respected_in_last_page_fixture() {
        // Verify the closed-state fixture maps correctly (state in extra_json).
        let page = parse_graphql_response(fixture_last_page(), "mygroup/myproject").unwrap();
        let extra: serde_json::Value = serde_json::from_str(&page.items[0].extra_json).unwrap();
        assert_eq!(extra["state"].as_str().unwrap(), "closed");
    }

    #[test]
    fn build_graphql_query_uses_variables_not_interpolation() {
        // Variables must be in a separate object — not string-interpolated into the query.
        let body_str = build_graphql_query("mygroup/myproject", "opened", Some("abc123"));
        let v: serde_json::Value = serde_json::from_str(&body_str).unwrap();
        // Must have both `query` (string) and `variables` (object) keys.
        assert!(v["query"].is_string(), "query must be a string");
        assert!(v["variables"].is_object(), "variables must be an object");
        // Variables must contain the path, state, and cursor as separate fields.
        assert_eq!(v["variables"]["path"], "mygroup/myproject");
        assert_eq!(v["variables"]["state"], "opened");
        assert_eq!(v["variables"]["cursor"], "abc123");
        // The query string itself must NOT contain the path literal — injection check.
        assert!(
            !v["query"].as_str().unwrap().contains("mygroup/myproject"),
            "project path must not be interpolated into query string"
        );
    }

    #[test]
    fn build_graphql_query_null_cursor_on_first_page() {
        let body_str = build_graphql_query("mygroup/myproject", "opened", None);
        let v: serde_json::Value = serde_json::from_str(&body_str).unwrap();
        assert!(v["variables"]["cursor"].is_null());
    }

    #[test]
    fn extra_json_contains_assignees_array() {
        let page = parse_graphql_response(fixture_open_issues(), "mygroup/myproject").unwrap();
        let extra: serde_json::Value = serde_json::from_str(&page.items[0].extra_json).unwrap();
        let assignees = extra["assignees"].as_array().unwrap();
        assert_eq!(assignees[0].as_str().unwrap(), "Alice");
        assert_eq!(extra["milestone"].as_str().unwrap(), "v2.0");
    }

    #[test]
    fn token_not_in_data_item_fields() {
        // Verify no DataItem field can contain the token — all fields come from the
        // API response JSON, never from the GITLAB_TOKEN env var.
        let page = parse_graphql_response(fixture_open_issues(), "mygroup/myproject").unwrap();
        for item in &page.items {
            let serialised = serde_json::to_string(item).unwrap();
            assert!(
                !serialised.contains("GITLAB_TOKEN"),
                "GITLAB_TOKEN key leaked into DataItem"
            );
        }
    }

    #[test]
    fn parse_invalid_json_returns_internal_error() {
        let err = parse_graphql_response("not json", "mygroup/myproject").unwrap_err();
        match err {
            PluginError::Internal { message } => {
                assert!(message.contains("JSON parse error"));
            }
            other => panic!("expected Internal, got {other:?}"),
        }
    }

    #[test]
    fn parse_missing_project_data_returns_internal_error() {
        let json = r#"{"data": {"project": null}}"#;
        let err = parse_graphql_response(json, "mygroup/myproject").unwrap_err();
        match err {
            PluginError::Internal { message } => {
                assert!(message.contains("data.project.issues"));
            }
            other => panic!("expected Internal, got {other:?}"),
        }
    }
}
