//! Volume mount/unmount watcher — pause and resume RAG indexing on drive events.
//!
//! Purpose: Detect when an external volume (e.g. /Volumes/X9) that contains a
//! configured RAG index root is mounted or unmounted, then emit `VolumeEvent`
//! on a Tokio broadcast channel so downstream consumers (IndexManager,
//! AutoRagWatcher) can pause/resume indexing without crashing or flooding logs
//! with I/O errors.
//!
//! Inputs:
//!   - `watched_roots`: list of paths that the daemon is indexing.
//!   - `poll_interval`: how often to diff the mount table (used on Linux and as
//!     the macOS fallback when the fsevents path is unavailable).
//!
//! Outputs:
//!   - `tokio::sync::broadcast::Receiver<VolumeEvent>` — consumers subscribe
//!     before the watcher task starts; capacity = 16 (bursts are unlikely).
//!
//! Constraints:
//!   - macOS: polls `/Volumes` directory on a configurable interval (default 2 s).
//!     DiskArbitration bindings are out of scope for P4; the `/Volumes`-poll
//!     approach is explicitly approved in the spec guide.
//!   - Linux: diffs `/proc/mounts` every `poll_interval`.
//!   - Windows: polls a synthetic list (real RegisterDeviceNotification is P5+).
//!   - All platforms: runs in a `tokio::task::spawn` green thread; never panics
//!     on I/O errors — logs WARN and continues.
//!   - Only volumes whose path is a **prefix** of at least one watched root
//!     trigger a `Mounted` / `Unmounted` event.
//!   - Daemon startup with the volume absent: logs WARN once; does NOT error or
//!     loop-spam.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, volume_watcher module
//!
//! T-P4-E01-32

use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::time::Duration;

use tokio::sync::broadcast;
use tokio_util::sync::CancellationToken;
use tracing::{info, warn};

// ── Public event type ─────────────────────────────────────────────────────────

/// Volume lifecycle events emitted by `VolumeWatcher`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum VolumeEvent {
    /// A volume appeared at `path` (mounted).
    Mounted(PathBuf),
    /// A volume disappeared from `path` (unmounted / ejected).
    Unmounted(PathBuf),
}

// ── MountTableProvider seam (injectable for tests) ────────────────────────────

/// Trait that abstracts reading the current list of mounted volume root paths.
///
/// Purpose: allows unit tests to inject a fake mount table without touching the
/// filesystem.
///
/// Constraints:
///   - Must be `Send + Sync` so it can be shared across async tasks.
///   - `list_mounts()` is synchronous; callers run it in a blocking context or
///     accept the brief syscall latency on the polling interval.
pub trait MountTableProvider: Send + Sync + 'static {
    /// Return the set of currently-mounted volume root paths.
    fn list_mounts(&self) -> HashSet<PathBuf>;
}

// ── Platform-native provider ──────────────────────────────────────────────────

/// Real mount table reader — platform-specific.
pub struct NativeMountProvider;

impl MountTableProvider for NativeMountProvider {
    fn list_mounts(&self) -> HashSet<PathBuf> {
        native_list_mounts()
    }
}

/// macOS: scan `/Volumes` for direct children (each is a mounted volume root).
#[cfg(target_os = "macos")]
fn native_list_mounts() -> HashSet<PathBuf> {
    let volumes = Path::new("/Volumes");
    let mut set = HashSet::new();
    // Always include the root filesystem.
    set.insert(PathBuf::from("/"));
    match std::fs::read_dir(volumes) {
        Ok(entries) => {
            for entry in entries.flatten() {
                set.insert(entry.path());
            }
        }
        Err(e) => {
            warn!(error = %e, "volume_watcher: failed to read /Volumes (macOS)");
        }
    }
    set
}

