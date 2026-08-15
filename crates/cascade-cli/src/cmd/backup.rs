//! `cascade backup` subcommand — manage backup snapshots.
//!
//! Purpose: List, verify, and restore backup snapshots created by the daemon's
//! scheduled backup task.
//!
//! Subcommands:
//!   - `cascade backup list` — list snapshots for a given tier
//!   - `cascade backup run` — create a snapshot now (used by the scheduler)
//!   - `cascade backup schedule` — register a recurring backup task in the
//!     daemon's persistent scheduler (`~/.cascade/scheduler.db`); the daemon
//!     executes `cascade backup run` at the chosen interval
//!   - (future) `cascade backup restore` — restore from a snapshot (P3)
//!   - (future) `cascade backup verify` — check integrity (P3)
//!
//! SPORT: .claude/docs/MASTER-CLI.md — backup command entry (T-P2-E02-21)

use async_trait::async_trait;
use cascade_types::error::{CascadeError, Result};
use clap::{Args, Subcommand};
use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use super::Command;

/// Name of the scheduled task registered by `cascade backup schedule`.
const BACKUP_TASK_NAME: &str = "cascade-backup";

/// Backup arguments for `cascade backup`.
#[derive(Debug, Args)]
pub struct BackupArgs {
    /// Backup subcommand variant.
    #[command(subcommand)]
    pub cmd: BackupSubcommand,
}

/// Supported schedule intervals for `cascade backup schedule`.
#[derive(Debug, Clone, clap::ValueEnum)]
pub enum ScheduleInterval {
    /// Once per day.
    Daily,
    /// Once per week.
    Weekly,
    /// Once per hour.
    Hourly,
}

/// Interval length in seconds for each [`ScheduleInterval`].
fn interval_secs(interval: &ScheduleInterval) -> u64 {
    match interval {
        ScheduleInterval::Daily => 24 * 60 * 60,
        ScheduleInterval::Weekly => 7 * 24 * 60 * 60,
        ScheduleInterval::Hourly => 60 * 60,
    }
}

/// Backup subcommand variants.
#[derive(Debug, Subcommand)]
pub enum BackupSubcommand {
    /// List snapshots for a given tier.
    List {
        /// Tier name (e.g., "GCI", "PPI").
        #[arg(value_name = "TIER")]
        tier: String,

        /// Backup root directory (default: ~/.cascade/backups).
        #[arg(long, value_name = "PATH")]
        backup_root: Option<PathBuf>,
    },

    /// Create backup snapshots now for every readable tier.
    ///
    /// Copies each tier's `.cascade/` instruction files (CASCADE.md and
    /// siblings, symlink targets included) into
    /// `{backup_root}/{TIER}/snapshot-<timestamp>/`.  This is the command the
    /// daemon's scheduler executes for scheduled backups.
    Run {
        /// Directory tier discovery starts from (default: current directory).
        #[arg(long, value_name = "PATH")]
        cwd: Option<PathBuf>,

        /// Backup root directory (default: ~/.cascade/backups).
        #[arg(long, value_name = "PATH")]
        backup_root: Option<PathBuf>,
    },

    /// Configure automatic backup schedule.
    ///
    /// Writes `backup.schedule` to ~/.cascade/config.toml and registers a
    /// recurring task in the daemon's persistent scheduler
    /// (`~/.cascade/scheduler.db`) so the daemon runs
    /// `cascade backup run --cwd <dir>` at the chosen interval.
    Schedule {
        /// How often to run an automatic backup.
        #[arg(value_enum, default_value = "daily")]
        interval: ScheduleInterval,
    },
}

#[async_trait]
impl Command for BackupArgs {
    async fn run(&self) -> Result<()> {
        match &self.cmd {
            BackupSubcommand::List { tier, backup_root } => {
                list_snapshots(tier, backup_root.clone()).await
            }
            BackupSubcommand::Run { cwd, backup_root } => {
                run_snapshot(cwd.clone(), backup_root.clone()).await
            }
            BackupSubcommand::Schedule { interval } => set_schedule(interval).await,
        }
    }
}

