//! # cascade_core::maps::pews_dag
//!
//! Reads PEWS YAML ticket files under `<phase_root>/.claude/phases/current/p*/`
//! and returns a [`GraphData`] where:
//! - Each ticket becomes a node (id, title, status, weight).
//! - Each `depends_on` entry becomes a directed edge (dependency → dependent).
//!
//! ## YAML schema (minimal required fields)
//!
//! ```yaml
//! id: T-P3-E01-01
//! title: "Some ticket"
//! status: done            # done | pending | in_progress | blocked
//! weight: S               # XS | S | M | L | XL
//! depends_on:             # optional; empty list or absent = no edges
//!   - T-P3-E00-01
//! ```
//!
//! Unknown fields are silently ignored.
//!
//! ## Algorithm
//!
//! 1. Glob `<phase_root>/.claude/phases/current/p*/` for the first matching
//!    phase directory.
//! 2. Walk all `T-*.yaml` files at any depth under that directory.
//! 3. Parse each file with [`toml`]-compatible field extraction; fall back to
//!    the raw `toml` crate for YAML-like parsing (the PEWS files use YAML but
//!    the toml crate is already in the dependency tree — the files use a subset
//!    that both parsers handle). Actually: use the `toml` crate already present
//!    in cascade-core via a lightweight YAML parser based on line-scanning to
//!    avoid adding `serde_yaml` as a new dependency.
//!
//! **Implementation note**: PEWS files are YAML. Since `serde_yaml` is not in
//! the workspace `Cargo.toml`, this module parses the ticket fields via a
//! minimal hand-written YAML scanner that handles only the fields it needs
//! (id, title, status, weight, depends_on). This avoids a new workspace dep
//! while keeping the parser deterministic and testable.
//!
//! ## SPORT
//! MASTER-COMPONENTS.md — PEWS DAG provider

use std::path::{Path, PathBuf};
use tracing::trace;

use super::{GraphData, GraphEdge, GraphNode};

// ---------------------------------------------------------------------------
// Minimal YAML field extractor
// ---------------------------------------------------------------------------

/// Partial representation of a ticket file — only the fields the DAG needs.
#[derive(Debug, Default)]
struct TicketRaw {
    id: String,
    title: String,
    status: String,
    weight: String,
    depends_on: Vec<String>,
}

/// Parse the fields we need from a YAML ticket file using a line-scanner.
///
/// # Purpose
/// Extract `id`, `title`, `status`, `weight`, and `depends_on` without
/// pulling in `serde_yaml` as a new workspace dependency.
///
/// # Constraints
/// - Handles only scalar values and simple YAML sequences (block style).
/// - Multi-line `|`/`>` scalars are not supported for the target fields.
/// - Unknown fields and parse errors are silently skipped.
fn parse_ticket_yaml(content: &str) -> Option<TicketRaw> {
    let mut raw = TicketRaw::default();
    let mut in_depends_on = false;

    for line in content.lines() {
        // Detect sequence items while inside depends_on block.
        if in_depends_on {
            let trimmed = line.trim();
            if trimmed.starts_with('-') {
                let item = trimmed.trim_start_matches('-').trim().trim_matches('"').trim_matches('\'');
                if !item.is_empty() {
                    raw.depends_on.push(item.to_string());
                }
                continue;
            }
            // Any non-indented / non-sequence line ends the depends_on block.
            if !line.starts_with(' ') && !line.starts_with('\t') {
                in_depends_on = false;
            }
        }

        // Parse key: value pairs at top level (no leading whitespace).
        if line.starts_with(' ') || line.starts_with('\t') {
            continue;
        }

        if let Some((key, value)) = split_kv(line) {
            let v = value.trim().trim_matches('"').trim_matches('\'').to_string();
            match key {
                "id" => raw.id = v,
                "title" => raw.title = v,
                "status" => raw.status = v,
                "weight" => raw.weight = v,
                "depends_on" => {
                    // Could be inline `depends_on: [T-..., T-...]` or block sequence.
                    if v.starts_with('[') {
                        // Inline list.
                        let inner = v.trim_start_matches('[').trim_end_matches(']');
                        for item in inner.split(',') {
                            let s = item.trim().trim_matches('"').trim_matches('\'');
                            if !s.is_empty() {
                                raw.depends_on.push(s.to_string());
                            }
                        }
                    } else if v.is_empty() || v == "~" || v == "null" {
                        // Block sequence follows — set flag.
                        in_depends_on = true;
                    }
                    // else: single inline scalar (uncommon — skip)
                }
                _ => {}
            }
        }
    }

    if raw.id.is_empty() {
        return None;
    }
    Some(raw)
}