/// Linux: parse `/proc/mounts` — each line's second field is the mount point.
#[cfg(target_os = "linux")]
fn native_list_mounts() -> HashSet<PathBuf> {
    let mut set = HashSet::new();
    match std::fs::read_to_string("/proc/mounts") {
        Ok(content) => {
            for line in content.lines() {
                let mut fields = line.split_whitespace();
                let _ = fields.next(); // device
                if let Some(mount_point) = fields.next() {
                    set.insert(PathBuf::from(mount_point));
                }
            }
        }
        Err(e) => {
            warn!(error = %e, "volume_watcher: failed to read /proc/mounts (Linux)");
        }
    }
    set
}

/// Windows stub: returns only `C:\` and nothing else.
/// Full RegisterDeviceNotification support is deferred to P5.
#[cfg(target_os = "windows")]
fn native_list_mounts() -> HashSet<PathBuf> {
    let mut set = HashSet::new();
    set.insert(PathBuf::from("C:\\"));
    set
}

/// Fallback for any other OS.
#[cfg(not(any(target_os = "macos", target_os = "linux", target_os = "windows")))]
fn native_list_mounts() -> HashSet<PathBuf> {
    HashSet::new()
}

// ── VolumeWatcher ─────────────────────────────────────────────────────────────

/// Background task that polls the mount table and emits `VolumeEvent` on changes.
///
/// Only volumes that are a path **prefix** of at least one watched root are
/// surfaced; unrelated volume changes are silently ignored.
pub struct VolumeWatcher {
    watched_roots: Vec<PathBuf>,
    poll_interval: Duration,
    provider: Arc<dyn MountTableProvider>,
    tx: broadcast::Sender<VolumeEvent>,
}

impl VolumeWatcher {
    /// Default poll interval used by the daemon.
    pub const DEFAULT_POLL_INTERVAL: Duration = Duration::from_secs(2);

    /// Broadcast channel capacity.
    const CHANNEL_CAPACITY: usize = 16;

    /// Construct a watcher with the platform-native mount provider.
    ///
    /// Returns the watcher plus a broadcast receiver that already subscribes
    /// to all future events (caller may `.resubscribe()` to hand out more).
    pub fn new(
        watched_roots: Vec<PathBuf>,
        poll_interval: Duration,
    ) -> (Self, broadcast::Receiver<VolumeEvent>) {
        Self::with_provider(watched_roots, poll_interval, Arc::new(NativeMountProvider))
    }

    /// Construct with an injected provider (used in unit tests).
    pub fn with_provider(
        watched_roots: Vec<PathBuf>,
        poll_interval: Duration,
        provider: Arc<dyn MountTableProvider>,
    ) -> (Self, broadcast::Receiver<VolumeEvent>) {
        let (tx, rx) = broadcast::channel(Self::CHANNEL_CAPACITY);
        let watcher = VolumeWatcher {
            watched_roots,
            poll_interval,
            provider,
            tx,
        };
        (watcher, rx)
    }

    /// Spawn the polling loop as a Tokio task.
    ///
    /// The task exits cleanly when `shutdown` is cancelled.
    pub fn spawn(self, shutdown: CancellationToken) -> tokio::task::JoinHandle<()> {
        tokio::spawn(async move { self.run(shutdown).await })
    }

    /// Inner poll loop.
    async fn run(self, shutdown: CancellationToken) {
        // Snapshot the mount table at startup — volumes already present are NOT
        // emitted as Mounted events (we only track deltas going forward).
        let mut known = self.provider.list_mounts();

        // Warn once at startup for each watched root whose volume is not present.
        for root in &self.watched_roots {
            if volume_for(root, &known).is_none() {
                warn!(
                    path = %root.display(),
                    "volume_watcher: index root on absent volume at daemon start; \
                     indexing paused until volume appears"
                );
            }
        }

        let mut interval = tokio::time::interval(self.poll_interval);
        interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

        loop {
            tokio::select! {
                _ = interval.tick() => {}
                _ = shutdown.cancelled() => {
                    info!("volume_watcher: shutdown requested — exiting poll loop");
                    return;
                }
            }

            let current = self.provider.list_mounts();
            let added: Vec<PathBuf> = current.difference(&known).cloned().collect();
            let removed: Vec<PathBuf> = known.difference(&current).cloned().collect();

            for vol in removed {
                if self.affects_watched(&vol) {
                    warn!(
                        volume = %vol.display(),
                        "volume_watcher: volume unmounted — indexing paused"
                    );
                    let _ = self.tx.send(VolumeEvent::Unmounted(vol));
                }
            }
            for vol in added {
                if self.affects_watched(&vol) {
                    info!(
                        volume = %vol.display(),
                        "volume_watcher: volume remounted — indexing resumed"
                    );
                    let _ = self.tx.send(VolumeEvent::Mounted(vol));
                }
            }

            known = current;
        }
    }

