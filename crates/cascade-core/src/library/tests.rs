//! Unit tests for the cascade_core::library module.
//!
//! Uses temporary directories so no real `~/.cascade/library/` is touched.
//! Tests are not serial (no global env mutation) so no #[serial(global_env)] needed.

use tempfile::TempDir;

use crate::library::reader::{get_item, list_items};
use crate::library::types::{LibraryItem, LibraryItemKind};
use crate::library::writer::{delete_item, upsert_item};

fn make_item(slug: &str, kind: LibraryItemKind) -> LibraryItem {
    LibraryItem {
        id: slug.to_string(),
        item_type: kind,
        title: format!("Test {slug}"),
        description: "A test item".to_string(),
        content: "Do something useful".to_string(),
        tags: vec!["test".to_string()],
        version: "1.0.0".to_string(),
        created_at: "2026-06-10T00:00:00Z".to_string(),
        updated_at: "2026-06-10T00:00:00Z".to_string(),
        harness_targets: vec!["cc".to_string()],
        metadata: Default::default(),
    }
}

// ── Reader tests ─────────────────────────────────────────────────────────

/// list_items on an empty / missing library root returns empty vec.
#[test]
fn list_items_empty_root_returns_empty() {
    let tmp = TempDir::new().unwrap();
    let result = list_items(tmp.path(), None).unwrap();
    assert!(result.is_empty());
}

/// list_items with a kind filter only returns items from that subdirectory.
#[test]
fn list_items_kind_filter() {
    let tmp = TempDir::new().unwrap();
    let root = tmp.path();

    // Write one prompt and one agent
    let p = make_item("my-prompt", LibraryItemKind::Prompt);
    let a = make_item("my-agent", LibraryItemKind::Agent);
    upsert_item(root, &p).unwrap();
    upsert_item(root, &a).unwrap();

    let prompts = list_items(root, Some("prompts")).unwrap();
    assert_eq!(prompts.len(), 1);
    assert_eq!(prompts[0].id, "my-prompt");

    let all = list_items(root, None).unwrap();
    assert_eq!(all.len(), 2);
}

/// get_item returns the correct item when the file exists.
#[test]
fn get_item_returns_correct_item() {
    let tmp = TempDir::new().unwrap();
    let root = tmp.path();

    let item = make_item("test-persona", LibraryItemKind::Persona);
    upsert_item(root, &item).unwrap();

    let loaded = get_item(root, "personas", "test-persona").unwrap();
    assert_eq!(loaded.id, "test-persona");
    assert_eq!(loaded.title, "Test test-persona");
}

/// get_item returns PathNotFound for a missing slug.
#[test]
fn get_item_missing_returns_path_not_found() {
    let tmp = TempDir::new().unwrap();
    let result = get_item(tmp.path(), "prompts", "nonexistent");
    assert!(result.is_err(), "expected error for missing item, got Ok");
}

// ── Writer tests ─────────────────────────────────────────────────────────

/// upsert creates the file; reading it back produces an equal item.
#[test]
fn upsert_creates_and_roundtrips() {
    let tmp = TempDir::new().unwrap();
    let root = tmp.path();

    let item = make_item("roundtrip-test", LibraryItemKind::SlashCommand);
    upsert_item(root, &item).unwrap();

    let file = root.join("slash-commands").join("roundtrip-test.yaml");
    assert!(file.exists(), "yaml file must exist after upsert");

    let loaded = get_item(root, "slash-commands", "roundtrip-test").unwrap();
    assert_eq!(loaded.id, item.id);
    assert_eq!(loaded.version, item.version);
    assert_eq!(loaded.harness_targets, item.harness_targets);
}

/// upsert overwrites an existing item (same id).
#[test]
fn upsert_overwrites_existing() {
    let tmp = TempDir::new().unwrap();
    let root = tmp.path();

    let mut item = make_item("overwrite-me", LibraryItemKind::Prompt);
    upsert_item(root, &item).unwrap();

    item.title = "Updated Title".to_string();
    upsert_item(root, &item).unwrap();

    let loaded = get_item(root, "prompts", "overwrite-me").unwrap();
    assert_eq!(loaded.title, "Updated Title");
}

/// delete removes the file.
#[test]
fn delete_removes_file() {
    let tmp = TempDir::new().unwrap();
    let root = tmp.path();

    let item = make_item("to-delete", LibraryItemKind::Agent);
    upsert_item(root, &item).unwrap();

    let file = root.join("agents").join("to-delete.yaml");
    assert!(file.exists());

    delete_item(root, "agents", "to-delete").unwrap();
    assert!(!file.exists(), "file must be gone after delete");
}

/// delete on a missing file returns Ok (idempotent).
#[test]
fn delete_missing_is_idempotent() {
    let tmp = TempDir::new().unwrap();
    let result = delete_item(tmp.path(), "prompts", "never-existed");
    assert!(result.is_ok(), "delete of missing file must return Ok");
}
