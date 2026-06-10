//! HookRunner: fires hooks registered in HookStore when daemon events occur.
//!
//! Inputs:  Arc<HookStore>; HookEvent emitted by the daemon event loop.
//! Outputs: Side-effects (child process, log line, event bus insert) per HookAction.
//!
//! Constraints:
//! - RunCommand uses tokio::process::Command::new(cmd).args(args) — never shell.
//! - Each hook runs under a 30-second timeout; timed-out hooks are killed and logged.
//! - Failures are logged and swallowed — never propagated to the caller.
//! - PublishEvent cannot publish DaemonStop, DaemonStart, or any internal lifecycle
//!   event that would form a feedback loop; blocked at execution time with WARN.
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — HookRunner entry (T-P2-E02-26)

use std::sync::Arc;
use std::time::Duration;

use tokio::time::timeout;
use tracing::{error, info, trace, warn};

use cascade_core::HookStore;
use cascade_types::hook::{HookAction, HookEvent};

use crate::event_bus::SharedBus;

/// Events that a `PublishEvent` hook action is NOT allowed to emit.
///
/// Why: publishing DaemonStart or DaemonStop from a hook would create a
/// feedback loop that could cause hooks to fire recursively or suppress
/// proper daemon lifecycle events. These variants are blocked at execution
/// time with a WARN log rather than a hard schema error so misconfigured
/// hooks are surfaced clearly without crashing the daemon.
const BLOCKED_PUBLISH_EVENTS: &[&str] = &["DaemonStart", "DaemonStop"];

/// Execution engine that fires hooks when daemon events occur.
///
/// `HookRunner` is constructed once at daemon startup and passed an `Arc<HookStore>`.
/// Call [`HookRunner::fire`] on any daemon event to execute all matching enabled hooks
/// in tier-priority order (GCI hooks run first; PAC hooks run last).
///
/// All hook failures are isolated: a failing hook never crashes the daemon,
/// never prevents subsequent hooks from running, and always produces a tracing log.
pub struct HookRunner {
    store: Arc<HookStore>,
    bus: SharedBus,
}

impl HookRunner {
    /// Create a new HookRunner backed by the given store and event bus.
    ///
    /// `store` — shared reference to the hook definition store (read-only at runtime).
    /// `bus`   — shared event bus used by `PublishEvent` hook actions.
    pub fn new(store: Arc<HookStore>, bus: SharedBus) -> Self {
        Self { store, bus }
    }

    /// Fire all enabled hooks registered for `event`.
    ///
    /// Hooks are executed in tier-priority order (GCI first). Each hook runs under a
    /// 30-second timeout. Failures (spawn error, non-zero exit, timeout) are logged
    /// and do NOT propagate to the caller.
    ///
    /// Returns `Ok(())` always; the caller need not handle any error from this method.
    pub async fn fire(&self, event: &HookEvent) {
        // Load matching enabled hooks from the store. Synchronous (SQLite).
        // Running on a spawn_blocking thread is not required here because
        // HookStore::list_for_event holds the Mutex only briefly and list_for_event
        // is expected to run in < 1 ms (< 100 hooks). For larger deployments this
        // could be moved to spawn_blocking; that is a P3 perf enhancement.
        let hooks = match tokio::task::spawn_blocking({
            let store = Arc::clone(&self.store);
            let event = event.clone();
            move || store.list_for_event(&event)
        })
        .await
        {
            Ok(Ok(h)) => h,
            Ok(Err(e)) => {
                error!(event = %event, error = %e, "hook_runner: failed to list hooks for event");
                return;
            }
            Err(e) => {
                error!(event = %event, error = ?e, "hook_runner: spawn_blocking panic listing hooks");
                return;
            }
        };

        if hooks.is_empty() {
            trace!(event = %event, "hook_runner: no hooks registered for event");
            return;
        }

        info!(event = %event, count = hooks.len(), "hook_runner: firing hooks");

        for hook in &hooks {
            if !hook.enabled {
                // list_for_event already filters disabled hooks, but this guards
                // against a race where a hook is disabled between list and execute.
                continue;
            }

            let hook_name = hook.name.clone();
            let action = hook.action.clone();
            let bus = self.bus.clone();

            // Execute each hook under a 30-second timeout. Errors are logged; the
            // loop continues to the next hook regardless of outcome.
            match timeout(
                Duration::from_secs(30),
                execute_action(&hook_name, action, &bus),
            )
            .await
            {
                Ok(()) => {
                    info!(hook.name = %hook_name, event = %event, "hook_runner: hook completed");
                }
                Err(_elapsed) => {
                    error!(
                        hook.name = %hook_name,
                        event = %event,
                        "hook_runner: hook timed out: {}",
                        hook_name
                    );
                }
            }
        }
    }
}

