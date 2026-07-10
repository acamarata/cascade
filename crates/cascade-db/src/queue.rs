//! Purpose: Embedded job queue. SQLite-backed, durable, claim-with-lease semantics.
//! Inputs: a job kind + JSON payload.
//! Outputs: a `JobQueue` trait with a SQLite implementation; replaces any need for Redis lists.
//! Constraints: Redis is NEVER required — this is the default runtime queue.
//! SPORT: cascade-db / queue

use rusqlite::Connection;

use crate::conn::open_configured;
use crate::error::Result;

/// A claimed unit of work.
#[derive(Debug, Clone)]
pub struct Job {
    /// Row id.
    pub id: i64,
    /// Logical job kind (e.g. "ingest", "study", "harvest").
    pub kind: String,
    /// Opaque JSON payload.
    pub payload: String,
    /// Number of times this job has been attempted.
    pub attempts: u32,
}

/// A durable, single-process job queue.
pub trait JobQueue: Send + Sync {
    /// Enqueue a job; returns its id.
    fn enqueue(&self, kind: &str, payload: &str) -> Result<i64>;
    /// Atomically claim the oldest ready job, leasing it for `lease_secs`.
    fn claim(&self, lease_secs: u64) -> Result<Option<Job>>;
    /// Mark a claimed job complete (removes it).
    fn complete(&self, id: i64) -> Result<()>;
    /// Release a job back to the queue (e.g. after a failure), incrementing attempts.
    fn fail(&self, id: i64) -> Result<()>;
    /// Count of jobs not yet completed.
    fn pending(&self) -> Result<u64>;
}

/// SQLite-backed job queue. Stored in the shared `tasks.db` in production.
pub struct SqliteJobQueue {
    conn: std::sync::Mutex<Connection>,
}

impl SqliteJobQueue {
    /// Open (or create) the queue at `path`.
    pub fn open(path: impl AsRef<std::path::Path>) -> Result<Self> {
        let conn = open_configured(path)?;
        Self::from_conn(conn)
    }

    /// Build a queue on an existing connection (e.g. the shared tasks.db).
    pub fn from_conn(conn: Connection) -> Result<Self> {
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS jobs(
                id          INTEGER PRIMARY KEY AUTOINCREMENT,
                kind        TEXT NOT NULL,
                payload     TEXT NOT NULL,
                attempts    INTEGER NOT NULL DEFAULT 0,
                leased_until INTEGER,            -- unix secs while claimed, NULL = ready
                created_at  INTEGER NOT NULL
            );
            CREATE INDEX IF NOT EXISTS idx_jobs_ready ON jobs(leased_until, id);",
        )?;
        Ok(Self {
            conn: std::sync::Mutex::new(conn),
        })
    }

    fn now() -> i64 {
        chrono::Utc::now().timestamp()
    }
}

impl JobQueue for SqliteJobQueue {
    fn enqueue(&self, kind: &str, payload: &str) -> Result<i64> {
        let conn = self.conn.lock().expect("queue mutex poisoned");
        conn.execute(
            "INSERT INTO jobs(kind, payload, created_at) VALUES(?1, ?2, ?3)",
            rusqlite::params![kind, payload, Self::now()],
        )?;
        Ok(conn.last_insert_rowid())
    }

    fn claim(&self, lease_secs: u64) -> Result<Option<Job>> {
        let mut conn = self.conn.lock().expect("queue mutex poisoned");
        let now = Self::now();
        let tx = conn.transaction()?;
        // A job is ready when it has never been leased, or its lease has expired.
        let row = tx
            .query_row(
                "SELECT id, kind, payload, attempts FROM jobs
                 WHERE leased_until IS NULL OR leased_until <= ?1
                 ORDER BY id LIMIT 1",
                [now],
                |r| {
                    Ok(Job {
                        id: r.get(0)?,
                        kind: r.get(1)?,
                        payload: r.get(2)?,
                        attempts: r.get::<_, i64>(3)? as u32,
                    })
                },
            )
            .ok();
        let job = match row {
            Some(j) => j,
            None => return Ok(None),
        };
        tx.execute(
            "UPDATE jobs SET leased_until = ?1 WHERE id = ?2",
            rusqlite::params![now + lease_secs as i64, job.id],
        )?;
        tx.commit()?;
        Ok(Some(job))
    }

    fn complete(&self, id: i64) -> Result<()> {
        let conn = self.conn.lock().expect("queue mutex poisoned");
        conn.execute("DELETE FROM jobs WHERE id = ?1", [id])?;
        Ok(())
    }

    fn fail(&self, id: i64) -> Result<()> {
        let conn = self.conn.lock().expect("queue mutex poisoned");
        conn.execute(
            "UPDATE jobs SET attempts = attempts + 1, leased_until = NULL WHERE id = ?1",
            [id],
        )?;
        Ok(())
    }

    fn pending(&self) -> Result<u64> {
        let conn = self.conn.lock().expect("queue mutex poisoned");
        let n: i64 = conn.query_row("SELECT COUNT(*) FROM jobs", [], |r| r.get(0))?;
        Ok(n as u64)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn enqueue_claim_complete_cycle() {
        let dir = tempfile::tempdir().unwrap();
        let q = SqliteJobQueue::open(dir.path().join("q.db")).unwrap();
        q.enqueue("ingest", "{\"f\":1}").unwrap();
        q.enqueue("ingest", "{\"f\":2}").unwrap();
        assert_eq!(q.pending().unwrap(), 2);

        let j = q.claim(60).unwrap().unwrap();
        assert_eq!(j.kind, "ingest");
        // Second claim returns the *other* job, not the leased one.
        let j2 = q.claim(60).unwrap().unwrap();
        assert_ne!(j.id, j2.id);
        // Nothing else ready while both are leased.
        assert!(q.claim(60).unwrap().is_none());

        q.complete(j.id).unwrap();
        q.fail(j2.id).unwrap(); // back to ready
        let again = q.claim(60).unwrap().unwrap();
        assert_eq!(again.id, j2.id);
        assert_eq!(again.attempts, 1);
    }
}