/// Split `key: value` (returns key and value, both trimmed).
fn split_kv(line: &str) -> Option<(&str, &str)> {
    let colon = line.find(':')?;
    let key = line[..colon].trim();
    let value = line[colon + 1..].trim();
    Some((key, value))
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/// Build a PEWS dependency DAG from the ticket YAML files under `phase_root`.
///
/// # Purpose
/// Powers the "PEWS DAG" view in the Maps panel. Tickets become nodes; their
/// `depends_on` lists become directed edges (dependency → dependent direction).
///
/// # Inputs
/// - `phase_root`: Absolute path to the project root whose PEWS files to read
///   (e.g. `/Users/admin/Sites/acamarata/cascade`). The function looks for
///   `.claude/phases/current/p*/` under this root.
///
/// # Outputs
/// [`GraphData`] where:
/// - Each node has `id`, `label` (title), `nodeType = "ticket"`, `path = ""`,
///   and `meta = {status, weight}`.
/// - Each edge encodes a `depends_on` relationship.
///
/// # Constraints
/// - Missing or unparseable files are silently skipped.
/// - An absent `depends_on` field is treated as an empty list (no edges).
/// - `phase_root/.claude/phases/current/` is HOME-confined by the caller's
///   responsibility; this function does not enforce it.
pub fn get_pews_dag(phase_root: &str) -> GraphData {
    let root = PathBuf::from(phase_root);
    let phases_dir = root.join(".claude").join("phases").join("current");

    if !phases_dir.is_dir() {
        trace!("get_pews_dag: phases dir not found at {:?}", phases_dir);
        return GraphData::empty();
    }

    // Find phase directories (p1, p2, p3, …).
    let phase_dirs = collect_phase_dirs(&phases_dir);
    if phase_dirs.is_empty() {
        trace!("get_pews_dag: no phase dirs found under {:?}", phases_dir);
        return GraphData::empty();
    }

    let mut data = GraphData::empty();

    for phase_dir in &phase_dirs {
        collect_tickets(phase_dir, &mut data);
    }

    data
}

/// Collect all `p*` directories from the phases/current/ dir.
fn collect_phase_dirs(phases_dir: &Path) -> Vec<PathBuf> {
    let mut dirs = Vec::new();
    let entries = match std::fs::read_dir(phases_dir) {
        Ok(e) => e,
        Err(_) => return dirs,
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            let name = path.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
            if name.starts_with('p') {
                dirs.push(path);
            }
        }
    }
    dirs
}

/// Walk all `T-*.yaml` files under `dir` recursively and add them to `data`.
fn collect_tickets(dir: &Path, data: &mut GraphData) {
    let entries = match std::fs::read_dir(dir) {
        Ok(e) => e,
        Err(_) => return,
    };

    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            collect_tickets(&path, data);
        } else if is_ticket_yaml(&path) {
            process_ticket_file(&path, data);
        }
    }
}

/// Return true if the file looks like a ticket spec (T-*.yaml, not T-*.status.yaml).
fn is_ticket_yaml(path: &Path) -> bool {
    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_default();
    name.starts_with("T-") && name.ends_with(".yaml") && !name.ends_with(".status.yaml")
}

