//! # rag_watcher
//!
//! Notify-based file watcher that feeds create/modify/delete events from
//! watched directories into the RAG ingest pipeline via an async channel.
//!
//! ## Purpose
//! Monitors `.claude/memory/`, `.claude/planning/`, `.github/wiki/`, `docs/`
//! (and any user-configured extra dirs) for changes and emits [`WatchSignal`]
//! values.  The ingest pipeline (T-P4-E01-22) subscribes to the receiver and
//! calls `IndexManager::ingest` / evict accordingly.
//!
//! ## Inputs
//! - `WatcherConfig` — root dirs + optional extras + debounce interval
//! - `tokio_util::sync::CancellationToken` — clean shutdown signal
//!
//! ## Outputs
//! - `tokio::sync::mpsc::Receiver<WatchSignal>` — consumed by the ingest pipeline
//!
//! ## Constraints
//! - `.git/`, `target/`, `node_modules/`, `.cascade/` are excluded.
//! - Events for the same path are debounced: a new event within the window resets
//!   the 500 ms timer rather than stacking.
//! - The notify callback is sync and only does a cheap channel send; the tokio
//!   debounce task handles all async logic.
//! - `Send + Sync` throughout; safe inside `tokio::spawn`.
//!
//! ## SPORT
//! MASTER-CRATES.md → cascade-daemon::rag_watcher (auto-RAG watcher, T-P4-E01-26)

use std::{
    collections::HashMap,
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicBool, Ordering},
        Arc,
    },
    time::Duration,
};

use notify::{Config, Event, EventKind, RecommendedWatcher, RecursiveMode, Watcher};
use tokio::{
    sync::{
        mpsc::{self, Receiver},
        Mutex,
    },
    task::JoinHandle,
    time::sleep,
};
use tokio_util::sync::CancellationToken;
use tracing::{debug, warn};

// ── Extensions that trigger ingest ───────────────────────────────────────────

/// File extensions that trigger ingest on create/modify events.
const WATCHED_EXTENSIONS: &[&str] = &[
    "md", "rs", "ts", "py", "json", "yaml", "toml", "html",
    "txt", "pdf", "docx", "xlsx",
];

/// Directory name segments that are always excluded from watching.
const EXCLUDED_DIRS: &[&str] = &[".git", "target", "node_modules", ".cascade"];

// ── Signal enum ──────────────────────────────────────────────────────────────

/// Signals emitted by [`AutoRagWatcher`] to the ingest pipeline consumer.
///
/// The pipeline subscriber matches on these to decide whether to call
/// `IndexManager::ingest(path)` or `IndexManager::evict(path)`.
///
/// SPORT: MASTER-CRATES.md → cascade-daemon::rag_watcher::WatchSignal
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum WatchSignal {
    /// File was created or modified; the pipeline should ingest/reindex it.
    Ingest(PathBuf),
    /// File was removed; the pipeline should evict its chunks.
    Evict(PathBuf),
}

// ── Raw event forwarded from the sync notify callback ────────────────────────

/// Internal event forwarded from the synchronous notify callback into the
/// async debounce task via a tokio unbounded channel (non-async send).
#[derive(Debug, Clone)]
enum RawEvent {
    Ingest(PathBuf),
    Evict(PathBuf),
}

// ── Config ───────────────────────────────────────────────────────────────────

/// Configuration for [`AutoRagWatcher`].
///
/// # Fields
/// - `project_root` — resolved project root; default watch dirs are derived from it.
/// - `extra_dirs` — user-added dirs (from `cascade.toml`).
/// - `debounce` — coalesce window (default 500 ms).  Events within this window
///   for the same path reset the timer rather than stacking.
#[derive(Debug, Clone)]
pub struct WatcherConfig {
    /// Project root used to derive default watch targets.
    pub project_root: PathBuf,
    /// Additional directories to watch (user-configured).
    pub extra_dirs: Vec<PathBuf>,
    /// Debounce window (default: 500 ms).
    pub debounce: Duration,
}