/// Resolve the backup root: explicit override, else `~/.cascade/backups`.
fn resolve_backup_root(backup_root: Option<PathBuf>) -> Result<PathBuf> {
    backup_root
        .or_else(|| {
            std::env::var("HOME")
                .ok()
                .map(|h| PathBuf::from(h).join(".cascade").join("backups"))
        })
        .ok_or_else(|| {
            CascadeError::Other("cannot determine backup root: provide --backup-root".into())
        })
}

/// List snapshots for a given tier.
///
/// Reads all `snapshot-*` directories under `{backup_root}/{tier}/`,
/// sorts lexicographically (oldest first), and displays count and names.
async fn list_snapshots(tier: &str, backup_root: Option<PathBuf>) -> Result<()> {
    let backup_root = resolve_backup_root(backup_root)?;

    let tier_root = backup_root.join(tier);

    if !tier_root.exists() {
        println!("no backups found for tier '{}'", tier);
        return Ok(());
    }

    // Collect and sort snapshot directories.
    let mut snapshots: Vec<PathBuf> = std::fs::read_dir(&tier_root)
        .map_err(|e| CascadeError::Other(e.to_string()))?
        .filter_map(|entry| {
            let entry = entry.ok()?;
            let path = entry.path();
            let name = entry.file_name();
            if path.is_dir() && name.to_string_lossy().starts_with("snapshot-") {
                Some(path)
            } else {
                None
            }
        })
        .collect();

    snapshots.sort();

    if snapshots.is_empty() {
        println!("no snapshots found for tier '{}'", tier);
    } else {
        println!(
            "Found {} snapshot{} for tier '{}' (newest last):",
            snapshots.len(),
            if snapshots.len() == 1 { "" } else { "s" },
            tier
        );
        for snapshot in snapshots {
            if let Some(name) = snapshot.file_name() {
                println!("  {}", name.to_string_lossy());
            }
        }
    }

    Ok(())
}

/// Create backup snapshots now for every readable tier discovered from `cwd`.
///
/// For each tier, copies the files directly inside the tier's `.cascade/`
/// directory (symlink targets included, so resolved CLAUDE.md/AGENTS.md
/// content is captured) into `{backup_root}/{TIER}/snapshot-<UTC timestamp>/`.
/// Sub-directories (index caches, memory corpora, ...) are not copied.
async fn run_snapshot(cwd: Option<PathBuf>, backup_root: Option<PathBuf>) -> Result<()> {
    let cwd = match cwd {
        Some(c) => c,
        None => std::env::current_dir()
            .map_err(|e| CascadeError::Other(format!("cannot determine current dir: {e}")))?,
    };
    let backup_root = resolve_backup_root(backup_root)?;

    let tiers = cascade_core::discovery::TierDiscovery::new().discover(&cwd)?;
    let readable: Vec<_> = tiers.iter().filter(|t| t.is_readable()).collect();

    if readable.is_empty() {
        println!(
            "no readable cascade tiers found from {} — nothing to back up",
            cwd.display()
        );
        return Ok(());
    }

    let ts = chrono::Utc::now().format("%Y%m%d-%H%M%S");
    for tier in readable {
        let tier_name = tier.tier.acronym().to_uppercase();
        let snap_dir = backup_root.join(&tier_name).join(format!("snapshot-{ts}"));
        std::fs::create_dir_all(&snap_dir).map_err(|e| CascadeError::Io {
            path: snap_dir.clone(),
            operation: "create snapshot dir",
            source: e,
        })?;

        let mut copied = 0usize;
        let entries = std::fs::read_dir(&tier.cascade_dir).map_err(|e| CascadeError::Io {
            path: tier.cascade_dir.clone(),
            operation: "read tier dir",
            source: e,
        })?;
        for entry in entries.flatten() {
            let path = entry.path();
            // is_file() follows symlinks; fs::copy copies the target content.
            if path.is_file() {
                let dest = snap_dir.join(entry.file_name());
                std::fs::copy(&path, &dest).map_err(|e| CascadeError::Io {
                    path: path.clone(),
                    operation: "copy into snapshot",
                    source: e,
                })?;
                copied += 1;
            }
        }
        println!(
            "tier {}: {} file{} copied to {}",
            tier_name,
            copied,
            if copied == 1 { "" } else { "s" },
            snap_dir.display()
        );
    }

    Ok(())
}

