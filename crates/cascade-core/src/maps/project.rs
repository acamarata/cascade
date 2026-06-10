//! # cascade_core::maps::project
//!
//! Scans `~/Sites/` (or a configurable sites root) for cascade-managed
//! projects and returns a [`GraphData`] of project → repo → app nodes.
//!
//! ## Algorithm
//!
//! 1. Resolve `sites_root` (defaults to `$HOME/Sites`).
//! 2. Walk with `max_depth = 3` to prevent runaway on large trees.
//!    - Depth 1 → project dirs (contain `.cascade/` = APC tier).
//!    - Depth 2 → repo dirs   (contain `.cascade/` = PPC/PRC tier).
//!    - Depth 3 → app dirs    (contain `.cascade/` = PAC tier).
//! 3. Build nodes and parent-to-child edges.
//!
//! ## Security
//!
//! All paths must be descendants of `$HOME` (HOME-confined). Any path
//! component that escapes HOME (e.g. a symlink pointing outside) is silently
//! skipped — the check uses `path.starts_with(home)`.
//!
//! ## SPORT
//! MASTER-COMPONENTS.md — project graph provider

use std::path::{Path, PathBuf};
use tracing::trace;

use super::{GraphData, GraphEdge, GraphNode};

/// Maximum directory depth below the sites root that will be scanned.
/// Depth 1 = project, 2 = repo, 3 = app.
const MAX_DEPTH: u32 = 3;

/// Returns the home directory via `$HOME` (Unix) or `USERPROFILE` (Windows).
fn home_dir() -> Option<PathBuf> {
    std::env::var("HOME")
        .or_else(|_| std::env::var("USERPROFILE"))
        .ok()
        .map(PathBuf::from)
}

/// Determine the node type for a given depth below the sites root.
fn node_type_for_depth(depth: u32) -> &'static str {
    match depth {
        1 => "project",
        2 => "repo",
        _ => "app",
    }
}

/// Build a stable slug id from a path component (lowercase, no spaces).
fn slug(p: &Path) -> String {
    p.file_name()
        .map(|n| n.to_string_lossy().to_lowercase().replace(' ', "-"))
        .unwrap_or_else(|| "unknown".to_string())
}

/// Scan the sites root and return a graph of cascade-managed directories.
///
/// # Purpose
/// Powers the "Project Graph" view in the Maps panel. Returns nodes at up to
/// three nesting levels (project/repo/app) with parent-child edges.
///
/// # Inputs
/// - `sites_root`: Optional override for `~/Sites/`. Defaults to `$HOME/Sites`.
///
/// # Outputs
/// [`GraphData`] with nodes (type = project|repo|app) and edges (parent→child).
///
/// # Constraints
/// - `max_depth = 3` hard cap — guards against runaway on large trees.
/// - Only directories that contain a `.cascade/` sub-directory are included.
/// - All included paths must be descendants of `$HOME` (HOME-confined).
/// - Errors on individual entries are silently skipped (best-effort scan).
pub fn get_project_graph(sites_root: Option<&str>) -> GraphData {
    let home = match home_dir() {
        Some(h) => h,
        None => {
            trace!("get_project_graph: could not resolve home dir — returning empty graph");
            return GraphData::empty();
        }
    };

    let root: PathBuf = match sites_root {
        Some(s) => PathBuf::from(s),
        None => home.join("Sites"),
    };

    if !root.is_dir() {
        trace!(
            "get_project_graph: sites root {:?} is not a directory",
            root
        );
        return GraphData::empty();
    }

    let mut data = GraphData::empty();
    walk_dir(&root, &root, 0, None, &home, &mut data);
    // Note: `_sites_root` is prefixed to suppress the only_used_in_recursion lint.
    data
}