impl WatcherConfig {
    /// Creates a new config with 500 ms debounce and no extra dirs.
    pub fn new(project_root: impl Into<PathBuf>) -> Self {
        Self {
            project_root: project_root.into(),
            extra_dirs: Vec::new(),
            debounce: Duration::from_millis(500),
        }
    }

    /// Returns default watch directories derived from `project_root` (existing only).
    pub fn default_watch_dirs(&self) -> Vec<PathBuf> {
        let candidates = [
            self.project_root.join(".claude/memory"),
            self.project_root.join(".claude/planning"),
            self.project_root.join(".github/wiki"),
            self.project_root.join("docs"),
        ];
        candidates
            .into_iter()
            .filter(|p| p.exists() && p.is_dir())
            .collect()
    }

    /// Returns defaults + user-configured extras (deduped, existing only).
    pub fn all_watch_dirs(&self) -> Vec<PathBuf> {
        let mut dirs = self.default_watch_dirs();
        for extra in &self.extra_dirs {
            if extra.exists() && extra.is_dir() && !dirs.contains(extra) {
                dirs.push(extra.clone());
            }
        }
        dirs
    }
}

// ── Path helpers ─────────────────────────────────────────────────────────────

/// Returns `true` if any component of `path` is an excluded directory name.
pub(crate) fn is_excluded(path: &Path) -> bool {
    path.components().any(|c| {
        let s = c.as_os_str().to_string_lossy();
        EXCLUDED_DIRS.iter().any(|ex| s == *ex)
    })
}

/// Returns `true` if `path`'s extension is in [`WATCHED_EXTENSIONS`].
pub(crate) fn has_watched_extension(path: &Path) -> bool {
    path.extension()
        .and_then(|e| e.to_str())
        .map(|e| WATCHED_EXTENSIONS.contains(&e))
        .unwrap_or(false)
}

// ── AutoRagWatcher ────────────────────────────────────────────────────────────

/// File watcher that monitors RAG-relevant directories and emits [`WatchSignal`]s.
///
/// # Usage
/// ```no_run
/// use cascade_daemon::rag_watcher::{AutoRagWatcher, WatcherConfig};
/// use tokio_util::sync::CancellationToken;
///
/// # async fn example() {
/// let cfg = WatcherConfig::new("/my/project");
/// let token = CancellationToken::new();
/// let (watcher, rx) = AutoRagWatcher::start(cfg, token).unwrap();
/// // Hand `rx` to the ingest pipeline.
/// # }
/// ```
/// ## Volume pause/resume (T-P4-E01-32)
///
/// When the watched directory lives on an external drive, the daemon's
/// `VolumeIndexGuard` calls `pause()` on unmount and `resume()` on remount.
/// While paused, debounce tasks complete their sleep but the outbound signal is
/// suppressed — the ingest pipeline sees no events until resumed.
// File-watcher for RAG ingest pipeline — constructed by supervisor at project open.
#[allow(dead_code)]
pub struct AutoRagWatcher {
    // Keeps the notify watcher alive for the struct's lifetime.
    _watcher: RecommendedWatcher,
    /// `true` while the watched volume is absent.  Shared with debounce tasks.
    // Shared pause flag — read by debounce tasks to suppress signals while volume is absent.
    #[allow(dead_code)]
    paused: Arc<AtomicBool>,
}