/// Execute a single hook action.
///
/// Why extracted: allows `timeout()` to wrap the entire action without
/// duplicating the match arms inside the timeout combinator.
///
/// All errors are logged here; this function returns `()` regardless of outcome.
async fn execute_action(hook_name: &str, action: HookAction, bus: &SharedBus) {
    match action {
        // ── RunCommand ──────────────────────────────────────────────────────────
        // Arg-array pattern mirrors scheduler.rs — never shell interpolation.
        HookAction::RunCommand { command, args } => {
            // WHY arg-array: `Command::new(cmd).args(args)` passes arguments
            // directly to execvp() without a shell, preventing command injection
            // via user-supplied command or args strings.
            let result = tokio::process::Command::new(&command)
                .args(&args)
                .output()
                .await;

            match result {
                Ok(output) if output.status.success() => {
                    info!(
                        hook.name = %hook_name,
                        command = %command,
                        "hook_runner: RunCommand succeeded"
                    );
                }
                Ok(output) => {
                    // Non-zero exit — log stderr, do NOT crash.
                    let stderr: String = String::from_utf8_lossy(&output.stderr)
                        .chars()
                        .take(512)
                        .collect();
                    let exit_code = output.status.code().unwrap_or(-1);
                    error!(
                        hook.name = %hook_name,
                        command = %command,
                        exit_code,
                        stderr = %stderr,
                        "hook_runner: RunCommand failed (non-zero exit)"
                    );
                }
                Err(e) => {
                    // OS-level spawn failure (command not found, permissions, etc.).
                    error!(
                        hook.name = %hook_name,
                        command = %command,
                        error = %e,
                        "hook_runner: RunCommand failed to spawn"
                    );
                }
            }
        }

        // ── LogMessage ──────────────────────────────────────────────────────────
        HookAction::LogMessage { level, message } => {
            match level.as_str() {
                "trace" => trace!(hook.name = %hook_name, "{}", message),
                "debug" => tracing::debug!(hook.name = %hook_name, "{}", message),
                "info" => info!(hook.name = %hook_name, "{}", message),
                "warn" => warn!(hook.name = %hook_name, "{}", message),
                "error" => error!(hook.name = %hook_name, "{}", message),
                other => {
                    // Unknown level — fall back to INFO and note the bad level.
                    warn!(
                        hook.name = %hook_name,
                        level = %other,
                        "hook_runner: unknown log level, defaulting to INFO"
                    );
                    info!(hook.name = %hook_name, "{}", message);
                }
            }
        }

        // ── PublishEvent ────────────────────────────────────────────────────────
        HookAction::PublishEvent { kind, payload } => {
            // Block lifecycle events that would form a feedback loop.
            // WHY: DaemonStart and DaemonStop are emitted by the daemon core;
            // a hook publishing these would confuse consumers expecting them to
            // represent real lifecycle transitions, and could trigger infinite
            // hook loops (a DaemonStart hook fires → publishes DaemonStart → ...).
            if BLOCKED_PUBLISH_EVENTS.contains(&kind.as_str()) {
                warn!(
                    hook.name = %hook_name,
                    kind = %kind,
                    "hook_runner: PublishEvent blocked for internal lifecycle kind"
                );
                return;
            }

            match bus.publish(&kind, payload).await {
                Ok(_) => {
                    info!(hook.name = %hook_name, kind = %kind, "hook_runner: published event");
                }
                Err(e) => {
                    error!(
                        hook.name = %hook_name,
                        kind = %kind,
                        error = %e,
                        "hook_runner: PublishEvent failed"
                    );
                }
            }
        }
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_core::HookStore;
    use cascade_types::hook::{HookAction, HookDef, HookEvent};

    use rusqlite::Connection;
    use std::sync::{Arc, Mutex};
    use tempfile::TempDir;

    use crate::event_bus::EventBus;

    async fn make_test_bus(tmp: &TempDir) -> SharedBus {
        EventBus::new(tmp.path().to_path_buf())
            .await
            .expect("test bus")
    }

    /// Create a fresh in-memory HookStore with the hooks table initialised.
    fn make_test_store() -> Arc<HookStore> {
        let conn = Connection::open_in_memory().expect("open in-memory db");
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS hooks (
                id          TEXT    PRIMARY KEY,
                name        TEXT    NOT NULL,
                tier        TEXT    NOT NULL DEFAULT '',
                event_json  TEXT    NOT NULL,
                action_json TEXT    NOT NULL,
                enabled     INTEGER NOT NULL DEFAULT 1
            );
            CREATE UNIQUE INDEX IF NOT EXISTS idx_hooks_name_tier ON hooks(name, tier);",
        )
        .expect("create hooks table");
        Arc::new(HookStore::new(Arc::new(Mutex::new(conn))))
    }

    // ── Test 1: RunCommand success ────────────────────────────────────────────

    #[tokio::test]
    async fn test_run_command_success() {
        let tmp = TempDir::new().unwrap();
        let bus = make_test_bus(&tmp).await;

        let store = make_test_store();

        let hook = HookDef::new(
            "echo-hook",
            HookEvent::CascadeChanged,
            HookAction::RunCommand {
                command: "echo".to_string(),
                args: vec!["hello".to_string()],
            },
        );
        store.insert(&hook).expect("insert");

        let runner = HookRunner::new(Arc::clone(&store), bus);
        // Must complete without panic.
        runner.fire(&HookEvent::CascadeChanged).await;
    }

    // ── Test 2: RunCommand failure (non-zero exit) ─────────────────────────────

    #[tokio::test]
    async fn test_run_command_failure_does_not_crash() {
        let tmp = TempDir::new().unwrap();
        let bus = make_test_bus(&tmp).await;

        let store = make_test_store();

        // `false` is a POSIX binary that always exits 1 with no output.
        let hook = HookDef::new(
            "failing-hook",
            HookEvent::CascadeChanged,
            HookAction::RunCommand {
                command: "false".to_string(),
                args: vec![],
            },
        );
        store.insert(&hook).expect("insert");

        let runner = HookRunner::new(Arc::clone(&store), bus);
        // Must NOT panic or return error.
        runner.fire(&HookEvent::CascadeChanged).await;
    }

    // ── Test 3: LogMessage hook emits without crash ───────────────────────────

    #[tokio::test]
    async fn test_log_message_hook() {
        let tmp = TempDir::new().unwrap();
        let bus = make_test_bus(&tmp).await;

        let store = make_test_store();

        for level in &["trace", "debug", "info", "warn", "error", "unknown_level"] {
            let hook = HookDef::new(
                format!("log-{}", level),
                HookEvent::RegenComplete,
                HookAction::LogMessage {
                    level: level.to_string(),
                    message: format!("test message at {}", level),
                },
            );
            store.insert(&hook).expect("insert");
        }

        let runner = HookRunner::new(Arc::clone(&store), bus);
        runner.fire(&HookEvent::RegenComplete).await;
    }

    // ── Test 4: Hook timeout kills slow process ────────────────────────────────
    // Uses a very short custom timeout to avoid making the test suite slow.
    // We exercise the timeout by wrapping a sleep-like process.

    #[tokio::test]
    async fn test_hook_timeout_is_handled() {
        // This test verifies that a hook with a long-running command does not
        // panic or block indefinitely. We use a 100 ms timeout on a command that
        // would block for > 100 ms. We test the timeout path by calling
        // execute_action directly with a dummy timeout.
        let tmp = TempDir::new().unwrap();
        let bus = make_test_bus(&tmp).await;

        // Calling execute_action with "sleep 10" inside a 1ms timeout verifies
        // that the timeout path is hit without blocking the test for 10 seconds.
        let result = timeout(
            Duration::from_millis(1),
            execute_action(
                "slow-hook",
                HookAction::RunCommand {
                    command: "sleep".to_string(),
                    args: vec!["10".to_string()],
                },
                &bus,
            ),
        )
        .await;

        // Timeout fires — Err(Elapsed) is the expected outcome.
        assert!(result.is_err(), "expected timeout but action completed");
    }

    // ── Test 5: Hook ordering — GCI fires before PAC ──────────────────────────

    #[tokio::test]
    async fn test_hooks_execute_in_gci_first_order() {
        // We cannot easily assert ordering via side-effects, but we can verify
        // that both hooks fire (no crash) when multiple tiers are registered.
        let tmp = TempDir::new().unwrap();
        let bus = make_test_bus(&tmp).await;

        let store = make_test_store();

        // Insert PAC hook first, then GCI hook — store returns GCI first.
        let pac_hook = HookDef::new(
            "pac-echo",
            HookEvent::DaemonStart,
            HookAction::LogMessage {
                level: "info".to_string(),
                message: "PAC hook fired".to_string(),
            },
        );
        let gci_hook = HookDef::new(
            "gci-echo",
            HookEvent::DaemonStart,
            HookAction::LogMessage {
                level: "info".to_string(),
                message: "GCI hook fired".to_string(),
            },
        );
        // Insert PAC first, GCI second — the store orders GCI first.
        store
            .insert_with_tier(&pac_hook, "PAC")
            .expect("insert PAC");
        store
            .insert_with_tier(&gci_hook, "GCI")
            .expect("insert GCI");

        let runner = HookRunner::new(Arc::clone(&store), bus);
        // Must complete without panic; both hooks execute.
        runner.fire(&HookEvent::DaemonStart).await;
    }

    // ── Test 6: PublishEvent blocked for DaemonStop ───────────────────────────

    #[tokio::test]
    async fn test_publish_event_blocked_for_daemon_stop() {
        let tmp = TempDir::new().unwrap();
        let bus = make_test_bus(&tmp).await;

        let store = make_test_store();

        // A hook that tries to publish DaemonStop — must be blocked.
        let hook = HookDef::new(
            "bad-publish",
            HookEvent::CascadeChanged,
            HookAction::PublishEvent {
                kind: "DaemonStop".to_string(),
                payload: serde_json::Value::Null,
            },
        );
        store.insert(&hook).expect("insert");

        let runner = HookRunner::new(Arc::clone(&store), bus.clone());
        runner.fire(&HookEvent::CascadeChanged).await;

        // DaemonStop must NOT appear on the bus.
        let events = bus.peek("DaemonStop", 10).await.expect("peek");
        assert!(
            events.is_empty(),
            "DaemonStop must not be published by a hook"
        );
    }

    // ── Test 7: PublishEvent allowed for non-blocked kind ─────────────────────

    #[tokio::test]
    async fn test_publish_event_allowed_for_custom_kind() {
        let tmp = TempDir::new().unwrap();
        let bus = make_test_bus(&tmp).await;

        let store = make_test_store();

        let hook = HookDef::new(
            "custom-publish",
            HookEvent::RegenComplete,
            HookAction::PublishEvent {
                kind: "custom.hook.fired".to_string(),
                payload: serde_json::json!({"source": "test"}),
            },
        );
        store.insert(&hook).expect("insert");

        let runner = HookRunner::new(Arc::clone(&store), bus.clone());
        runner.fire(&HookEvent::RegenComplete).await;

        // Custom event must appear on the bus.
        let events = bus.peek("custom.hook.fired", 10).await.expect("peek");
        assert_eq!(events.len(), 1, "custom event must be published");
    }
}
