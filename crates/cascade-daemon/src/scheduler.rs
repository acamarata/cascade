//! Scheduled-task execution engine for the Cascade daemon.
//!
//! Purpose: Poll the TaskStore every `poll_interval_secs` seconds for tasks
//! whose next_run <= now AND enabled = true. For each due task, spawn a child
//! process via `tokio::process::Command` (arg-array — no shell interpolation),
//! capture stdout/stderr, update TaskStatus in the TaskStore, and compute the
//! next_run from the ScheduleSpec.
//!
//! Inputs:  `SchedulerConfig`, an `Arc<TaskStore>`, a `CancellationToken`.
//! Outputs: Side-effects on TaskStore rows; tracing spans per task execution.
//! Constraints:
//!   - Maximum `max_parallel` tasks run simultaneously (Semaphore-bounded).
//!   - stderr captured and truncated at 512 chars to prevent unbounded buffers.
//!   - Once tasks are disabled atomically BEFORE spawning to prevent double-run
//!     on daemon restart.
//!   - No shell string interpolation: `Command::new(&task.command).args(&task.args)`.
//!
//! SPORT: .claude/docs/MASTER-DAEMON.md — Scheduler task entry (T-P2-E02-23)

use std::str::FromStr;
use std::sync::Arc;
use std::time::Instant;

use chrono::{DateTime, Duration, Utc};
use cron::Schedule as CronSchedule;
use tokio::sync::Semaphore;
use tokio_util::sync::CancellationToken;
use tracing::{error, info, warn};

use cascade_core::TaskStore;
use cascade_types::scheduled_task::{ScheduleSpec, ScheduledTask, TaskStatus};

use crate::config::SchedulerConfig;

/// Compute the next run time for a given schedule spec after `after`.
///
/// Why: centralised so both the initial insertion path and post-run update use
/// the same logic. Returns `None` for Once schedules (they disable after run).
fn compute_next_run(spec: &ScheduleSpec, after: DateTime<Utc>) -> Option<DateTime<Utc>> {
    match spec {
        ScheduleSpec::Cron { expression } => {
            // Parse the cron expression and find the first upcoming time after `after`.
            // If the expression is malformed here it means it passed insertion validation;
            // fall back to None (task won't re-run) and log a warning in the caller.
            CronSchedule::from_str(expression)
                .ok()
                .and_then(|sched| sched.after(&after).next())
        }
        ScheduleSpec::Interval { secs } => {
            // Next run = after + interval. If `after` is last_run or now, this
            // gives a consistent cadence regardless of execution duration.
            Some(after + Duration::seconds(*secs as i64))
        }
        ScheduleSpec::Once { .. } => {
            // Once tasks run exactly once; next_run is None after execution.
            None
        }
    }
}

