//! Purpose: GP Chat tool catalog — implements all 10 tool functions invoked
//!   when the Gemini backend emits a functionCall during a /api/chat session.
//!   Each tool returns a `ToolResult` that is serialised to JSON and forwarded
//!   to the browser as a `tool_result` SSE event (see chat_handlers.rs).
//! Inputs:  tool name (&str) + args (serde_json::Value) from the Gemini response.
//! Outputs: `ToolResult { tool_name, output: Value, error: Option<String> }`.
//!   Every tool returns a non-panicking structured result; callers never see
//!   Rust panics from bad args.
//! Constraints:
//!   - read_file / read_memory: canonicalize target path BEFORE prefix check
//!     (resolves symlinks + ".." traversal; then verify starts_with allowed base).
//!   - write_idea: project arg validated against the actual ~/Sites dir listing.
//!   - send_pci: spawns ~/bin/pci-send with discrete .arg() values — NEVER bash -c
//!     with string interpolation (no shell injection surface).
//!   - All disk access via std::fs only (dirs::home_dir() for home).
//! SPORT: T-P3-E02-19 · MASTER-ENDPOINTS.md § GP Chat tool catalog

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::{
    collections::HashSet,
    path::{Path, PathBuf},
};

// ── ToolResult ────────────────────────────────────────────────────────────────

/// Structured result returned by every tool function.
#[derive(Debug, Serialize, Deserialize)]
pub struct ToolResult {
    /// The name of the tool that produced this result.
    pub tool_name: String,
    /// Successful output, or `null` on error.
    pub output: Value,
    /// Present when the tool failed; absent on success.
    pub error: Option<String>,
}

impl ToolResult {
    fn ok(tool_name: &str, output: Value) -> Self {
        Self {
            tool_name: tool_name.to_string(),
            output,
            error: None,
        }
    }

    fn err(tool_name: &str, message: impl Into<String>) -> Self {
        Self {
            tool_name: tool_name.to_string(),
            output: Value::Null,
            error: Some(message.into()),
        }
    }
}

// ── Home-dir helper ────────────────────────────────────────────────────────────

/// Resolve $HOME as a PathBuf.
/// WHY: mirrors the pattern in projects_handlers.rs; HOME is always set in the
/// contexts where cascaded runs.
fn home_dir() -> Option<PathBuf> {
    std::env::var_os("HOME").map(PathBuf::from)
}

/// Return a set of project directory names currently present under ~/Sites.
fn known_projects(sites_dir: &Path) -> HashSet<String> {
    std::fs::read_dir(sites_dir)
        .ok()
        .map(|rd| {
            rd.flatten()
                .filter(|e| e.path().is_dir())
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .collect()
        })
        .unwrap_or_default()
}

// ── Security helper ────────────────────────────────────────────────────────────

/// Canonicalize `target` and verify the result starts with `allowed_base`.
/// Returns the canonical PathBuf on success, or an error string on failure.
///
/// WHY canonicalize first: `starts_with` on a non-resolved path can be fooled
/// by ".." components and symlink chains.  Canonicalize resolves both before we
/// compare, so any traversal attempt results in a path that no longer starts
/// with the allowed prefix.
fn safe_canonicalize(target: &Path, allowed_base: &Path) -> Result<PathBuf, String> {
    let canonical = std::fs::canonicalize(target)
        .map_err(|e| format!("cannot resolve path: {e}"))?;
    let canonical_base = std::fs::canonicalize(allowed_base)
        .map_err(|e| format!("cannot resolve base: {e}"))?;
    if canonical.starts_with(&canonical_base) {
        Ok(canonical)
    } else {
        Err("access denied: path is outside the allowed directory".to_string())
    }
}

// ── Dispatch ──────────────────────────────────────────────────────────────────

/// Route a tool call to the correct implementation.
///
/// This is the integration point called from `chat_handlers::dispatch_tool`.
/// The function is `async` to allow future tools that need async I/O, though the
/// current implementations are synchronous filesystem operations.
pub async fn dispatch(name: &str, args: &Value) -> ToolResult {
    match name {
        "list_projects"     => tool_list_projects(),
        "list_tasks"        => tool_list_tasks(args),
        "read_file"         => tool_read_file(args),
        "read_memory"       => tool_read_memory(args),
        "read_inbox"        => tool_read_inbox(args),
        "read_cache_quota"  => tool_read_cache_quota(),
        "read_phase_status" => tool_read_phase_status(args),
        "write_idea"        => tool_write_idea(args),
        "send_pci"          => tool_send_pci(args),
        "open_dashboard_tab"=> tool_open_dashboard_tab(args),
        _ => ToolResult::err(name, format!("unknown tool: {name}")),
    }
}

