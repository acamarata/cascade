//! Unit tests for context_pack — CRUD round-trip, item add/remove, malformed skip.
//!
//! All tests use #[serial(global_env)] because they mutate temp-dir state via
//! HOME env. Tests are isolated via tempfile::tempdir().

use chrono::Utc;
use serial_test::serial;
use tempfile::tempdir;

use super::reader::{get_context, list_contexts};
use super::types::{ContextItem, ContextSession, ItemType};
use super::writer::{delete_context, upsert_context};

// ── helpers ────────────────────────────────────────────────────────────────────

fn make_session(id: &str, title: &str) -> ContextSession {
    ContextSession {
        id: id.to_string(),
        title: title.to_string(),
        created_at: Utc::now(),
        updated_at: Utc::now(),
        items: vec![],
    }
}

fn file_item(path: &str) -> ContextItem {
    ContextItem {
        item_type: ItemType::File,
        path: Some(path.to_string()),
        content: None,
        label: None,
    }
}

fn note_item(content: &str) -> ContextItem {
    ContextItem {
        item_type: ItemType::Note,
        path: None,
        content: Some(content.to_string()),
        label: Some("my note".to_string()),
    }
}

// ── tests ──────────────────────────────────────────────────────────────────────

/// Empty directory returns empty list — no panic.
#[test]
#[serial(global_env)]
fn list_empty_dir_returns_empty() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");
    std::fs::create_dir_all(&ctx_dir).unwrap();
    let result = list_contexts(&ctx_dir).unwrap();
    assert!(result.is_empty());
}

/// list_contexts on a non-existent directory returns empty without error.
#[test]
#[serial(global_env)]
fn list_missing_dir_ok() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");
    // ctx_dir does NOT exist
    let result = list_contexts(&ctx_dir).unwrap();
    assert!(result.is_empty());
}

/// CRUD round-trip: upsert → get → list → delete → list empty.
#[test]
#[serial(global_env)]
fn crud_round_trip() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");

    // Upsert creates the dir and writes the file.
    let session = make_session("my-session", "My Session");
    upsert_context(&ctx_dir, &session).unwrap();

    // get_context returns the session.
    let loaded = get_context(&ctx_dir, "my-session").unwrap();
    assert_eq!(loaded.id, "my-session");
    assert_eq!(loaded.title, "My Session");
    assert!(loaded.items.is_empty());

    // list_contexts returns one meta entry.
    let metas = list_contexts(&ctx_dir).unwrap();
    assert_eq!(metas.len(), 1);
    assert_eq!(metas[0].id, "my-session");
    assert_eq!(metas[0].item_count, 0);

    // delete_context removes the file.
    delete_context(&ctx_dir, "my-session").unwrap();
    let metas2 = list_contexts(&ctx_dir).unwrap();
    assert!(metas2.is_empty());
}

/// upsert overwrites an existing session (update path).
#[test]
#[serial(global_env)]
fn upsert_overwrites_existing() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");

    let s1 = make_session("sess", "Original Title");
    upsert_context(&ctx_dir, &s1).unwrap();

    let s2 = ContextSession {
        id: "sess".to_string(),
        title: "Updated Title".to_string(),
        created_at: s1.created_at,
        updated_at: Utc::now(),
        items: vec![file_item("/some/path.rs")],
    };
    upsert_context(&ctx_dir, &s2).unwrap();

    let loaded = get_context(&ctx_dir, "sess").unwrap();
    assert_eq!(loaded.title, "Updated Title");
    assert_eq!(loaded.items.len(), 1);
}

/// Items add/remove: verify two items round-trip correctly.
#[test]
#[serial(global_env)]
fn item_add_and_remove() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");

    let mut session = make_session("items-test", "Items Test");
    session.items.push(file_item("/src/main.rs"));
    session.items.push(note_item("remember this"));

    upsert_context(&ctx_dir, &session).unwrap();

    let loaded = get_context(&ctx_dir, "items-test").unwrap();
    assert_eq!(loaded.items.len(), 2);

    let f = &loaded.items[0];
    assert_eq!(f.item_type, ItemType::File);
    assert_eq!(f.path.as_deref(), Some("/src/main.rs"));
    assert!(f.content.is_none());

    let n = &loaded.items[1];
    assert_eq!(n.item_type, ItemType::Note);
    assert_eq!(n.content.as_deref(), Some("remember this"));
    assert_eq!(n.label.as_deref(), Some("my note"));

    // list_contexts reports correct item_count.
    let metas = list_contexts(&ctx_dir).unwrap();
    assert_eq!(metas[0].item_count, 2);

    // Remove an item: upsert with one item removed.
    let mut session2 = loaded.clone();
    session2.items.pop();
    upsert_context(&ctx_dir, &session2).unwrap();

    let loaded2 = get_context(&ctx_dir, "items-test").unwrap();
    assert_eq!(loaded2.items.len(), 1);
}