/// Write the backup schedule to `~/.cascade/config.toml` and register the real
/// daemon-side trigger.
///
/// Sets `backup.schedule = "daily"|"weekly"|"hourly"` in config.toml, then
/// upserts a task row named [`BACKUP_TASK_NAME`] in `~/.cascade/scheduler.db`
/// — the daemon scheduler's persistent store — whose command is this very
/// binary running `backup run --cwd <cwd>`.  Re-running with a different
/// interval replaces the previous registration.
async fn set_schedule(interval: &ScheduleInterval) -> Result<()> {
    let home = std::env::var("HOME")
        .map(PathBuf::from)
        .map_err(|_| CascadeError::Other("cannot determine home directory".into()))?;
    let cwd = std::env::current_dir().unwrap_or_else(|_| PathBuf::from("."));

    set_schedule_for_home(&home, interval, &cwd)
}

/// Home-injectable core of [`set_schedule`] (unit-testable without env races).
fn set_schedule_for_home(
    home: &std::path::Path,
    interval: &ScheduleInterval,
    cwd: &std::path::Path,
) -> Result<()> {
    let interval_str = match interval {
        ScheduleInterval::Daily => "daily",
        ScheduleInterval::Weekly => "weekly",
        ScheduleInterval::Hourly => "hourly",
    };

    let config_path = home.join(".cascade").join("config.toml");

    // Read existing config or start fresh.
    let raw = if config_path.exists() {
        std::fs::read_to_string(&config_path).map_err(|e| CascadeError::Io {
            path: config_path.clone(),
            operation: "read config.toml",
            source: e,
        })?
    } else {
        String::new()
    };

    // Parse as TOML and set backup.schedule.
    let mut doc: toml_edit::DocumentMut = raw
        .parse()
        .map_err(|e| CascadeError::Other(format!("parse config.toml: {e}")))?;

    // Ensure `backup` is a regular table (not an inline table) so the file
    // renders as a `[backup]` header, matching the rest of config.toml style.
    let backup_entry = doc
        .entry("backup")
        .or_insert(toml_edit::Item::Table(toml_edit::Table::new()));
    let backup_table = match backup_entry.as_table_mut() {
        Some(t) => t,
        None => {
            // Existing value was an inline table or other non-table value;
            // convert to a regular table preserving the schedule key only.
            let mut t = toml_edit::Table::new();
            if let Some(s) = backup_entry.as_value() {
                t["schedule"] = toml_edit::value(s.clone());
            }
            *backup_entry = toml_edit::Item::Table(t);
            backup_entry.as_table_mut().expect("just inserted table")
        }
    };
    backup_table["schedule"] = toml_edit::value(interval_str);

    // Ensure parent dir exists.
    if let Some(parent) = config_path.parent() {
        std::fs::create_dir_all(parent).map_err(|e| CascadeError::Io {
            path: parent.to_path_buf(),
            operation: "create .cascade dir",
            source: e,
        })?;
    }

    std::fs::write(&config_path, doc.to_string()).map_err(|e| CascadeError::Io {
        path: config_path.clone(),
        operation: "write config.toml",
        source: e,
    })?;

    // Register the real trigger in the daemon scheduler's persistent store.
    let scheduler_db = home.join(".cascade").join("scheduler.db");
    register_backup_task(&scheduler_db, interval, cwd)?;

    println!(
        "Backup schedule set to '{}' in {}",
        interval_str,
        config_path.display()
    );
    println!(
        "Scheduled task '{}' registered in {} — the daemon runs \
         `cascade backup run --cwd {}` every {}.",
        BACKUP_TASK_NAME,
        scheduler_db.display(),
        cwd.display(),
        interval_str
    );
    println!("Ensure the daemon is running (`cascade daemon start`) for backups to fire.");

    Ok(())
}