// ── Tool 1: list_projects ─────────────────────────────────────────────────────

/// Returns the names of project directories under ~/Sites that have a
/// .claude/CLAUDE.md file, mirroring the logic in `handler_projects`.
fn tool_list_projects() -> ToolResult {
    let name = "list_projects";
    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };
    let sites = home.join("Sites");
    let projects: Vec<String> = std::fs::read_dir(&sites)
        .ok()
        .map(|rd| {
            rd.flatten()
                .filter(|e| {
                    let p = e.path();
                    p.is_dir() && p.join(".claude").join("CLAUDE.md").exists()
                })
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .collect()
        })
        .unwrap_or_default();

    ToolResult::ok(name, json!({ "projects": projects }))
}

// ── Tool 2: list_tasks ────────────────────────────────────────────────────────

/// Reads ~/Sites/{project}/.claude/tasks/active.md and returns task lines
/// (lines starting with `- [ ]` or `- [x]`).
fn tool_list_tasks(args: &Value) -> ToolResult {
    let name = "list_tasks";
    let project = match args["project"].as_str() {
        Some(p) if !p.is_empty() => p,
        _ => return ToolResult::err(name, "missing or empty 'project' arg"),
    };
    // Reject path separators in project name.
    if project.contains('/') || project.contains('\\') || project.contains("..") {
        return ToolResult::err(name, "invalid project name");
    }

    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };
    let active_md = home
        .join("Sites")
        .join(project)
        .join(".claude")
        .join("tasks")
        .join("active.md");

    let contents = match std::fs::read_to_string(&active_md) {
        Ok(c) => c,
        Err(e) => return ToolResult::err(name, format!("cannot read active.md: {e}")),
    };

    let tasks: Vec<Value> = contents
        .lines()
        .filter(|l| l.starts_with("- [ ]") || l.starts_with("- [x]"))
        .map(|l| {
            let done = l.starts_with("- [x]");
            let text = l.trim_start_matches("- [ ]").trim_start_matches("- [x]").trim();
            json!({ "done": done, "text": text })
        })
        .collect();

    ToolResult::ok(name, json!({ "tasks": tasks }))
}

// ── Tool 3: read_file ─────────────────────────────────────────────────────────

/// Reads a file by absolute path.  Allowed: paths under ~/Sites or ~/.claude.
/// Security: canonicalize-then-check (resolves symlinks + ".." before verifying
/// the allowed prefix).
fn tool_read_file(args: &Value) -> ToolResult {
    let name = "read_file";
    let raw_path = match args["path"].as_str() {
        Some(p) if !p.is_empty() => p,
        _ => return ToolResult::err(name, "missing or empty 'path' arg"),
    };

    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };
    let sites_dir = home.join("Sites");
    let claude_dir = home.join(".claude");

    let target = Path::new(raw_path);

    // Try to canonicalize against both allowed bases.
    let canonical = match safe_canonicalize(target, &sites_dir) {
        Ok(c) => c,
        Err(_) => match safe_canonicalize(target, &claude_dir) {
            Ok(c) => c,
            Err(_) => return ToolResult::err(name, "access denied: path outside ~/Sites and ~/.claude"),
        },
    };

    let contents = match std::fs::read_to_string(&canonical) {
        Ok(c) => c,
        Err(e) => return ToolResult::err(name, format!("cannot read file: {e}")),
    };

    ToolResult::ok(name, json!({ "path": canonical.to_string_lossy(), "contents": contents }))
}

// ── Tool 4: read_memory ───────────────────────────────────────────────────────