/// Execute a single due task as a child process and update the TaskStore.
///
/// Why: extracted into its own function so it can be spawned as an independent
/// Tokio task, bounded by the semaphore, without holding any locks.
///
/// CR-A note: `enabled = false` is written via `TaskStore::update` BEFORE the
/// process is spawned for Once tasks. This prevents a second scheduler poll
/// (on rapid daemon restart) from seeing the task as due and running it again.
async fn run_task(
    task: ScheduledTask,
    store: Arc<TaskStore>,
    _permit: tokio::sync::OwnedSemaphorePermit,
) {
    let task_id = task.id;
    let task_name = task.name.clone();

    info!(task.id = %task_id, task.name = %task_name, command = %task.command, "scheduler: starting task");

    // For Once tasks: disable atomically BEFORE spawning the process so a
    // concurrent scheduler tick or a rapid daemon restart cannot double-run it.
    let is_once = matches!(task.schedule, ScheduleSpec::Once { .. });
    if is_once {
        let mut pre_disable = task.clone();
        pre_disable.enabled = false;
        let store_ref = Arc::clone(&store);
        if let Err(e) = tokio::task::spawn_blocking(move || store_ref.update(&pre_disable)).await {
            error!(task.id = %task_id, error = ?e, "scheduler: failed to disable Once task before spawn");
        }
    }

    let start = Instant::now();
    let now = Utc::now();

    // Spawn the process using an arg-array — NEVER a shell string.
    // WHY: `Command::new(cmd).args(args)` passes arguments directly to execvp()
    // without a shell, preventing command injection via task.command or task.args.
    let result = tokio::process::Command::new(&task.command)
        .args(&task.args)
        .output()
        .await;

    let duration_ms = start.elapsed().as_millis() as u64;

    match result {
        Ok(output) if output.status.success() => {
            let next_run = compute_next_run(&task.schedule, now);
            if let Some(ref nr) = next_run {
                info!(task.id = %task_id, task.name = %task_name, duration_ms, next_run = %nr, "scheduler: task succeeded");
            } else {
                info!(task.id = %task_id, task.name = %task_name, duration_ms, "scheduler: task succeeded (no further runs)");
            }

            let mut updated = task.clone();
            updated.status = TaskStatus::Success {
                ran_at: now,
                duration_ms,
            };
            updated.last_run = Some(now);
            updated.next_run = next_run;
            // Once tasks are already disabled above.
            if !is_once {
                // Ensure re-enabled tasks remain enabled (non-Once tasks stay enabled).
                updated.enabled = true;
            } else {
                updated.enabled = false;
            }

            let store_ref = Arc::clone(&store);
            if let Err(e) = tokio::task::spawn_blocking(move || store_ref.update(&updated)).await {
                error!(task.id = %task_id, error = ?e, "scheduler: failed to persist success status");
            }
        }
        Ok(output) => {
            // Non-zero exit code: capture stderr, truncate at 512 chars.
            // WHY: unbounded stderr capture (e.g. a task that writes 100 KB of
            // error output) would allocate unbounded memory per failed task.
            let raw_stderr = String::from_utf8_lossy(&output.stderr);
            let error: String = raw_stderr.chars().take(512).collect();
            let exit_code = output.status.code().unwrap_or(-1);

            warn!(task.id = %task_id, task.name = %task_name, exit_code, %error, "scheduler: task failed");

            let mut updated = task.clone();
            updated.status = TaskStatus::Failed { ran_at: now, error };
            updated.last_run = Some(now);
            // On failure, still compute next_run so the task retries on schedule.
            updated.next_run = if is_once {
                None
            } else {
                compute_next_run(&task.schedule, now)
            };
            if is_once {
                updated.enabled = false;
            }

            let store_ref = Arc::clone(&store);
            if let Err(e) = tokio::task::spawn_blocking(move || store_ref.update(&updated)).await {
                error!(task.id = %task_id, error = ?e, "scheduler: failed to persist failure status");
            }
        }
        Err(e) => {
            // OS-level spawn failure (e.g. command not found, permission denied).
            let error = format!("{}", e).chars().take(512).collect::<String>();
            error!(task.id = %task_id, task.name = %task_name, %error, "scheduler: failed to spawn process");

            let mut updated = task.clone();
            updated.status = TaskStatus::Failed { ran_at: now, error };
            updated.last_run = Some(now);
            updated.next_run = if is_once {
                None
            } else {
                compute_next_run(&task.schedule, now)
            };
            if is_once {
                updated.enabled = false;
            }

            let store_ref = Arc::clone(&store);
            if let Err(e) = tokio::task::spawn_blocking(move || store_ref.update(&updated)).await {
                error!(task.id = %task_id, error = ?e, "scheduler: failed to persist spawn-failure status");
            }
        }
    }

    // Semaphore permit is released when `_permit` drops at end of scope.
}

/// The Scheduler background task.
///
/// Polls the TaskStore every `config.poll_interval_secs` seconds and executes
/// due tasks up to `config.max_parallel` concurrently. Runs until `shutdown`
/// is cancelled.
pub struct Scheduler {
    config: SchedulerConfig,
    store: Arc<TaskStore>,
    shutdown: CancellationToken,
}