/// Upsert the backup task row in the daemon scheduler's SQLite store.
///
/// Opens (creating if needed) `scheduler.db` with the same schema the daemon's
/// supervisor creates, removes any prior [`BACKUP_TASK_NAME`] row so
/// re-scheduling is idempotent, and inserts a fresh task with
/// `next_run = now + interval` (no surprise immediate run).
fn register_backup_task(
    scheduler_db: &std::path::Path,
    interval: &ScheduleInterval,
    cwd: &std::path::Path,
) -> Result<()> {
    use cascade_core::task_store::TaskStore;
    use cascade_types::scheduled_task::{ScheduleSpec, ScheduledTask};

    let conn = cascade_db::open_configured(scheduler_db)
        .map_err(|e| CascadeError::Other(format!("open {}: {e}", scheduler_db.display())))?;
    // Same DDL the daemon supervisor applies — keeps the CLI usable before
    // the daemon has ever started.
    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS scheduled_tasks (
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
        );",
    )
    .map_err(|e| CascadeError::Other(format!("create scheduled_tasks: {e}")))?;

    let store = TaskStore::new(Arc::new(Mutex::new(conn)));

    // Idempotent re-schedule: drop any previous registration first.
    for existing in store.list()? {
        if existing.name == BACKUP_TASK_NAME {
            store.delete(existing.id)?;
        }
    }

    let exe = std::env::current_exe()
        .map_err(|e| CascadeError::Other(format!("resolve cascade binary: {e}")))?;
    let secs = interval_secs(interval);
    let mut task = ScheduledTask::new(
        BACKUP_TASK_NAME,
        ScheduleSpec::Interval { secs },
        exe.display().to_string(),
        vec![
            "backup".into(),
            "run".into(),
            "--cwd".into(),
            cwd.display().to_string(),
        ],
    );
    task.next_run = Some(chrono::Utc::now() + chrono::Duration::seconds(secs as i64));
    store.insert(&task)?;

    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn backup_list_command_parses() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: BackupSubcommand,
        }

        let args = vec!["backup", "list", "GCI"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            BackupSubcommand::List { tier, .. } => {
                assert_eq!(tier, "GCI");
            }
            _ => panic!("expected List variant"),
        }
    }

    #[test]
    fn backup_schedule_command_parses() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: BackupSubcommand,
        }

        let args = vec!["backup", "schedule", "daily"];
        let cli = Cli::try_parse_from(args).unwrap();
        match cli.cmd {
            BackupSubcommand::Schedule { interval } => {
                assert!(matches!(interval, ScheduleInterval::Daily));
            }
            _ => panic!("expected Schedule variant"),
        }
    }

    #[test]
    fn backup_run_command_parses() {
        use clap::Parser;

        #[derive(Parser)]
        struct Cli {
            #[command(subcommand)]
            cmd: BackupSubcommand,
        }

        let cli = Cli::try_parse_from(vec!["backup", "run", "--cwd", "/tmp/x"]).unwrap();
        match cli.cmd {
            BackupSubcommand::Run { cwd, backup_root } => {
                assert_eq!(cwd, Some(PathBuf::from("/tmp/x")));
                assert!(backup_root.is_none());
            }
            _ => panic!("expected Run variant"),
        }
    }

    /// set_schedule_for_home must write config.toml AND register exactly one
    /// enabled task named `cascade-backup` in scheduler.db, with the interval
    /// cadence and a future next_run.  Re-scheduling replaces, not duplicates.
    #[test]
    fn set_schedule_registers_real_daemon_task() {
        let tmp = tempfile::tempdir().expect("tmpdir");
        let home = tmp.path();
        let cwd = home.join("project");
        std::fs::create_dir_all(&cwd).unwrap();

        set_schedule_for_home(home, &ScheduleInterval::Hourly, &cwd).expect("schedule set");

        // config.toml updated.
        let cfg = std::fs::read_to_string(home.join(".cascade").join("config.toml")).unwrap();
        assert!(cfg.contains("[backup]"), "config must have [backup]: {cfg}");
        assert!(cfg.contains("schedule = \"hourly\""), "cfg: {cfg}");

        // Task registered in scheduler.db.
        let read_tasks = || {
            use cascade_core::task_store::TaskStore;
            let conn =
                cascade_db::open_configured(home.join(".cascade").join("scheduler.db")).unwrap();
            TaskStore::new(Arc::new(Mutex::new(conn))).list().unwrap()
        };
        let tasks = read_tasks();
        assert_eq!(tasks.len(), 1, "exactly one task, got {tasks:?}");
        let t = &tasks[0];
        assert_eq!(t.name, BACKUP_TASK_NAME);
        assert!(t.enabled);
        assert!(matches!(
            t.schedule,
            cascade_types::scheduled_task::ScheduleSpec::Interval { secs: 3600 }
        ));
        assert_eq!(
            t.args,
            vec!["backup", "run", "--cwd", cwd.to_str().unwrap()]
        );
        assert!(t.next_run.unwrap() > chrono::Utc::now());

        // Re-schedule at a different interval: replaces, not duplicates.
        set_schedule_for_home(home, &ScheduleInterval::Daily, &cwd).expect("re-schedule");
        let tasks = read_tasks();
        assert_eq!(tasks.len(), 1, "re-schedule must replace, got {tasks:?}");
        assert!(matches!(
            tasks[0].schedule,
            cascade_types::scheduled_task::ScheduleSpec::Interval { secs: 86400 }
        ));
    }

    /// run_snapshot must copy a discovered tier's files (symlink targets
    /// included) into {backup_root}/{TIER}/snapshot-*/.
    #[tokio::test]
    async fn run_snapshot_copies_tier_files() {
        let tmp = tempfile::tempdir().expect("tmpdir");
        let cascade_dir = tmp.path().join(".cascade");
        std::fs::create_dir_all(&cascade_dir).unwrap();
        std::fs::write(cascade_dir.join("CASCADE.md"), "# cascade\n").unwrap();
        // Sibling symlink whose target content must be captured.
        let target = tmp.path().join("real-CLAUDE.md");
        std::fs::write(&target, "# resolved instructions\n").unwrap();
        #[cfg(unix)]
        std::os::unix::fs::symlink(&target, cascade_dir.join("CLAUDE.md")).unwrap();

        let backup_root = tmp.path().join("backups");
        run_snapshot(Some(tmp.path().to_path_buf()), Some(backup_root.clone()))
            .await
            .expect("run_snapshot");

        // Find the snapshot dir under any tier name (classification depends on
        // the temp path location — a project-level tier here).
        let mut found: Vec<std::path::PathBuf> = Vec::new();
        for tier_dir in std::fs::read_dir(&backup_root).unwrap().flatten() {
            for snap in std::fs::read_dir(tier_dir.path()).unwrap().flatten() {
                found.push(snap.path());
            }
        }
        assert_eq!(found.len(), 1, "exactly one snapshot dir, got {found:?}");
        let snap = &found[0];
        assert!(snap
            .file_name()
            .unwrap()
            .to_str()
            .unwrap()
            .starts_with("snapshot-"));
        let copied = std::fs::read_to_string(snap.join("CASCADE.md")).unwrap();
        assert_eq!(copied, "# cascade\n");
        #[cfg(unix)]
        {
            let resolved = std::fs::read_to_string(snap.join("CLAUDE.md")).unwrap();
            assert_eq!(resolved, "# resolved instructions\n");
        }
    }
}