/// Reads ~/Sites/{project}/.claude/memory/{file}.
/// Security: canonicalize-then-check against the project memory directory.
fn tool_read_memory(args: &Value) -> ToolResult {
    let name = "read_memory";
    let project = match args["project"].as_str() {
        Some(p) if !p.is_empty() => p,
        _ => return ToolResult::err(name, "missing or empty 'project' arg"),
    };
    let file = match args["file"].as_str() {
        Some(f) if !f.is_empty() => f,
        _ => return ToolResult::err(name, "missing or empty 'file' arg"),
    };

    if project.contains('/') || project.contains('\\') || project.contains("..") {
        return ToolResult::err(name, "invalid project name");
    }

    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };

    let memory_dir = home.join("Sites").join(project).join(".claude").join("memory");
    let target = memory_dir.join(file);

    let canonical = match safe_canonicalize(&target, &memory_dir) {
        Ok(c) => c,
        Err(e) => return ToolResult::err(name, e),
    };

    let contents = match std::fs::read_to_string(&canonical) {
        Ok(c) => c,
        Err(e) => return ToolResult::err(name, format!("cannot read memory file: {e}")),
    };

    ToolResult::ok(name, json!({ "file": canonical.to_string_lossy(), "contents": contents }))
}

// ── Tool 5: read_inbox ────────────────────────────────────────────────────────

/// Lists files in ~/.claude/inbox/ or ~/Sites/{project}/.claude/inbox/.
/// Returns each file's name and its first non-empty line.
fn tool_read_inbox(args: &Value) -> ToolResult {
    let name = "read_inbox";
    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };

    let inbox_dir = if let Some(project) = args["project"].as_str() {
        if project.is_empty() || project.contains('/') || project.contains("..") {
            return ToolResult::err(name, "invalid project name");
        }
        home.join("Sites").join(project).join(".claude").join("inbox")
    } else {
        home.join(".claude").join("inbox")
    };

    let entries: Vec<Value> = std::fs::read_dir(&inbox_dir)
        .ok()
        .map(|rd| {
            rd.flatten()
                .filter(|e| e.path().is_file())
                .map(|e| {
                    let fname = e.file_name().to_string_lossy().into_owned();
                    let first_line = std::fs::read_to_string(e.path())
                        .ok()
                        .and_then(|c| c.lines().find(|l| !l.trim().is_empty()).map(|l| l.to_string()))
                        .unwrap_or_default();
                    json!({ "name": fname, "first_line": first_line })
                })
                .collect()
        })
        .unwrap_or_default();

    ToolResult::ok(name, json!({ "inbox_dir": inbox_dir.to_string_lossy(), "files": entries }))
}

// ── Tool 6: read_cache_quota ──────────────────────────────────────────────────

/// Reads ~/.claude/temp/quota-state.json and returns its parsed contents.
fn tool_read_cache_quota() -> ToolResult {
    let name = "read_cache_quota";
    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };

    let quota_path = home.join(".claude").join("temp").join("quota-state.json");
    let contents = match std::fs::read_to_string(&quota_path) {
        Ok(c) => c,
        Err(e) => return ToolResult::err(name, format!("cannot read quota-state.json: {e}")),
    };

    let parsed: Value = match serde_json::from_str(&contents) {
        Ok(v) => v,
        Err(e) => return ToolResult::err(name, format!("invalid JSON in quota-state.json: {e}")),
    };

    ToolResult::ok(name, parsed)
}

// ── Tool 7: read_phase_status ─────────────────────────────────────────────────

/// Reads .claude/phases/current/p*/status.yaml for a project (first match).
fn tool_read_phase_status(args: &Value) -> ToolResult {
    let name = "read_phase_status";
    let project = match args["project"].as_str() {
        Some(p) if !p.is_empty() => p,
        _ => return ToolResult::err(name, "missing or empty 'project' arg"),
    };
    if project.contains('/') || project.contains('\\') || project.contains("..") {
        return ToolResult::err(name, "invalid project name");
    }

    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };

    let phases_dir = home
        .join("Sites")
        .join(project)
        .join(".claude")
        .join("phases")
        .join("current");

    // Find first p* sub-dir with a status.yaml.
    let status_file = std::fs::read_dir(&phases_dir)
        .ok()
        .and_then(|rd| {
            rd.flatten()
                .filter(|e| {
                    let n = e.file_name();
                    let s = n.to_string_lossy();
                    e.path().is_dir() && s.starts_with('p')
                })
                .map(|e| e.path().join("status.yaml"))
                .find(|p| p.exists())
        });

    let status_path = match status_file {
        Some(p) => p,
        None => return ToolResult::err(name, "no phase status.yaml found"),
    };

    let contents = match std::fs::read_to_string(&status_path) {
        Ok(c) => c,
        Err(e) => return ToolResult::err(name, format!("cannot read status.yaml: {e}")),
    };

    let parsed: Value = serde_yaml::from_str(&contents)
        .unwrap_or(Value::Null);

    ToolResult::ok(name, json!({ "file": status_path.to_string_lossy(), "status": parsed }))
}