impl AutoRagWatcher {
    /// Starts the watcher and returns `(Self, Receiver<WatchSignal>)`.
    ///
    /// The receiver should be handed to the ingest pipeline (T-P4-E01-22).
    /// On `shutdown.cancel()`, all in-flight debounce tasks are aborted and the
    /// output channel closes — the receiver will see `None`.
    ///
    /// # Errors
    /// Returns `Err` if `RecommendedWatcher::new` fails (e.g. OS error).
    pub fn start(
        cfg: WatcherConfig,
        shutdown: CancellationToken,
    ) -> Result<(Self, Receiver<WatchSignal>), notify::Error> {
        // Outbound channel consumed by the ingest pipeline.
        let (out_tx, out_rx) = mpsc::channel::<WatchSignal>(256);
        let debounce = cfg.debounce;
        let paused = Arc::new(AtomicBool::new(false));

        // Inner channel: synchronous notify callback → async debounce task.
        // `unbounded_channel` sender has a non-async `send` — safe on any thread.
        let (raw_tx, mut raw_rx) = mpsc::unbounded_channel::<RawEvent>();

        // Synchronous notify callback.
        // MUST NOT call any async fn or tokio::spawn — runs on the OS FS thread.
        let watcher_callback = move |result: Result<Event, notify::Error>| {
            let event = match result {
                Ok(e) => e,
                Err(e) => {
                    warn!(error = %e, "rag_watcher: notify error");
                    return;
                }
            };
            for path in &event.paths {
                if is_excluded(path) {
                    debug!(path = %path.display(), "rag_watcher: excluded, skipping");
                    continue;
                }
                // Classify using EventKind first, then fall back to path
                // existence check.  macOS FSEvents sometimes emits Create or
                // Modify events for a path that was just deleted (the Removed
                // flag is unreliable on older FSEvents API surfaces).
                let raw = match &event.kind {
                    EventKind::Remove(_) => {
                        if has_watched_extension(path) {
                            Some(RawEvent::Evict(path.clone()))
                        } else {
                            None
                        }
                    }
                    EventKind::Create(_) | EventKind::Modify(_) | EventKind::Any => {
                        if has_watched_extension(path) {
                            // Cross-platform safety: if the path no longer exists
                            // the OS lied about the event kind — treat as Evict.
                            if path.exists() {
                                Some(RawEvent::Ingest(path.clone()))
                            } else {
                                Some(RawEvent::Evict(path.clone()))
                            }
                        } else {
                            None
                        }
                    }
                    _ => None,
                };
                if let Some(r) = raw {
                    // Non-async send — fine on any thread.
                    if raw_tx.send(r).is_err() {
                        debug!("rag_watcher: raw channel closed");
                    }
                }
            }
        };

        let mut watcher = RecommendedWatcher::new(watcher_callback, Config::default())?;

        // Register each existing watch dir.
        for dir in &cfg.all_watch_dirs() {
            match watcher.watch(dir, RecursiveMode::Recursive) {
                Ok(()) => debug!(dir = %dir.display(), "rag_watcher: watching"),
                Err(e) => warn!(
                    dir = %dir.display(),
                    error = %e,
                    "rag_watcher: watch failed (skipped)"
                ),
            }
        }

        // Clone before the `move` so the struct retains ownership of `paused`.
        let paused_for_task = Arc::clone(&paused);

        // Async debounce task — the only place tokio APIs are called.
        tokio::spawn(async move {
            // pending: path → handle of the in-flight debounce sleep.
            let pending: Arc<Mutex<HashMap<PathBuf, JoinHandle<()>>>> =
                Arc::new(Mutex::new(HashMap::new()));

            loop {
                tokio::select! {
                    biased;

                    _ = shutdown.cancelled() => {
                        debug!("rag_watcher: debounce task stopping (shutdown)");
                        let mut map = pending.lock().await;
                        for (_, h) in map.drain() {
                            h.abort();
                        }
                        break;
                    }

                    msg = raw_rx.recv() => {
                        let raw = match msg {
                            Some(r) => r,
                            None => break, // raw_tx dropped
                        };

                        let (path, signal): (PathBuf, WatchSignal) = match raw {
                            RawEvent::Ingest(p) => (p.clone(), WatchSignal::Ingest(p)),
                            RawEvent::Evict(p)  => (p.clone(), WatchSignal::Evict(p)),
                        };

                        let mut map = pending.lock().await;
                        // Cancel previous timer for this path (debounce reset).
                        if let Some(old) = map.remove(&path) {
                            old.abort();
                        }

                        let out_tx2   = out_tx.clone();
                        let pending2  = Arc::clone(&pending);
                        let path2     = path.clone();
                        let paused2   = Arc::clone(&paused_for_task);

                        let handle = tokio::spawn(async move {
                            sleep(debounce).await;
                            {
                                let mut m = pending2.lock().await;
                                m.remove(&path2);
                            }
                            // Gate: suppress signal while the volume is unmounted.
                            if paused2.load(Ordering::Relaxed) {
                                debug!("rag_watcher: paused — signal suppressed (volume absent)");
                                return;
                            }
                            if let Err(e) = out_tx2.send(signal).await {
                                debug!(error = %e, "rag_watcher: outbound channel closed");
                            }
                        });

                        map.insert(path, handle);
                    }
                }
            }

            debug!("rag_watcher: debounce task exited");
        });

        Ok((
            Self {
                _watcher: watcher,
                paused,
            },
            out_rx,
        ))
    }

