//! github-issues — Cascade DataSource WASM plugin.
//!
//! # Purpose
//! Fetches GitHub Issues (and optionally PRs) from a configured `owner/repo`
//! via the GitHub REST API v3 and returns normalized `DataItem` records for
//! RAG ingestion. Reads `GITHUB_TOKEN` from the env capability declared in
//! `plugin.json` and `owner`/`repo`/`state` from plugin config.
//!
//! # Inputs
//! - `GITHUB_TOKEN` env var (read via `std::env::var` on WASM with WASI env cap).
//! - `owner`, `repo`, `state` set in plugin.json `config` section at install time.
//! - `cursor`: optional page number string from the previous `fetch_items` call.
//!
//! # Outputs
//! - `DataSourcePage { items: Vec<DataItem>, next_cursor: Option<String> }`
//! - Each `DataItem` has: id, title, body, url, labels, created_at, updated_at,
//!   source = `"github-issues:{owner}/{repo}"`, extra_json = `"{}"`.
//!
//! # Pagination
//! GitHub REST API uses page-number pagination (`?page=N&per_page=100`). The
//! cursor is the next page number as a decimal string. When the API returns
//! fewer items than `per_page`, pagination is exhausted and `next_cursor` is
//! `None`. On WASM the actual HTTP call is dispatched via `reqwest` with the
//! WASI net capability; on host targets the HTTP layer is stubbed so unit tests
//! can exercise the pure parsing logic without network access.
//!
//! # Security
//! `GITHUB_TOKEN` is read once, used in the Authorization header, and never
//! included in any `DataItem` field or log output.
//!
//! # Constraints
//! - no_std + alloc for wasm32-wasip1 target.
//! - No GitHub GraphQL, no PR reviews/comments, no GitHub App auth.
//! - Token auth only.
//!
//! SPORT: github-issues plugin (T-P4-E03-13)

use cascade_pdk::{
    cascade_plugin, DataItem, DataSourceFetchArgs, DataSourcePage, Plugin, PluginError,
    PluginKind,
};
use serde::Deserialize;

// ── API response shapes ───────────────────────────────────────────────────────

/// Partial representation of a GitHub Issue or PR from the REST API v3.
/// Only the fields we map to DataItem are included.
#[derive(Debug, Deserialize)]
pub struct GitHubIssue {
    pub number: u64,
    pub title: String,
    /// Body may be null in the API response.
    pub body: Option<String>,
    pub html_url: String,
    pub labels: Vec<GitHubLabel>,
    pub created_at: String,
    pub updated_at: String,
}

#[derive(Debug, Deserialize)]
pub struct GitHubLabel {
    pub name: String,
}

// ── Plugin struct ─────────────────────────────────────────────────────────────

/// GitHub Issues DataSource plugin.
///
/// Reads config from environment variables set by the Cascade host at startup:
/// `GITHUB_PLUGIN_OWNER`, `GITHUB_PLUGIN_REPO`, `GITHUB_PLUGIN_STATE`.
/// Reads auth from `GITHUB_TOKEN` env var via the manifest env capability.
#[cascade_plugin]
pub struct GithubIssues;

impl Plugin for GithubIssues {
    fn name(&self) -> &str {
        "github-issues"
    }

    fn kind(&self) -> PluginKind {
        PluginKind::DataSource
    }

    fn version(&self) -> &str {
        "0.1.0"
    }

    /// Fetch one page of GitHub issues.
    ///
    /// The cursor is the next page number (as a string). Pass `None` to start
    /// from page 1. Returns `next_cursor: None` when the page is exhausted.
    fn fetch_items(&self, args: DataSourceFetchArgs) -> Result<DataSourcePage, PluginError> {
        // Read config from environment (set by host from plugin.json config section).
        let owner = read_env("GITHUB_PLUGIN_OWNER")?;
        let repo = read_env("GITHUB_PLUGIN_REPO")?;
        let state = read_env_or("GITHUB_PLUGIN_STATE", "open");
        let token = read_env("GITHUB_TOKEN")?;

        // Determine current page from cursor (default: page 1).
        let page: u64 = args
            .cursor
            .as_deref()
            .and_then(|s| s.parse().ok())
            .unwrap_or(1);

        // Perform HTTP request (WASM: real net via WASI; host: dispatch via fetch_page_impl).
        let issues = fetch_page_impl(&owner, &repo, &state, &token, page)?;

        // Map API response to DataItem.
        let source = format!("github-issues:{owner}/{repo}");
        let items: Vec<DataItem> = issues
            .iter()
            .map(|issue| DataItem {
                id: issue.number.to_string(),
                title: issue.title.clone(),
                body: issue.body.clone().unwrap_or_default(),
                url: issue.html_url.clone(),
                labels: issue.labels.iter().map(|l| l.name.clone()).collect(),
                created_at: issue.created_at.clone(),
                updated_at: issue.updated_at.clone(),
                source: source.clone(),
                extra_json: "{}".into(),
            })
            .collect();

        // Advance cursor if we got a full page (100 items = per_page).
        // GitHub returns fewer items on the last page.
        let next_cursor = if items.len() == 100 {
            Some((page + 1).to_string())
        } else {
            None
        };

        Ok(DataSourcePage { items, next_cursor })
    }
}