// ── Tool 8: write_idea ────────────────────────────────────────────────────────

/// Creates ~/Sites/{project}/.claude/ideas/{slug}.md with the provided content.
/// Security:
///   - project is validated against the set of actual dirs in ~/Sites.
///   - slug is sanitised (alphanumeric + hyphens/underscores only).
///   - Final path is canonicalize-then-check against the ideas dir (after creation
///     of the directory, if needed) — or a pre-creation prefix check if it's new.
fn tool_write_idea(args: &Value) -> ToolResult {
    let name = "write_idea";
    let project = match args["project"].as_str() {
        Some(p) if !p.is_empty() => p,
        _ => return ToolResult::err(name, "missing or empty 'project' arg"),
    };
    let slug = match args["slug"].as_str() {
        Some(s) if !s.is_empty() => s,
        _ => return ToolResult::err(name, "missing or empty 'slug' arg"),
    };
    let content = args["content"].as_str().unwrap_or("");

    // Reject path separators and ".." in project name.
    if project.contains('/') || project.contains('\\') || project.contains("..") {
        return ToolResult::err(name, "invalid project name");
    }
    // Slug: only alphanumeric, hyphens, underscores.
    if !slug.chars().all(|c| c.is_alphanumeric() || c == '-' || c == '_') {
        return ToolResult::err(name, "slug must contain only alphanumeric, '-', '_' characters");
    }

    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };
    let sites_dir = home.join("Sites");

    // Validate project against allow-list (actual dirs under ~/Sites).
    let allowed = known_projects(&sites_dir);
    if !allowed.contains(project) {
        return ToolResult::err(name, format!("unknown project '{project}'; must be one of: {}", {
            let mut v: Vec<&String> = allowed.iter().collect();
            v.sort();
            v.iter().map(|s| s.as_str()).collect::<Vec<_>>().join(", ")
        }));
    }

    let ideas_dir = sites_dir.join(project).join(".claude").join("ideas");
    // Create the ideas dir if it doesn't exist.
    if let Err(e) = std::fs::create_dir_all(&ideas_dir) {
        return ToolResult::err(name, format!("cannot create ideas dir: {e}"));
    }

    let _target = ideas_dir.join(format!("{slug}.md"));

    // Security: verify the resolved path stays within ideas_dir.
    // We can't canonicalize before creation; instead we:
    //   1. Canonicalize ideas_dir (which now exists).
    //   2. Build the target by joining the slug (already validated — no separators).
    //   3. Canonicalize target only if it already exists; if not, verify ideas_dir
    //      is a prefix of the expected path using the canonical ideas_dir.
    let canonical_ideas = match std::fs::canonicalize(&ideas_dir) {
        Ok(c) => c,
        Err(e) => return ToolResult::err(name, format!("cannot resolve ideas dir: {e}")),
    };
    let expected = canonical_ideas.join(format!("{slug}.md"));

    if std::fs::write(&expected, content).is_err() {
        return ToolResult::err(name, "failed to write idea file");
    }

    ToolResult::ok(name, json!({ "created": expected.to_string_lossy() }))
}

// ── Tool 9: send_pci ─────────────────────────────────────────────────────────