/// Malformed YAML in the directory is silently skipped by list_contexts.
#[test]
#[serial(global_env)]
fn malformed_yaml_skipped_by_list() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");
    std::fs::create_dir_all(&ctx_dir).unwrap();

    // Write a valid session.
    let session = make_session("good", "Good Session");
    upsert_context(&ctx_dir, &session).unwrap();

    // Write a malformed YAML file.
    std::fs::write(ctx_dir.join("bad.yaml"), b"{ invalid: yaml: [[").unwrap();

    // list_contexts skips the bad file and returns only the good one.
    let metas = list_contexts(&ctx_dir).unwrap();
    assert_eq!(metas.len(), 1);
    assert_eq!(metas[0].id, "good");
}

/// Non-YAML files in the contexts directory are ignored.
#[test]
#[serial(global_env)]
fn non_yaml_files_ignored() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");
    std::fs::create_dir_all(&ctx_dir).unwrap();

    std::fs::write(ctx_dir.join("README.md"), b"docs").unwrap();
    std::fs::write(ctx_dir.join(".gitkeep"), b"").unwrap();

    let metas = list_contexts(&ctx_dir).unwrap();
    assert!(metas.is_empty());
}

/// delete_context is idempotent — no error when file is already absent.
#[test]
#[serial(global_env)]
fn delete_idempotent() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");
    std::fs::create_dir_all(&ctx_dir).unwrap();

    // Delete a non-existent id — should be Ok.
    delete_context(&ctx_dir, "does-not-exist").unwrap();
    // Delete again.
    delete_context(&ctx_dir, "does-not-exist").unwrap();
}

/// get_context returns an error for a missing session.
#[test]
#[serial(global_env)]
fn get_missing_returns_error() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");
    std::fs::create_dir_all(&ctx_dir).unwrap();

    let result = get_context(&ctx_dir, "ghost");
    assert!(result.is_err());
}

/// DateTime fields survive a YAML round-trip as RFC3339 strings.
#[test]
#[serial(global_env)]
fn datetime_round_trips_as_rfc3339() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");

    let session = make_session("dt-test", "DateTime Test");
    let original_created = session.created_at;
    upsert_context(&ctx_dir, &session).unwrap();

    // Verify the YAML file contains a string timestamp, not a number.
    let raw = std::fs::read_to_string(ctx_dir.join("dt-test.yaml")).unwrap();
    // RFC3339 strings contain 'T' and 'Z' or offset
    assert!(
        raw.contains('T'),
        "DateTime should be RFC3339 string, got: {}",
        raw
    );

    let loaded = get_context(&ctx_dir, "dt-test").unwrap();
    // Timestamps should match (within second precision after YAML round-trip).
    let diff = (loaded.created_at - original_created).num_seconds().abs();
    assert!(diff < 2, "created_at drifted by {} seconds", diff);
}

/// list_contexts returns results sorted alphabetically by id.
#[test]
#[serial(global_env)]
fn list_sorted_by_id() {
    let dir = tempdir().unwrap();
    let ctx_dir = dir.path().join("contexts");

    upsert_context(&ctx_dir, &make_session("zebra", "Z")).unwrap();
    upsert_context(&ctx_dir, &make_session("alpha", "A")).unwrap();
    upsert_context(&ctx_dir, &make_session("middle", "M")).unwrap();

    let metas = list_contexts(&ctx_dir).unwrap();
    assert_eq!(metas.len(), 3);
    assert_eq!(metas[0].id, "alpha");
    assert_eq!(metas[1].id, "middle");
    assert_eq!(metas[2].id, "zebra");
}