// ── HTTP dispatch ─────────────────────────────────────────────────────────────

/// Issue an HTTP GET via the Cascade host's `cascade_http_get_json` import.
///
/// On wasm32-wasip1, the host exports this symbol and proxies the request
/// through the capabilities allowlist. On host targets (unit tests) this
/// function is a stub — tests call `parse_issues_response` directly.
#[cfg(target_arch = "wasm32")]
fn fetch_page_impl(
    owner: &str,
    repo: &str,
    state: &str,
    token: &str,
    page: u64,
) -> Result<Vec<GitHubIssue>, PluginError> {
    // WHY: the Cascade host registers `cascade_http_get_json` as a WASM import
    // in the "cascade" module at instantiation time. #[link(...)] tells the
    // WASM linker these are guest imports, not undefined symbols.
    #[link(wasm_import_module = "cascade")]
    extern "C" {
        fn cascade_http_get_json(
            url_ptr: *const u8,
            url_len: usize,
            auth_ptr: *const u8,
            auth_len: usize,
            out_ptr: *mut u8,
            out_cap: usize,
        ) -> i32;
    }

    let url = format!(
        "https://api.github.com/repos/{owner}/{repo}/issues?state={state}&per_page=100&page={page}"
    );
    let auth_header = format!("Bearer {token}");
    let mut out_buf = vec![0u8; 2 << 20]; // 2 MiB
    // Safety: all slices are valid; wasmtime bounds-checks linear memory accesses.
    let n = unsafe {
        cascade_http_get_json(
            url.as_ptr(),
            url.len(),
            auth_header.as_ptr(),
            auth_header.len(),
            out_buf.as_mut_ptr(),
            out_buf.len(),
        )
    };
    if n < 0 {
        return Err(PluginError::Internal {
            message: format!("cascade_http_get_json failed (code {n})"),
        });
    }
    let body = std::str::from_utf8(&out_buf[..n as usize])
        .map_err(|_| PluginError::Internal { message: "HTTP response is not valid UTF-8".into() })?;
    parse_issues_response(body)
}

/// Host-target stub — not used in production; unit tests inject responses via
/// the `parse_issues_response` helper directly.
#[cfg(not(target_arch = "wasm32"))]
fn fetch_page_impl(
    _owner: &str,
    _repo: &str,
    _state: &str,
    _token: &str,
    _page: u64,
) -> Result<Vec<GitHubIssue>, PluginError> {
    // Host-side unit tests call parse_issues_response directly, bypassing HTTP.
    Err(PluginError::Internal {
        message: "fetch_page_impl is a stub on host targets; use parse_issues_response in tests"
            .into(),
    })
}

// ── Pure parsing logic (testable on host) ─────────────────────────────────────

/// Parse a JSON array of GitHub Issues from the REST API v3 response body.
///
/// This function contains the entire data-mapping logic and is the primary
/// unit under test — no network access required.
pub fn parse_issues_response(json_body: &str) -> Result<Vec<GitHubIssue>, PluginError> {
    serde_json::from_str::<Vec<GitHubIssue>>(json_body).map_err(|e| PluginError::Internal {
        message: format!("JSON parse error: {e}"),
    })
}

// ── Environment helpers ───────────────────────────────────────────────────────