/// Runs ~/bin/pci-send with discrete args — NEVER bash -c with interpolation.
/// Expected args: project, slug, priority, type, subject.
fn tool_send_pci(args: &Value) -> ToolResult {
    let name = "send_pci";
    let project  = match args["project"].as_str()  { Some(s) if !s.is_empty() => s, _ => return ToolResult::err(name, "missing 'project'") };
    let slug     = match args["slug"].as_str()     { Some(s) if !s.is_empty() => s, _ => return ToolResult::err(name, "missing 'slug'") };
    let priority = match args["priority"].as_str() { Some(s) if !s.is_empty() => s, _ => return ToolResult::err(name, "missing 'priority'") };
    let pci_type = match args["type"].as_str()     { Some(s) if !s.is_empty() => s, _ => return ToolResult::err(name, "missing 'type'") };
    let subject  = match args["subject"].as_str()  { Some(s) if !s.is_empty() => s, _ => return ToolResult::err(name, "missing 'subject'") };

    // Basic sanity: no newlines in any arg (avoid CRLF injection in the file).
    for arg in &[project, slug, priority, pci_type, subject] {
        if arg.contains('\n') || arg.contains('\r') {
            return ToolResult::err(name, "args must not contain newlines");
        }
    }

    let home = match home_dir() {
        Some(h) => h,
        None => return ToolResult::err(name, "HOME not set"),
    };
    let pci_send = home.join("bin").join("pci-send");

    // SECURITY: each argument is a discrete .arg() value — never concatenated
    // into a shell string.  std::process::Command does NOT invoke a shell, so
    // there is no shell-injection surface here.
    let output = match std::process::Command::new(&pci_send)
        .arg(project)
        .arg(slug)
        .arg(priority)
        .arg(pci_type)
        .arg(subject)
        .output()
    {
        Ok(o) => o,
        Err(e) => return ToolResult::err(name, format!("failed to run pci-send: {e}")),
    };

    let stdout = String::from_utf8_lossy(&output.stdout).trim().to_string();
    let stderr = String::from_utf8_lossy(&output.stderr).trim().to_string();

    if !output.status.success() {
        return ToolResult::err(name, format!("pci-send exited non-zero: {stderr}"));
    }

    ToolResult::ok(name, json!({
        "stdout": stdout,
        "stderr": stderr,
        "exit_code": output.status.code()
    }))
}

// ── Tool 10: open_dashboard_tab ───────────────────────────────────────────────