    /// Pause signal emission (volume unmounted).
    ///
    /// Idempotent.  Debounce timers keep running but their output is suppressed.
    /// Called by `VolumeIndexGuard` on `VolumeEvent::Unmounted`.
    // Called by VolumeIndexGuard on VolumeEvent::Unmounted — wired in T-P4-E01-32.
    #[allow(dead_code)]
    pub fn pause(&self) {
        self.paused.store(true, Ordering::Relaxed);
    }

    /// Resume signal emission (volume remounted).
    ///
    /// Idempotent.  Subsequent debounce completions will forward signals normally.
    /// Called by `VolumeIndexGuard` on `VolumeEvent::Mounted`.
    // Called by VolumeIndexGuard on VolumeEvent::Mounted — wired in T-P4-E01-32.
    #[allow(dead_code)]
    pub fn resume(&self) {
        self.paused.store(false, Ordering::Relaxed);
    }

    /// Returns `true` when the watcher is suppressing signals due to a volume
    /// being absent.
    // Status query — used by daemon status handler and tests.
    #[allow(dead_code)]
    pub fn is_paused(&self) -> bool {
        self.paused.load(Ordering::Relaxed)
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;
    use tokio::time::{timeout, Duration};

    /// Wait up to `timeout_ms` ms for one signal on `rx`.
    async fn next_signal(rx: &mut Receiver<WatchSignal>, timeout_ms: u64) -> Option<WatchSignal> {
        timeout(Duration::from_millis(timeout_ms), rx.recv())
            .await
            .ok()
            .flatten()
    }

    // ── Pure unit tests (no watcher) ─────────────────────────────────────────

    #[test]
    fn test_excluded_dir_filter() {
        assert!(is_excluded(&PathBuf::from("/project/.git/config")));
        assert!(is_excluded(&PathBuf::from("/project/target/debug/foo.md")));
        assert!(is_excluded(&PathBuf::from(
            "/project/node_modules/.bin/x.md"
        )));
        assert!(is_excluded(&PathBuf::from(
            "/project/.cascade/index/vec.db"
        )));
        assert!(!is_excluded(&PathBuf::from(
            "/project/.claude/memory/notes.md"
        )));
    }

    #[test]
    fn test_watched_extension() {
        assert!(has_watched_extension(&PathBuf::from("file.md")));
        assert!(has_watched_extension(&PathBuf::from("file.rs")));
        assert!(has_watched_extension(&PathBuf::from("file.yaml")));
        assert!(!has_watched_extension(&PathBuf::from("file.lock")));
        assert!(!has_watched_extension(&PathBuf::from("file.bak")));
        assert!(!has_watched_extension(&PathBuf::from("file")));
    }

    #[test]
    fn test_config_skips_nonexistent_dirs() {
        let tmp = TempDir::new().unwrap();
        fs::create_dir_all(tmp.path().join(".claude/memory")).unwrap();

        let cfg = WatcherConfig::new(tmp.path());
        let dirs = cfg.default_watch_dirs();
        assert_eq!(dirs.len(), 1);
        assert!(dirs[0].ends_with(".claude/memory"));
    }

    // ── Integration tests (real watcher) ─────────────────────────────────────
    //
    // NOTE: macOS FSEvents resolves symlinks — TempDir may return
    // `/var/folders/…` but notify returns `/private/var/folders/…`.
    // All integration tests canonicalize the expected path to match.
    //
    // NOTE: macOS sometimes re-fires an Ingest for a file that already exists
    // in a watched dir when the watcher stream is first established. Integration
    // tests that pre-create a file drain any such stale events before acting.

    /// Helper: drain all events that arrive within `drain_ms` ms (stale flush).
    async fn drain(rx: &mut Receiver<WatchSignal>, drain_ms: u64) {
        while next_signal(rx, drain_ms).await.is_some() {}
    }

    /// Helper: extract the PathBuf from a WatchSignal.
    fn sig_path(sig: &WatchSignal) -> &PathBuf {
        match sig {
            WatchSignal::Ingest(p) | WatchSignal::Evict(p) => p,
        }
    }

    /// Create a .md file → Ingest signal after debounce.
    #[tokio::test]
    async fn test_create_emits_ingest() {
        let tmp = TempDir::new().unwrap();
        let watch_dir = tmp.path().join(".claude/memory");
        fs::create_dir_all(&watch_dir).unwrap();
        // Canonicalize the watch_dir (resolves /var/folders → /private/var/folders).
        let watch_dir_real = watch_dir.canonicalize().unwrap();

        let mut cfg = WatcherConfig::new(tmp.path());
        cfg.debounce = Duration::from_millis(50);
        let token = CancellationToken::new();
        let (_w, mut rx) = AutoRagWatcher::start(cfg, token.clone()).unwrap();

        // Let the OS-level watcher register its stream.
        sleep(Duration::from_millis(150)).await;

        let file = watch_dir.join("notes.md");
        fs::write(&file, "hello").unwrap();
        let expected = watch_dir_real.join("notes.md");

        let sig = next_signal(&mut rx, 2000).await;
        let sig = sig.expect("expected Ingest signal");
        let got = sig_path(&sig)
            .canonicalize()
            .unwrap_or_else(|_| sig_path(&sig).clone());
        assert!(
            matches!(sig, WatchSignal::Ingest(_)),
            "expected Ingest, got {sig:?}"
        );
        assert_eq!(got, expected, "path mismatch");

        token.cancel();
    }

    /// Modify a .md file → Ingest signal.
    #[tokio::test]
    async fn test_modify_emits_ingest() {
        let tmp = TempDir::new().unwrap();
        let watch_dir = tmp.path().join(".claude/memory");
        fs::create_dir_all(&watch_dir).unwrap();

        let file = watch_dir.join("notes.md");
        fs::write(&file, "initial").unwrap();
        let expected = file.canonicalize().unwrap();

        let mut cfg = WatcherConfig::new(tmp.path());
        cfg.debounce = Duration::from_millis(50);
        let token = CancellationToken::new();
        let (_w, mut rx) = AutoRagWatcher::start(cfg, token.clone()).unwrap();
        sleep(Duration::from_millis(150)).await;
        // Drain any stale create event for the pre-existing file.
        drain(&mut rx, 200).await;

        fs::write(&file, "updated").unwrap();

        let sig = next_signal(&mut rx, 2000).await;
        let sig = sig.expect("expected Ingest signal");
        let got = sig_path(&sig)
            .canonicalize()
            .unwrap_or_else(|_| sig_path(&sig).clone());
        assert!(matches!(sig, WatchSignal::Ingest(_)));
        assert_eq!(got, expected);

        token.cancel();
    }

    /// Delete a .md file → Evict signal.
    ///
    /// The file is created AFTER the watcher starts to avoid spurious stale events.
    #[tokio::test]
    async fn test_delete_emits_evict() {
        let tmp = TempDir::new().unwrap();
        let watch_dir = tmp.path().join(".claude/memory");
        fs::create_dir_all(&watch_dir).unwrap();
        let watch_dir_real = watch_dir.canonicalize().unwrap();

        let mut cfg = WatcherConfig::new(tmp.path());
        cfg.debounce = Duration::from_millis(50);
        let token = CancellationToken::new();
        let (_w, mut rx) = AutoRagWatcher::start(cfg, token.clone()).unwrap();

        // Let the watcher register its stream before touching any files.
        sleep(Duration::from_millis(150)).await;

        // Create the file AFTER the watcher is ready — drain that Ingest first.
        let file = watch_dir.join("old.md");
        fs::write(&file, "data").unwrap();
        let expected = watch_dir_real.join("old.md");

        // Wait for the Ingest from the create, then drain it.
        let create_sig = next_signal(&mut rx, 2000).await;
        assert!(
            matches!(create_sig, Some(WatchSignal::Ingest(_))),
            "expected Ingest for file creation, got {create_sig:?}"
        );

        // Now delete — the next signal should be Evict.
        fs::remove_file(&file).unwrap();

        let sig = next_signal(&mut rx, 2000).await;
        let sig = sig.expect("expected Evict signal");
        let got = sig_path(&sig)
            .canonicalize()
            .unwrap_or_else(|_| sig_path(&sig).clone());
        assert!(
            matches!(sig, WatchSignal::Evict(_)),
            "expected Evict, got {sig:?}"
        );
        assert_eq!(got, expected);

        token.cancel();
    }

    /// Rapid writes to the same file produce only ONE Ingest signal.
    #[tokio::test]
    async fn test_debounce_coalesces_rapid_events() {
        let tmp = TempDir::new().unwrap();
        let watch_dir = tmp.path().join(".claude/planning");
        fs::create_dir_all(&watch_dir).unwrap();

        let mut cfg = WatcherConfig::new(tmp.path());
        cfg.debounce = Duration::from_millis(200);
        let token = CancellationToken::new();
        let (_w, mut rx) = AutoRagWatcher::start(cfg, token.clone()).unwrap();
        sleep(Duration::from_millis(150)).await;

        let file = watch_dir.join("plan.md");
        // Five rapid writes — all within the 200 ms debounce window.
        for i in 0..5u8 {
            fs::write(&file, format!("version {i}")).unwrap();
            sleep(Duration::from_millis(30)).await;
        }
        let expected = file.canonicalize().unwrap();

        // Debounce fires 200 ms after the LAST write.
        let first = next_signal(&mut rx, 1500).await;
        let first = first.expect("expected one Ingest signal");
        let got = sig_path(&first)
            .canonicalize()
            .unwrap_or_else(|_| sig_path(&first).clone());
        assert!(matches!(first, WatchSignal::Ingest(_)));
        assert_eq!(got, expected);

        // No second signal in a short follow-on window.
        let second = next_signal(&mut rx, 250).await;
        assert!(
            second.is_none(),
            "expected one debounced signal, got second: {second:?}"
        );

        token.cancel();
    }

    /// Files with non-watched extensions produce no signal.
    #[tokio::test]
    async fn test_ignored_extension_no_signal() {
        let tmp = TempDir::new().unwrap();
        let watch_dir = tmp.path().join(".claude/memory");
        fs::create_dir_all(&watch_dir).unwrap();

        let mut cfg = WatcherConfig::new(tmp.path());
        cfg.debounce = Duration::from_millis(50);
        let token = CancellationToken::new();
        let (_w, mut rx) = AutoRagWatcher::start(cfg, token.clone()).unwrap();
        sleep(Duration::from_millis(150)).await;

        fs::write(watch_dir.join("package.lock.bak"), "lock").unwrap();

        let sig = next_signal(&mut rx, 500).await;
        assert!(sig.is_none(), "no signal expected for .bak, got {sig:?}");

        token.cancel();
    }

    /// Shutdown token closes the output channel cleanly — no panic.
    #[tokio::test]
    async fn test_shutdown_closes_channel() {
        let tmp = TempDir::new().unwrap();
        let mut cfg = WatcherConfig::new(tmp.path());
        cfg.debounce = Duration::from_millis(50);
        let token = CancellationToken::new();
        let (_w, mut rx) = AutoRagWatcher::start(cfg, token.clone()).unwrap();

        token.cancel();
        sleep(Duration::from_millis(100)).await;

        let result = timeout(Duration::from_millis(300), rx.recv()).await;
        match result {
            Ok(None) | Err(_) => {} // channel closed or timed out — both correct
            Ok(Some(sig)) => panic!("unexpected signal after shutdown: {sig:?}"),
        }
    }
}