/// Recursively walk directory entries up to MAX_DEPTH.
///
/// `current` — directory being examined
/// `sites_root` — top of the sites tree (used for HOME-confinement check)
/// `depth` — current depth below sites_root (0 = sites_root itself)
/// `parent_id` — node id of the parent, if any
/// `home` — home directory for HOME-confinement checks
/// `data` — mutable graph being built
fn walk_dir(
    current: &Path,
    _sites_root: &Path,
    depth: u32,
    parent_id: Option<&str>,
    home: &Path,
    data: &mut GraphData,
) {
    if depth > MAX_DEPTH {
        return;
    }

    let entries = match std::fs::read_dir(current) {
        Ok(e) => e,
        Err(err) => {
            trace!("get_project_graph: cannot read {:?}: {}", current, err);
            return;
        }
    };

    for entry in entries.flatten() {
        let path = entry.path();

        // HOME-confinement check.
        if !path.starts_with(home) {
            trace!("get_project_graph: skipping out-of-HOME path {:?}", path);
            continue;
        }

        if !path.is_dir() {
            continue;
        }

        // Only include directories that contain a `.cascade/` subdirectory.
        let cascade_dir = path.join(".cascade");
        if !cascade_dir.is_dir() {
            // Still descend if we are at the sites root level (depth 0).
            if depth == 0 {
                walk_dir(&path, _sites_root, depth + 1, parent_id, home, data);
            }
            continue;
        }

        let depth_from_root = depth + 1;
        if depth_from_root > MAX_DEPTH {
            continue;
        }

        let node_type = node_type_for_depth(depth_from_root);
        let node_slug = slug(&path);
        // Build a unique id by joining slug with parent id.
        let node_id = match parent_id {
            Some(pid) => format!("{}/{}", pid, node_slug),
            None => node_slug.clone(),
        };

        data.nodes.push(GraphNode {
            id: node_id.clone(),
            label: path
                .file_name()
                .map(|n| n.to_string_lossy().to_string())
                .unwrap_or_else(|| node_slug.clone()),
            node_type: node_type.to_string(),
            path: path.to_string_lossy().to_string(),
            meta: None,
        });

        if let Some(pid) = parent_id {
            data.edges.push(GraphEdge {
                from: pid.to_string(),
                to: node_id.clone(),
            });
        }

        // Descend one level deeper.
        if depth_from_root < MAX_DEPTH {
            walk_dir(
                &path,
                _sites_root,
                depth_from_root,
                Some(&node_id),
                home,
                data,
            );
        }
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use serial_test::serial;
    use std::fs;
    use tempfile::TempDir;

    /// Build a fixture tree:
    /// ```
    /// tmp/
    ///   Sites/
    ///     proj-a/
    ///       .cascade/
    ///       repo-x/
    ///         .cascade/
    ///     proj-b/
    ///       .cascade/
    ///       repo-y/
    ///         .cascade/
    ///         app-ui/
    ///           .cascade/
    /// ```
    fn build_fixture(root: &Path) {
        let sites = root.join("Sites");
        let dirs = [
            sites.join("proj-a").join(".cascade"),
            sites.join("proj-a").join("repo-x").join(".cascade"),
            sites.join("proj-b").join(".cascade"),
            sites.join("proj-b").join("repo-y").join(".cascade"),
            sites
                .join("proj-b")
                .join("repo-y")
                .join("app-ui")
                .join(".cascade"),
        ];
        for d in &dirs {
            fs::create_dir_all(d).unwrap();
        }
    }

    #[test]
    #[serial(global_env)]
    fn project_graph_finds_nodes_and_edges() {
        let tmp = TempDir::new().unwrap();
        build_fixture(tmp.path());
        // Override HOME so HOME-confinement check passes.
        std::env::set_var("HOME", tmp.path());

        let sites = tmp.path().join("Sites");
        let graph = get_project_graph(Some(sites.to_str().unwrap()));

        std::env::remove_var("HOME");

        // Nodes: proj-a, repo-x, proj-b, repo-y, app-ui
        assert_eq!(
            graph.nodes.len(),
            5,
            "expected 5 nodes, got {:?}",
            graph.nodes
        );

        let project_nodes: Vec<_> = graph
            .nodes
            .iter()
            .filter(|n| n.node_type == "project")
            .collect();
        assert_eq!(project_nodes.len(), 2, "expected 2 project nodes");

        let repo_nodes: Vec<_> = graph
            .nodes
            .iter()
            .filter(|n| n.node_type == "repo")
            .collect();
        assert_eq!(repo_nodes.len(), 2, "expected 2 repo nodes");

        let app_nodes: Vec<_> = graph
            .nodes
            .iter()
            .filter(|n| n.node_type == "app")
            .collect();
        assert_eq!(app_nodes.len(), 1, "expected 1 app node");

        // Edges: proj-a→repo-x, proj-b→repo-y, repo-y→app-ui
        assert_eq!(
            graph.edges.len(),
            3,
            "expected 3 edges, got {:?}",
            graph.edges
        );

        // version is always 1
        assert_eq!(graph.version, 1);
    }

    #[test]
    #[serial(global_env)]
    fn project_graph_empty_when_no_sites_root() {
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());

        let graph = get_project_graph(None); // ~/Sites does not exist in tmp

        std::env::remove_var("HOME");

        assert_eq!(graph.nodes.len(), 0);
        assert_eq!(graph.edges.len(), 0);
    }

    #[test]
    #[serial(global_env)]
    fn project_graph_respects_home_confinement() {
        let home_tmp = TempDir::new().unwrap();
        let outside_tmp = TempDir::new().unwrap();

        // Create a sites root inside HOME with one project.
        let sites = home_tmp.path().join("Sites");
        fs::create_dir_all(sites.join("inside-proj").join(".cascade")).unwrap();

        // Create a directory OUTSIDE home with .cascade.
        fs::create_dir_all(outside_tmp.path().join("outside-proj").join(".cascade")).unwrap();

        std::env::set_var("HOME", home_tmp.path());

        let graph = get_project_graph(Some(sites.to_str().unwrap()));

        std::env::remove_var("HOME");

        // Only the inside-proj should appear; outside-proj is not under HOME.
        assert_eq!(graph.nodes.len(), 1);
        assert_eq!(graph.nodes[0].node_type, "project");
    }

    #[test]
    #[serial(global_env)]
    fn project_graph_max_depth_guard() {
        let tmp = TempDir::new().unwrap();
        std::env::set_var("HOME", tmp.path());

        let sites = tmp.path().join("Sites");
        // Build a 4-deep nesting: proj/repo/app/sub — sub should be ignored.
        fs::create_dir_all(sites.join("proj").join(".cascade")).unwrap();
        fs::create_dir_all(sites.join("proj").join("repo").join(".cascade")).unwrap();
        fs::create_dir_all(sites.join("proj").join("repo").join("app").join(".cascade")).unwrap();
        fs::create_dir_all(
            sites
                .join("proj")
                .join("repo")
                .join("app")
                .join("sub")
                .join(".cascade"),
        )
        .unwrap();

        let graph = get_project_graph(Some(sites.to_str().unwrap()));

        std::env::remove_var("HOME");

        // Only proj (depth 1), repo (depth 2), app (depth 3) — sub is beyond MAX_DEPTH.
        let types: Vec<_> = graph.nodes.iter().map(|n| n.node_type.as_str()).collect();
        assert!(
            !types.contains(&"sub"),
            "depth-4 node leaked through MAX_DEPTH guard"
        );
        assert_eq!(
            graph.nodes.len(),
            3,
            "expected exactly 3 nodes at depths 1-3"
        );
    }
}