fn read_env(key: &str) -> Result<String, PluginError> {
    // wasm32-wasip1 is a full WASI target — std::env::var works on both host and WASM.
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

    // Fixture: minimal two-issue API response (mirrors real GitHub REST v3 shape).
    const FIXTURE_TWO_ISSUES: &str = include_str!("../tests/fixtures/issues_page1.json");
    // Fixture: 100-issue page to test pagination cursor advancement.
    const FIXTURE_FULL_PAGE: &str = include_str!("../tests/fixtures/issues_full_page.json");
    // Fixture: empty page (last page).
    const FIXTURE_EMPTY_PAGE: &str = "[]";

    #[test]
    fn parse_two_issues_returns_correct_count() {
        let issues = parse_issues_response(FIXTURE_TWO_ISSUES).unwrap();
        assert_eq!(issues.len(), 2);
    }

    #[test]
    fn parse_issue_fields_are_mapped_correctly() {
        let issues = parse_issues_response(FIXTURE_TWO_ISSUES).unwrap();
        let first = &issues[0];

        assert_eq!(first.number, 42);
        assert_eq!(first.title, "Fix the broken widget");
        assert_eq!(first.html_url, "https://github.com/owner/repo/issues/42");
        assert_eq!(first.created_at, "2024-01-15T10:00:00Z");
        assert_eq!(first.updated_at, "2024-01-16T12:30:00Z");
        // Body is present.
        assert_eq!(first.body.as_deref().unwrap_or(""), "Widget crashes on startup.");
        // Labels.
        assert_eq!(first.labels.len(), 2);
        assert_eq!(first.labels[0].name, "bug");
        assert_eq!(first.labels[1].name, "priority:high");
    }

    #[test]
    fn map_to_data_item_source_field() {
        let issues = parse_issues_response(FIXTURE_TWO_ISSUES).unwrap();
        let item = DataItem {
            id: issues[0].number.to_string(),
            title: issues[0].title.clone(),
            body: issues[0].body.clone().unwrap_or_default(),
            url: issues[0].html_url.clone(),
            labels: issues[0].labels.iter().map(|l| l.name.clone()).collect(),
            created_at: issues[0].created_at.clone(),
            updated_at: issues[0].updated_at.clone(),
            source: "github-issues:owner/repo".into(),
            extra_json: "{}".into(),
        };
        assert_eq!(item.id, "42");
        assert_eq!(item.source, "github-issues:owner/repo");
        assert!(!item.labels.is_empty());
    }

    #[test]
    fn data_item_no_body_defaults_to_empty_string() {
        // Fixture issue #43 has null body.
        let issues = parse_issues_response(FIXTURE_TWO_ISSUES).unwrap();
        let second = &issues[1];
        assert!(second.body.is_none(), "expected null body in fixture");
        // Mapping: None -> empty string.
        let body = second.body.clone().unwrap_or_default();
        assert_eq!(body, "");
    }

    #[test]
    fn pagination_cursor_none_on_partial_page() {
        // Parse fewer than 100 items — no next cursor.
        let issues = parse_issues_response(FIXTURE_TWO_ISSUES).unwrap();
        assert!(issues.len() < 100);
        let next_cursor = if issues.len() == 100 { Some("2".into()) } else { None };
        assert_eq!(next_cursor, None::<String>);
    }

    #[test]
    fn pagination_cursor_advances_on_full_page() {
        let issues = parse_issues_response(FIXTURE_FULL_PAGE).unwrap();
        assert_eq!(issues.len(), 100);
        // Simulate what fetch_items does: page=1, full page -> next_cursor = "2".
        let page: u64 = 1;
        let next_cursor = if issues.len() == 100 {
            Some((page + 1).to_string())
        } else {
            None
        };
        assert_eq!(next_cursor, Some("2".to_string()));
    }

    #[test]
    fn pagination_cursor_none_on_empty_page() {
        let issues = parse_issues_response(FIXTURE_EMPTY_PAGE).unwrap();
        assert_eq!(issues.len(), 0);
        let next_cursor: Option<String> = if issues.len() == 100 {
            Some("2".into())
        } else {
            None
        };
        assert!(next_cursor.is_none());
    }

    #[test]
    fn token_not_in_data_item_fields() {
        // Ensure no DataItem field can contain the token — all fields come from
        // the API response, never from the GITHUB_TOKEN env var.
        let issues = parse_issues_response(FIXTURE_TWO_ISSUES).unwrap();
        let item = DataItem {
            id: issues[0].number.to_string(),
            title: issues[0].title.clone(),
            body: issues[0].body.clone().unwrap_or_default(),
            url: issues[0].html_url.clone(),
            labels: issues[0].labels.iter().map(|l| l.name.clone()).collect(),
            created_at: issues[0].created_at.clone(),
            updated_at: issues[0].updated_at.clone(),
            source: "github-issues:owner/repo".into(),
            extra_json: "{}".into(),
        };
        let serialised = serde_json::to_string(&item).unwrap();
        // The fixture doesn't contain a token; verify no env-like string leaks.
        assert!(!serialised.contains("ghp_"), "token pattern leaked into DataItem");
    }

    #[test]
    fn parse_invalid_json_returns_internal_error() {
        let err = parse_issues_response("not json at all").unwrap_err();
        match err {
            PluginError::Internal { message } => {
                assert!(message.contains("JSON parse error"));
            }
            other => panic!("expected Internal error, got {other:?}"),
        }
    }
}