    /// Returns true if `volume` is a prefix of at least one watched root.
    fn affects_watched(&self, volume: &Path) -> bool {
        self.watched_roots
            .iter()
            .any(|root| root.starts_with(volume))
    }
}

// ── Volume-prefix helper ──────────────────────────────────────────────────────

/// Return the longest mounted volume path that is a prefix of `path`, if any.
///
/// Used at startup to check whether a watched root's volume is currently mounted.
fn volume_for<'a>(path: &Path, mounts: &'a HashSet<PathBuf>) -> Option<&'a PathBuf> {
    mounts
        .iter()
        .filter(|m| path.starts_with(m.as_path()))
        .max_by_key(|m| m.components().count())
}

// ── IndexManager (T-P4-E01-25 real impl) ─────────────────────────────────────

/// Re-export the real `IndexManager` from `cascade-rag`.
///
/// The volume watcher subscribes to mount/unmount events and calls
/// `pause()` / `resume()` on this handle.  The full implementation
/// (DB path routing, tier-aware source registration, connection cache) lives in
/// `crates/cascade-rag/src/index_manager.rs`.
///
/// SPORT: MASTER-CRATES.md → cascade-daemon volume_watcher::IndexManager (re-export)
pub use cascade_rag::IndexManager;

/// Create a test-only `IndexManager` backed by a process-scoped temp directory.
///
/// Provides the same `Arc<IndexManager>` type expected by `VolumeIndexGuard`
/// without requiring a real project root.  Only for use in tests; production
/// code should call [`IndexManager::open`] with the actual project root.
#[allow(dead_code)]
pub(crate) async fn new_index_manager_for_test() -> Arc<IndexManager> {
    let tmp = std::env::temp_dir().join(format!(
        "cascade-index-stub-{}-{}",
        std::process::id(),
        // Use a counter so parallel tests get distinct dirs.
        {
            use std::sync::atomic::{AtomicU64, Ordering};
            static CTR: AtomicU64 = AtomicU64::new(0);
            CTR.fetch_add(1, Ordering::Relaxed)
        }
    ));
    IndexManager::open(&tmp)
        .await
        .expect("new_index_manager_for_test")
}

// ── VolumeIndexGuard — subscriber that wires VolumeEvent → IndexManager ──────

/// Subscribes to a `VolumeEvent` broadcast channel and calls
/// `IndexManager::pause/resume` on the appropriate events.
///
/// Purpose: decouples the VolumeWatcher (event emission) from the
/// IndexManager (reaction). Spawn one guard per IndexManager.
///
/// Constraints:
///   - Tracks which volumes it has paused via a `HashSet`; only resumes when
///     ALL previously-paused volumes have re-appeared.
///   - Watched roots are compared by prefix; only matching volumes trigger
///     state transitions.
///   - Never panics on a closed broadcast channel — exits the loop.
pub struct VolumeIndexGuard {
    watched_roots: Vec<PathBuf>,
    index_mgr: Arc<IndexManager>,
    paused_volumes: HashSet<PathBuf>,
}