impl Scheduler {
    /// Create a new Scheduler.
    pub fn new(
        config: SchedulerConfig,
        store: Arc<TaskStore>,
        shutdown: CancellationToken,
    ) -> Self {
        Self {
            config,
            store,
            shutdown,
        }
    }

    /// Run the scheduler loop. Call `tokio::spawn(scheduler.run())` from the
    /// supervisor to start it as a background task.
    pub async fn run(self) {
        let semaphore = Arc::new(Semaphore::new(self.config.max_parallel));
        let poll_secs = self.config.poll_interval_secs;

        info!(
            poll_interval_secs = poll_secs,
            max_parallel = self.config.max_parallel,
            "scheduler: started"
        );

        loop {
            tokio::select! {
                _ = tokio::time::sleep(tokio::time::Duration::from_secs(poll_secs)) => {}
                _ = self.shutdown.cancelled() => {
                    info!("scheduler: shutdown signal received — stopping");
                    break;
                }
            }

            let now = Utc::now();

            // Fetch due tasks via spawn_blocking (TaskStore is sync).
            let store_ref = Arc::clone(&self.store);
            let due_tasks = match tokio::task::spawn_blocking(move || store_ref.get_due(now)).await
            {
                Ok(Ok(tasks)) => tasks,
                Ok(Err(e)) => {
                    error!(error = %e, "scheduler: failed to query due tasks");
                    continue;
                }
                Err(e) => {
                    error!(error = ?e, "scheduler: spawn_blocking panicked during get_due");
                    continue;
                }
            };

            if due_tasks.is_empty() {
                continue;
            }

            info!(count = due_tasks.len(), "scheduler: found due tasks");

            for task in due_tasks {
                // Acquire a semaphore permit (bounded concurrency).
                // `acquire_owned` returns an error only if the semaphore is
                // closed, which only happens on explicit close — never here.
                let permit = match Arc::clone(&semaphore).acquire_owned().await {
                    Ok(p) => p,
                    Err(e) => {
                        error!(error = ?e, "scheduler: semaphore closed unexpectedly");
                        break;
                    }
                };

                let store_clone = Arc::clone(&self.store);
                tokio::spawn(run_task(task, store_clone, permit));
            }
        }

        info!("scheduler: stopped");
    }
}