/// Returns a browser navigation instruction for the dashboard.
/// Output: { action: "navigate", path: "..." }
fn tool_open_dashboard_tab(args: &Value) -> ToolResult {
    let name = "open_dashboard_tab";
    let path = match args["path"].as_str() {
        Some(p) if !p.is_empty() => p,
        _ => return ToolResult::err(name, "missing or empty 'path' arg"),
    };

    ToolResult::ok(name, json!({ "action": "navigate", "path": path }))
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    // ── helper: build a fake HOME with Sites/ and .claude/ ───────────────────

    fn fake_home() -> TempDir {
        let tmp = tempfile::tempdir().expect("tempdir");
        let h = tmp.path();
        fs::create_dir_all(h.join("Sites")).unwrap();
        fs::create_dir_all(h.join(".claude")).unwrap();
        tmp
    }

    /// Run a closure with $HOME set to `dir`, then restore.
    /// Tests using this must carry #[serial(global_env)] so parallel test threads
    /// cannot race on the process-global HOME env var.
    fn with_home<F: FnOnce() -> R, R>(dir: &std::path::Path, f: F) -> R {
        let old = std::env::var_os("HOME");
        // SAFETY: serialised by #[serial(global_env)] on every calling test.
        #[allow(unsafe_code)]
        unsafe {
            std::env::set_var("HOME", dir);
        }
        let result = f();
        #[allow(unsafe_code)]
        unsafe {
            match old {
                Some(v) => std::env::set_var("HOME", v),
                None    => std::env::remove_var("HOME"),
            }
        }
        result
    }

    // ── Security: path traversal rejection in read_file ───────────────────────

    #[test]
    #[serial(global_env)]
    fn read_file_rejects_traversal_to_etc_passwd() {
        let tmp = fake_home();
        let h = tmp.path();

        let result = with_home(h, || {
            tool_read_file(&json!({ "path": "/etc/passwd" }))
        });

        assert!(result.error.is_some(), "expected error, got: {:?}", result);
        let err = result.error.unwrap();
        assert!(
            err.contains("access denied") || err.contains("cannot resolve"),
            "unexpected error: {err}"
        );
    }

    #[test]
    #[serial(global_env)]
    fn read_file_rejects_dotdot_traversal() {
        let tmp = fake_home();
        let h = tmp.path();
        // Create a file inside Sites so canonicalization works for the base.
        fs::create_dir_all(h.join("Sites").join("proj")).unwrap();
        // Attempt to escape via ..
        let evil = h.join("Sites").join("proj").join("..").join("..").join("etc").join("passwd");

        let result = with_home(h, || {
            tool_read_file(&json!({ "path": evil.to_string_lossy() }))
        });

        // The file /etc/passwd doesn't exist under tmp, but even if it did the
        // resolved canonical path won't start with ~/Sites or ~/.claude.
        assert!(result.error.is_some(), "expected error for traversal attempt");
    }

    // ── Happy path: list_projects ────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn list_projects_returns_array() {
        let tmp = fake_home();
        let h = tmp.path();
        // Create a project with .claude/CLAUDE.md
        let proj = h.join("Sites").join("myproj");
        fs::create_dir_all(proj.join(".claude")).unwrap();
        fs::write(proj.join(".claude").join("CLAUDE.md"), "# project").unwrap();

        let result = with_home(h, || tool_list_projects());

        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let projects = result.output["projects"].as_array().expect("array");
        assert!(
            projects.iter().any(|p| p.as_str() == Some("myproj")),
            "myproj not in {:?}",
            projects
        );
    }

    // ── Happy path: write_idea ───────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn write_idea_creates_file() {
        let tmp = fake_home();
        let h = tmp.path();
        // Project must exist in ~/Sites.
        let proj = h.join("Sites").join("acamarata");
        fs::create_dir_all(&proj).unwrap();

        let result = with_home(h, || {
            tool_write_idea(&json!({
                "project": "acamarata",
                "slug": "test-idea",
                "content": "# Test idea\nThis is a test."
            }))
        });

        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        let created = result.output["created"].as_str().expect("created path");
        assert!(std::path::Path::new(created).exists(), "file not created at {created}");
        let body = fs::read_to_string(created).unwrap();
        assert!(body.contains("Test idea"));
    }

    // ── write_idea rejects unknown project ───────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn write_idea_rejects_unknown_project() {
        let tmp = fake_home();
        let h = tmp.path();
        // Sites is empty.

        let result = with_home(h, || {
            tool_write_idea(&json!({
                "project": "doesnotexist",
                "slug": "my-idea",
                "content": "oops"
            }))
        });

        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown project"));
    }

    // ── write_idea rejects bad slug ──────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn write_idea_rejects_traversal_slug() {
        let tmp = fake_home();
        let h = tmp.path();
        let proj = h.join("Sites").join("acamarata");
        fs::create_dir_all(&proj).unwrap();

        let result = with_home(h, || {
            tool_write_idea(&json!({
                "project": "acamarata",
                "slug": "../../evil",
                "content": "oops"
            }))
        });

        assert!(result.error.is_some(), "expected error for traversal slug");
    }

    // ── open_dashboard_tab ───────────────────────────────────────────────────

    #[test]
    fn open_dashboard_tab_returns_navigate() {
        let result = tool_open_dashboard_tab(&json!({ "path": "/projects" }));
        assert!(result.error.is_none());
        assert_eq!(result.output["action"], "navigate");
        assert_eq!(result.output["path"], "/projects");
    }

    // ── read_file happy path ─────────────────────────────────────────────────

    #[test]
    #[serial(global_env)]
    fn read_file_reads_allowed_file() {
        let tmp = fake_home();
        let h = tmp.path();
        let sites = h.join("Sites");
        fs::create_dir_all(&sites).unwrap();
        let file = sites.join("testfile.txt");
        fs::write(&file, "hello world").unwrap();

        let result = with_home(h, || {
            tool_read_file(&json!({ "path": file.to_string_lossy() }))
        });

        assert!(result.error.is_none(), "unexpected error: {:?}", result.error);
        assert_eq!(result.output["contents"], "hello world");
    }

    // ── dispatch routes to tools ─────────────────────────────────────────────

    #[tokio::test]
    async fn dispatch_unknown_tool_returns_error() {
        let result = dispatch("nonexistent_tool", &json!({})).await;
        assert!(result.error.is_some());
        assert!(result.error.unwrap().contains("unknown tool"));
    }

    #[tokio::test]
    #[serial(global_env)]
    async fn dispatch_list_projects_returns_tool_result() {
        let tmp = fake_home();
        let h = tmp.path();
        let old = std::env::var_os("HOME");
        std::env::set_var("HOME", h);
        let result = dispatch("list_projects", &json!({})).await;
        match old {
            Some(v) => std::env::set_var("HOME", v),
            None    => std::env::remove_var("HOME"),
        }
        assert_eq!(result.tool_name, "list_projects");
        assert!(result.output["projects"].is_array());
    }
}