impl VolumeIndexGuard {
    /// Construct the guard.
    pub fn new(watched_roots: Vec<PathBuf>, index_mgr: Arc<IndexManager>) -> Self {
        Self {
            watched_roots,
            index_mgr,
            paused_volumes: HashSet::new(),
        }
    }

    /// Spawn the guard as a Tokio task that drains `rx` until shutdown.
    pub fn spawn(
        mut self,
        mut rx: broadcast::Receiver<VolumeEvent>,
        shutdown: CancellationToken,
    ) -> tokio::task::JoinHandle<()> {
        tokio::spawn(async move {
            loop {
                tokio::select! {
                    event = rx.recv() => {
                        match event {
                            Ok(VolumeEvent::Unmounted(vol)) => {
                                if self.affects_watched(&vol) {
                                    self.paused_volumes.insert(vol.clone());
                                    self.index_mgr.pause().await;
                                    warn!(
                                        volume = %vol.display(),
                                        "VolumeIndexGuard: index root on unmounted volume; \
                                         indexing paused"
                                    );
                                }
                            }
                            Ok(VolumeEvent::Mounted(vol)) => {
                                if self.paused_volumes.remove(&vol)
                                    && self.paused_volumes.is_empty() {
                                        self.index_mgr.resume().await;
                                        info!(
                                            volume = %vol.display(),
                                            "VolumeIndexGuard: index root volume remounted; \
                                             indexing resumed"
                                        );
                                    }
                            }
                            Err(broadcast::error::RecvError::Lagged(n)) => {
                                warn!(skipped = n, "VolumeIndexGuard: broadcast lagged");
                            }
                            Err(broadcast::error::RecvError::Closed) => {
                                info!("VolumeIndexGuard: channel closed — exiting");
                                return;
                            }
                        }
                    }
                    _ = shutdown.cancelled() => {
                        info!("VolumeIndexGuard: shutdown — exiting");
                        return;
                    }
                }
            }
        })
    }

    fn affects_watched(&self, volume: &Path) -> bool {
        self.watched_roots
            .iter()
            .any(|root| root.starts_with(volume))
    }
}

// ── Per-volume state map (used by VolumeIndexGuard internally) ────────────────

/// Per-volume pause state — kept as a plain HashMap inside the guard.
/// Exported as a type alias so callers can name the map type.
pub type VolumeStateMap = HashMap<PathBuf, VolumeStatus>;