/// Parse one ticket file and add its node (and edges) to `data`.
fn process_ticket_file(path: &Path, data: &mut GraphData) {
    let content = match std::fs::read_to_string(path) {
        Ok(c) => c,
        Err(err) => {
            trace!("get_pews_dag: cannot read {:?}: {}", path, err);
            return;
        }
    };

    let raw = match parse_ticket_yaml(&content) {
        Some(r) => r,
        None => {
            trace!("get_pews_dag: no id in {:?}, skipping", path);
            return;
        }
    };

    // Build meta object with status + weight.
    let meta = serde_json::json!({
        "status": raw.status,
        "weight": raw.weight,
    });

    data.nodes.push(GraphNode {
        id: raw.id.clone(),
        label: raw.title.clone(),
        node_type: "ticket".to_string(),
        path: String::new(),
        meta: Some(meta),
    });

    // Add edges for each depends_on entry.
    for dep in &raw.depends_on {
        data.edges.push(GraphEdge {
            from: dep.clone(),
            to: raw.id.clone(),
        });
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;

    /// Helper: write a ticket YAML file.
    fn write_ticket(dir: &Path, filename: &str, content: &str) {
        fs::create_dir_all(dir).unwrap();
        fs::write(dir.join(filename), content).unwrap();
    }

    fn make_phase_root(phase_dir_name: &str) -> (TempDir, PathBuf) {
        let tmp = TempDir::new().unwrap();
        let tickets_dir = tmp
            .path()
            .join(".claude")
            .join("phases")
            .join("current")
            .join(phase_dir_name)
            .join("epics")
            .join("E-01")
            .join("waves")
            .join("W-01")
            .join("sprints")
            .join("S-01")
            .join("tickets");
        fs::create_dir_all(&tickets_dir).unwrap();
        (tmp, tickets_dir)
    }

    #[test]
    fn pews_dag_basic_nodes_and_edges() {
        let (tmp, tickets_dir) = make_phase_root("p3");

        write_ticket(
            &tickets_dir,
            "T-P3-E01-01.yaml",
            "id: T-P3-E01-01\ntitle: \"First ticket\"\nstatus: done\nweight: S\ndepends_on:\n",
        );
        write_ticket(
            &tickets_dir,
            "T-P3-E01-02.yaml",
            "id: T-P3-E01-02\ntitle: \"Second ticket\"\nstatus: pending\nweight: M\ndepends_on:\n  - T-P3-E01-01\n",
        );

        let graph = get_pews_dag(tmp.path().to_str().unwrap());

        assert_eq!(graph.nodes.len(), 2, "expected 2 nodes");
        assert_eq!(graph.edges.len(), 1, "expected 1 edge");

        let t1 = graph.nodes.iter().find(|n| n.id == "T-P3-E01-01").unwrap();
        assert_eq!(t1.node_type, "ticket");
        assert_eq!(t1.label, "First ticket");

        let edge = &graph.edges[0];
        assert_eq!(edge.from, "T-P3-E01-01");
        assert_eq!(edge.to, "T-P3-E01-02");
    }

    #[test]
    fn pews_dag_missing_depends_on_yields_no_edges() {
        let (tmp, tickets_dir) = make_phase_root("p3");

        write_ticket(
            &tickets_dir,
            "T-P3-E01-01.yaml",
            "id: T-P3-E01-01\ntitle: \"Only ticket\"\nstatus: done\nweight: S\n",
        );

        let graph = get_pews_dag(tmp.path().to_str().unwrap());

        assert_eq!(graph.nodes.len(), 1);
        assert_eq!(graph.edges.len(), 0, "no depends_on should produce no edges");
    }

    #[test]
    fn pews_dag_status_yaml_files_skipped() {
        let (tmp, tickets_dir) = make_phase_root("p3");

        write_ticket(
            &tickets_dir,
            "T-P3-E01-01.yaml",
            "id: T-P3-E01-01\ntitle: \"Real ticket\"\nstatus: done\nweight: S\n",
        );
        // .status.yaml sidecar — must be skipped.
        write_ticket(
            &tickets_dir,
            "T-P3-E01-01.status.yaml",
            "id: T-P3-E01-01-status\ntitle: \"Should be ignored\"\nstatus: done\nweight: S\n",
        );

        let graph = get_pews_dag(tmp.path().to_str().unwrap());

        assert_eq!(graph.nodes.len(), 1, "status sidecar should be skipped");
        assert_eq!(graph.nodes[0].id, "T-P3-E01-01");
    }

    #[test]
    fn pews_dag_meta_carries_status_and_weight() {
        let (tmp, tickets_dir) = make_phase_root("p3");

        write_ticket(
            &tickets_dir,
            "T-P3-E01-01.yaml",
            "id: T-P3-E01-01\ntitle: \"A ticket\"\nstatus: in_progress\nweight: L\n",
        );

        let graph = get_pews_dag(tmp.path().to_str().unwrap());

        let node = &graph.nodes[0];
        let meta = node.meta.as_ref().unwrap();
        assert_eq!(meta["status"], "in_progress");
        assert_eq!(meta["weight"], "L");
    }

    #[test]
    fn pews_dag_empty_when_no_phase_dir() {
        let tmp = TempDir::new().unwrap();
        // No .claude/phases/current/ directory.
        let graph = get_pews_dag(tmp.path().to_str().unwrap());
        assert_eq!(graph.nodes.len(), 0);
        assert_eq!(graph.edges.len(), 0);
    }

    #[test]
    fn pews_dag_multiple_depends_on_entries() {
        let (tmp, tickets_dir) = make_phase_root("p3");

        write_ticket(&tickets_dir, "T-P3-E01-01.yaml",
            "id: T-P3-E01-01\ntitle: \"A\"\nstatus: done\nweight: S\n");
        write_ticket(&tickets_dir, "T-P3-E01-02.yaml",
            "id: T-P3-E01-02\ntitle: \"B\"\nstatus: done\nweight: S\n");
        write_ticket(
            &tickets_dir,
            "T-P3-E01-03.yaml",
            "id: T-P3-E01-03\ntitle: \"C\"\nstatus: pending\nweight: M\ndepends_on:\n  - T-P3-E01-01\n  - T-P3-E01-02\n",
        );

        let graph = get_pews_dag(tmp.path().to_str().unwrap());

        assert_eq!(graph.nodes.len(), 3);
        assert_eq!(graph.edges.len(), 2, "two depends_on entries = two edges");
    }
}
