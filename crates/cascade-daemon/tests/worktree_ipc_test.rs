//! Integration tests for worktree IPC commands (AddWorktree, RemoveWorktree, ListWorktrees).

use cascade_daemon::ipc_handlers::{
    handle_add_worktree, handle_list_worktrees, handle_remove_worktree, IpcHandlerError,
};
use std::env;
use std::fs;
use std::sync::OnceLock;
use tempfile::TempDir;
use tokio::sync::Mutex;

fn home_lock() -> &'static Mutex<()> {
    static LOCK: OnceLock<Mutex<()>> = OnceLock::new();
    LOCK.get_or_init(|| Mutex::new(()))
}

/// Sets up a temporary HOME and ensures the async mutex guard is held for the
/// duration so parallel tokio tests do not race on `HOME`.
async fn setup_and_run_with_temp_home<F, Fut, T>(f: F) -> T
where
    F: FnOnce(TempDir, Option<String>) -> Fut,
    Fut: std::future::Future<Output = T>,
{
    let guard = home_lock().lock().await;

    let temp_home = TempDir::new().expect("failed to create temp home");
    let home_path = temp_home.path().to_string_lossy().to_string();

    // Create the .cascade directory that the store needs
    let cascade_dir = std::path::PathBuf::from(&home_path).join(".cascade");
    fs::create_dir_all(&cascade_dir).expect("failed to create .cascade directory");

    let old_home = env::var("HOME").ok();
    env::set_var("HOME", &home_path);

    let result = f(temp_home, old_home.clone()).await;

    // Restore HOME while the guard is still held (prevents interference)
    if let Some(h) = old_home {
        env::set_var("HOME", h);
    }
    drop(guard);

    result
}

#[tokio::test]
async fn test_add_list_remove_worktree() {
    setup_and_run_with_temp_home(|_temp_home, _old_home| async {
        // Create a temporary directory to act as the worktree path.
        let temp_wt = TempDir::new().expect("failed to create temp worktree");
        let worktree_path = temp_wt.path().to_string_lossy().to_string();

        // Add a worktree entry pointing to the created temp path.
        let repo_root = "/tmp/test-repo".to_string();
        let branch = "main".to_string();
        let harness = "cc".to_string();
        let session_id = "test-session-123".to_string();

        let id = handle_add_worktree(
            repo_root.clone(),
            branch.clone(),
            worktree_path.clone(),
            harness.clone(),
            session_id.clone(),
            false, // don't actually run git
        )
        .await
        .expect("AddWorktree should succeed");

        assert!(!id.is_empty(), "returned ID should not be empty");

        // List worktrees and verify the entry is there.
        let entries = handle_list_worktrees()
            .await
            .expect("ListWorktrees should succeed");

        assert_eq!(entries.len(), 1, "should have one worktree entry");
        assert_eq!(entries[0].id, id);
        assert_eq!(entries[0].branch, branch);
        assert_eq!(entries[0].harness, harness);
        assert_eq!(entries[0].session_id, session_id);
        assert!(!entries[0].stale, "new entry should not be stale");

        // Remove the worktree entry.
        handle_remove_worktree(id.clone(), false)
            .await
            .expect("RemoveWorktree should succeed");

        // List worktrees and verify it's gone.
        let entries = handle_list_worktrees()
            .await
            .expect("ListWorktrees after remove should succeed");

        assert_eq!(
            entries.len(),
            0,
            "should have no worktree entries after remove"
        );

        drop(temp_wt);
    })
    .await;
}

#[tokio::test]
async fn test_remove_nonexistent_worktree() {
    setup_and_run_with_temp_home(|_temp_home, _old_home| async {
        // Try to remove a worktree that doesn't exist.
        let remove_result = handle_remove_worktree("nonexistent-id".to_string(), false).await;

        match remove_result {
            Err(IpcHandlerError::NotFound(_)) => {
                // Expected: should get NotFound error
            }
            _ => panic!("RemoveWorktree for nonexistent ID should fail with NotFound"),
        }
    })
    .await;
}

#[tokio::test]
async fn test_stale_worktree_detection() {
    setup_and_run_with_temp_home(|_temp_home, _old_home| async {
        // Create a temporary directory to act as the worktree path.
        let temp_wt = TempDir::new().expect("failed to create temp worktree");
        let worktree_path = temp_wt.path().to_string_lossy().to_string();

        // Add a worktree entry pointing to this temp path.
        let _id = handle_add_worktree(
            "/tmp/test-repo".to_string(),
            "main".to_string(),
            worktree_path.clone(),
            "cc".to_string(),
            "test-session".to_string(),
            false,
        )
        .await
        .expect("AddWorktree should succeed");

        // Verify the entry exists and is not stale.
        let entries = handle_list_worktrees()
            .await
            .expect("ListWorktrees should succeed");

        assert_eq!(entries.len(), 1);
        assert!(!entries[0].stale, "new entry should not be stale");

        // Delete the temp worktree directory.
        drop(temp_wt);

        // List worktrees again; scan_stale should detect the missing path.
        let entries = handle_list_worktrees()
            .await
            .expect("ListWorktrees should succeed");

        assert_eq!(entries.len(), 1, "entry should still be in the list");
        assert!(
            entries[0].stale,
            "entry should be marked stale after path deletion"
        );
    })
    .await;
}