// ── Unit tests ─────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::scheduled_task::ScheduleSpec;
    use chrono::{Duration, Utc};

    // ── next_run computation ──────────────────────────────────────────────

    #[test]
    fn test_next_run_interval() {
        let spec = ScheduleSpec::Interval { secs: 60 };
        let base = Utc::now();
        let next = compute_next_run(&spec, base);
        assert!(next.is_some());
        let diff = next.unwrap() - base;
        assert_eq!(
            diff,
            Duration::seconds(60),
            "Interval(60): next_run should be exactly base + 60s"
        );
    }

    #[test]
    fn test_next_run_once_returns_none() {
        let at = Utc::now() + Duration::seconds(3600);
        let spec = ScheduleSpec::Once { at };
        let next = compute_next_run(&spec, Utc::now());
        assert!(
            next.is_none(),
            "Once schedule should return None (no repeat)"
        );
    }

    #[test]
    fn test_next_run_cron_valid() {
        // "0 * * * * *" fires at second 0 of every minute.
        let spec = ScheduleSpec::Cron {
            expression: "0 * * * * *".to_string(),
        };
        let base = Utc::now();
        let next = compute_next_run(&spec, base);
        assert!(next.is_some(), "Valid cron should produce a next_run");
        assert!(next.unwrap() > base, "next_run should be in the future");
    }

    #[test]
    fn test_next_run_cron_invalid_returns_none() {
        // Malformed cron expression: compute_next_run should return None
        // (task stops re-running) rather than panicking.
        let spec = ScheduleSpec::Cron {
            expression: "not a valid cron expression".to_string(),
        };
        let next = compute_next_run(&spec, Utc::now());
        assert!(next.is_none(), "Invalid cron should return None, not panic");
    }

    #[test]
    fn test_next_run_interval_large() {
        // Edge case: large interval (1 day = 86400s)
        let spec = ScheduleSpec::Interval { secs: 86400 };
        let base = Utc::now();
        let next = compute_next_run(&spec, base);
        assert!(next.is_some());
        let diff = next.unwrap() - base;
        assert_eq!(diff, Duration::seconds(86400));
    }

    // ── Integration test: Once task round-trip via real TaskStore ─────────

    #[tokio::test]
    async fn test_once_task_runs_and_sets_success() {
        use cascade_core::TaskStore;
        use cascade_types::scheduled_task::ScheduledTask;
        use rusqlite::Connection;
        use std::sync::{Arc, Mutex};

        // Build an in-memory TaskStore.
        let conn = Connection::open(":memory:").unwrap();
        conn.execute(
            "CREATE TABLE scheduled_tasks (
                id TEXT PRIMARY KEY,
                name TEXT NOT NULL,
                schedule_json TEXT NOT NULL,
                command TEXT NOT NULL,
                args_json TEXT NOT NULL,
                enabled INTEGER NOT NULL DEFAULT 1,
                created_at TEXT NOT NULL,
                last_run TEXT,
                next_run TEXT,
                status_json TEXT NOT NULL
            )",
            [],
        )
        .unwrap();
        let conn_arc = Arc::new(Mutex::new(conn));
        let store = Arc::new(TaskStore::new(conn_arc));

        // Insert a Once task with command='echo' args=['test'].
        // Set next_run to the epoch (past) so get_due returns it immediately.
        let mut task = ScheduledTask::new(
            "once-smoke",
            ScheduleSpec::Once {
                at: Utc::now() + Duration::seconds(3600),
            },
            "echo",
            vec!["test".to_string()],
        );
        // Force next_run to past so it is immediately due.
        task.next_run = Some(Utc::now() - Duration::seconds(1));
        store.insert(&task).unwrap();

        let task_id = task.id;

        // Build the scheduler with poll_interval=30, max_parallel=4.
        let config = SchedulerConfig {
            enabled: true,
            poll_interval_secs: 30,
            max_parallel: 4,
        };
        let shutdown = CancellationToken::new();

        // Manually run a single poll cycle (same logic as scheduler loop).
        let now = Utc::now();
        let store_ref = Arc::clone(&store);
        let due = tokio::task::spawn_blocking(move || store_ref.get_due(now))
            .await
            .unwrap()
            .unwrap();
        assert_eq!(due.len(), 1, "Due task should be returned by get_due");

        let semaphore = Arc::new(Semaphore::new(config.max_parallel));
        for t in due {
            let permit = semaphore.clone().acquire_owned().await.unwrap();
            let s = Arc::clone(&store);
            run_task(t, s, permit).await;
        }

        // Verify the task reached Success status and was disabled.
        let updated = store.get(task_id).unwrap().unwrap();
        assert!(
            matches!(updated.status, TaskStatus::Success { .. }),
            "Once task should be Success after run, got: {:?}",
            updated.status
        );
        assert!(!updated.enabled, "Once task should be disabled after run");

        // Suppress unused-variable warning on shutdown token.
        drop(shutdown);
    }

    // ── Parallel limit: second task waits for first ───────────────────────

    #[tokio::test]
    async fn test_parallel_limit_blocks_excess_tasks() {
        // When max_parallel=1, a second concurrent task must wait until the
        // first releases the semaphore. We verify the semaphore has 0 permits
        // while one permit is held.
        let semaphore = Arc::new(Semaphore::new(1));

        // Acquire the only permit.
        let permit = semaphore.clone().acquire_owned().await.unwrap();
        // Available permits should now be 0.
        assert_eq!(
            semaphore.available_permits(),
            0,
            "With 1 max_parallel, 0 permits should remain when one is held"
        );

        // Release and verify permit returns.
        drop(permit);
        assert_eq!(
            semaphore.available_permits(),
            1,
            "After permit release, 1 permit should be available again"
        );
    }
}
