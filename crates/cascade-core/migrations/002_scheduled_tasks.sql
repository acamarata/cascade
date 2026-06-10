-- Migration 002: Create scheduled_tasks table
--
-- Stores task definitions and execution status for the daemon to pick up
-- and execute according to their schedule specifications.

CREATE TABLE IF NOT EXISTS scheduled_tasks (
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
);

-- Index for finding enabled tasks that need to run
CREATE INDEX IF NOT EXISTS idx_scheduled_tasks_next_run
    ON scheduled_tasks(enabled, next_run);