/// The indexing state for a single volume.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VolumeStatus {
    /// Volume is mounted; indexing is active.
    Active,
    /// Volume is unmounted; indexing is paused.
    Paused,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::Mutex as StdMutex;
    use tokio::time::timeout;

    // ── Fake mount provider ───────────────────────────────────────────────────

    /// Injectable mount table backed by a `Mutex<HashSet<PathBuf>>`.
    struct FakeMountProvider {
        mounts: Arc<StdMutex<HashSet<PathBuf>>>,
    }

    impl FakeMountProvider {
        fn new(
            initial: impl IntoIterator<Item = PathBuf>,
        ) -> (Self, Arc<StdMutex<HashSet<PathBuf>>>) {
            let mounts = Arc::new(StdMutex::new(initial.into_iter().collect()));
            let provider = Self {
                mounts: mounts.clone(),
            };
            (provider, mounts)
        }
    }

    impl MountTableProvider for FakeMountProvider {
        fn list_mounts(&self) -> HashSet<PathBuf> {
            self.mounts.lock().unwrap().clone()
        }
    }

    // ── Helpers ───────────────────────────────────────────────────────────────

    fn p(s: &str) -> PathBuf {
        PathBuf::from(s)
    }

    // ── VolumeWatcher unit tests ──────────────────────────────────────────────

    /// Unmounting a watched volume emits `VolumeEvent::Unmounted`.
    #[tokio::test]
    async fn test_unmount_emits_event() {
        let initial = vec![p("/Volumes/X9")];
        let watched = vec![p("/Volumes/X9/cascade-indexes/")];
        let (provider, mounts_ref) = FakeMountProvider::new(initial);

        let (watcher, mut rx) =
            VolumeWatcher::with_provider(watched, Duration::from_millis(10), Arc::new(provider));

        let shutdown = CancellationToken::new();
        watcher.spawn(shutdown.clone());

        // Give the watcher task one scheduler yield to take its initial snapshot
        // before we mutate the fake table.  Without this yield there is a race:
        // the remove could happen before `run()` calls `list_mounts()` the first
        // time, so the volume would never appear in `known` and Unmounted would
        // never fire.
        tokio::time::sleep(Duration::from_millis(25)).await;

        // Simulate unmount by removing the volume from the fake table.
        mounts_ref.lock().unwrap().remove(&p("/Volumes/X9"));

        let event = timeout(Duration::from_millis(500), rx.recv())
            .await
            .expect("timed out waiting for event")
            .expect("channel closed");

        assert_eq!(event, VolumeEvent::Unmounted(p("/Volumes/X9")));
        shutdown.cancel();
    }

    /// Mounting a previously-absent volume emits `VolumeEvent::Mounted`.
    #[tokio::test]
    async fn test_mount_emits_event() {
        // Start with no volumes so the watcher sees X9 as absent.
        let watched = vec![p("/Volumes/X9/cascade-indexes/")];
        let (provider, mounts_ref) = FakeMountProvider::new(vec![]);

        let (watcher, mut rx) =
            VolumeWatcher::with_provider(watched, Duration::from_millis(10), Arc::new(provider));

        let shutdown = CancellationToken::new();
        watcher.spawn(shutdown.clone());

        // Simulate mount after watcher has snapshotted the empty table.
        tokio::time::sleep(Duration::from_millis(25)).await;
        mounts_ref.lock().unwrap().insert(p("/Volumes/X9"));

        let event = timeout(Duration::from_millis(500), rx.recv())
            .await
            .expect("timed out waiting for event")
            .expect("channel closed");

        assert_eq!(event, VolumeEvent::Mounted(p("/Volumes/X9")));
        shutdown.cancel();
    }

    /// Volumes NOT a prefix of any watched root are silently ignored.
    #[tokio::test]
    async fn test_unrelated_volume_ignored() {
        let initial = vec![p("/Volumes/Other")];
        let watched = vec![p("/Volumes/X9/cascade-indexes/")];
        let (provider, mounts_ref) = FakeMountProvider::new(initial);

        let (watcher, mut rx) =
            VolumeWatcher::with_provider(watched, Duration::from_millis(10), Arc::new(provider));

        let shutdown = CancellationToken::new();
        watcher.spawn(shutdown.clone());

        // Remove the unrelated volume — should not trigger any event.
        mounts_ref.lock().unwrap().remove(&p("/Volumes/Other"));

        let result = timeout(Duration::from_millis(100), rx.recv()).await;
        assert!(result.is_err(), "expected no event for unrelated volume");

        shutdown.cancel();
    }

    // ── VolumeIndexGuard / IndexManager pause-resume tests ───────────────────

    /// Unmounted event → IndexManager pauses.
    #[tokio::test]
    async fn test_guard_pauses_on_unmount() {
        let index_mgr = new_index_manager_for_test().await;
        let watched = vec![p("/Volumes/X9/idx")];
        let guard = VolumeIndexGuard::new(watched, index_mgr.clone());

        let (tx, rx) = broadcast::channel::<VolumeEvent>(16);
        let shutdown = CancellationToken::new();
        let _handle = guard.spawn(rx, shutdown.clone());

        tx.send(VolumeEvent::Unmounted(p("/Volumes/X9"))).unwrap();

        // Give the task one tick to process.
        tokio::time::sleep(Duration::from_millis(20)).await;

        assert!(index_mgr.is_paused().await, "IndexManager should be paused");

        shutdown.cancel();
    }

    /// Mounted event → IndexManager resumes (after being paused by unmount).
    #[tokio::test]
    async fn test_guard_resumes_on_mount() {
        let index_mgr = new_index_manager_for_test().await;
        let watched = vec![p("/Volumes/X9/idx")];
        let guard = VolumeIndexGuard::new(watched, index_mgr.clone());

        let (tx, rx) = broadcast::channel::<VolumeEvent>(16);
        let shutdown = CancellationToken::new();
        let _handle = guard.spawn(rx, shutdown.clone());

        // First pause, then resume.
        tx.send(VolumeEvent::Unmounted(p("/Volumes/X9"))).unwrap();
        tokio::time::sleep(Duration::from_millis(20)).await;
        assert!(index_mgr.is_paused().await);

        tx.send(VolumeEvent::Mounted(p("/Volumes/X9"))).unwrap();
        tokio::time::sleep(Duration::from_millis(20)).await;

        assert!(
            !index_mgr.is_paused().await,
            "IndexManager should be resumed"
        );

        shutdown.cancel();
    }

    /// Unrelated volume mount/unmount events do not affect paused state.
    #[tokio::test]
    async fn test_guard_ignores_unrelated_volumes() {
        let index_mgr = new_index_manager_for_test().await;
        let watched = vec![p("/Volumes/X9/idx")];
        let guard = VolumeIndexGuard::new(watched, index_mgr.clone());

        let (tx, rx) = broadcast::channel::<VolumeEvent>(16);
        let shutdown = CancellationToken::new();
        let _handle = guard.spawn(rx, shutdown.clone());

        tx.send(VolumeEvent::Unmounted(p("/Volumes/Other")))
            .unwrap();
        tokio::time::sleep(Duration::from_millis(20)).await;

        assert!(
            !index_mgr.is_paused().await,
            "unrelated unmount must not pause"
        );

        shutdown.cancel();
    }

    /// IndexManager starts unpaused; pause/resume transitions are correct.
    #[tokio::test]
    async fn test_index_manager_state_machine() {
        let mgr = new_index_manager_for_test().await;
        assert!(!mgr.is_paused().await, "should start active");

        mgr.pause().await;
        assert!(mgr.is_paused().await);

        mgr.resume().await;
        assert!(!mgr.is_paused().await);

        // Idempotent pause.
        mgr.pause().await;
        mgr.pause().await;
        assert!(mgr.is_paused().await);

        // Idempotent resume.
        mgr.resume().await;
        mgr.resume().await;
        assert!(!mgr.is_paused().await);
    }

    /// Absent index root at startup: watcher does not panic, emits no events.
    #[tokio::test]
    async fn test_absent_root_at_startup_no_panic() {
        let watched = vec![p("/Volumes/NonExistent/cascade-indexes/")];
        let (provider, _) = FakeMountProvider::new(vec![]); // empty table

        let (watcher, mut rx) =
            VolumeWatcher::with_provider(watched, Duration::from_millis(10), Arc::new(provider));

        let shutdown = CancellationToken::new();
        watcher.spawn(shutdown.clone());

        // No events — volume was already absent at snapshot time.
        let result = timeout(Duration::from_millis(80), rx.recv()).await;
        assert!(
            result.is_err(),
            "no events expected on absent-at-startup volume"
        );

        shutdown.cancel();
    }

    /// `affects_watched` helper: prefix matching correctness.
    #[tokio::test]
    async fn test_affects_watched_prefix() {
        let (watcher, _rx) = VolumeWatcher::with_provider(
            vec![p("/Volumes/X9/idx/cascade")],
            Duration::from_millis(100),
            Arc::new(NativeMountProvider),
        );

        assert!(watcher.affects_watched(&p("/Volumes/X9")));
        assert!(watcher.affects_watched(&p("/Volumes/X9/idx")));
        assert!(!watcher.affects_watched(&p("/Volumes/X8")));
        assert!(!watcher.affects_watched(&p("/Volumes/X99")));
    }
}
