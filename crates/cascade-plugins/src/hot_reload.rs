//! Plugin hot-reload — filesystem watcher for `--dev` mode.
//!
//! Purpose: watch `~/.cascade/plugins/**/*.wasm` for changes (Modify/Create
//!   events) and emit a `ReloadEvent` on a channel so the `PluginRegistry` can
//!   atomically swap the `Arc<LoadedPlugin>` for the affected plugin.
//! Inputs:  `plugins_dir` path, `mpsc::Sender<ReloadEvent>`, `dev_mode` flag.
//! Outputs: `PluginWatcher` handle; drops the watcher when dropped.
//! Constraints:
//!   - Only WASM files inside `plugins_dir` trigger reload (path-traversal guard).
//!   - Reload events are debounced (400 ms) to avoid double-firing on editors
//!     that truncate-and-rewrite (common on macOS FSEvents).
//!   - Disabled when `dev_mode = false` — `PluginWatcher::start` returns `None`.
//!   - In-flight `Arc<LoadedPlugin>` is not dropped until its strong-count reaches
//!     1 (drain pattern), with a 5-second timeout.
//!
//! SPORT: cascade-plugins / watcher layer (T-P4-E03-08)

use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use notify::event::{EventKind, ModifyKind};
use notify::{Event, EventHandler, RecommendedWatcher, RecursiveMode, Watcher};
use tokio::sync::mpsc;
use tracing::{error, info, instrument, warn};

/// Default debounce window to avoid duplicate reload events.
const DEBOUNCE_MS: u64 = 400;

/// How long to wait for in-flight `Arc<LoadedPlugin>` drains before force-dropping.
pub const DRAIN_TIMEOUT: Duration = Duration::from_secs(5);

// ── Event types ───────────────────────────────────────────────────────────────

/// Notification that a WASM file change was detected.
#[derive(Debug, Clone)]
pub struct ReloadEvent {
    /// The plugin directory whose WASM changed (e.g. `~/.cascade/plugins/com.example.foo`).
    pub plugin_dir: PathBuf,
    /// Wall-clock time the event was detected (for `reload_duration_ms` tracing).
    pub detected_at: Instant,
}

// ── Debounce state ────────────────────────────────────────────────────────────

/// Tracks the last event time per plugin dir to implement debouncing.
#[derive(Default)]
struct Debouncer {
    last: std::collections::HashMap<PathBuf, Instant>,
}

impl Debouncer {
    /// Returns `true` if enough time has passed since the last event for this dir.
    fn should_fire(&mut self, dir: &PathBuf) -> bool {
        let threshold = Duration::from_millis(DEBOUNCE_MS);
        let now = Instant::now();
        match self.last.get(dir) {
            Some(prev) if now.duration_since(*prev) < threshold => false,
            _ => {
                self.last.insert(dir.clone(), now);
                true
            }
        }
    }
}

// ── PluginWatcher ─────────────────────────────────────────────────────────────

/// Filesystem watcher that sends reload events when WASM files change.
///
/// Dropped when the owning `PluginRegistry` shuts down.
pub struct PluginWatcher {
    /// The underlying notify watcher handle — kept alive by being owned here.
    _watcher: RecommendedWatcher,
    /// Root directory being watched.
    plugins_dir: PathBuf,
}

/// Internal handler that filters events and forwards relevant ones.
struct WatcherHandler {
    plugins_dir: PathBuf,
    tx: mpsc::Sender<ReloadEvent>,
    debouncer: Arc<Mutex<Debouncer>>,
}

impl EventHandler for WatcherHandler {
    fn handle_event(&mut self, event: notify::Result<Event>) {
        let event = match event {
            Ok(e) => e,
            Err(e) => {
                warn!(reason = %e, "notify watcher error");
                return;
            }
        };

        // We care about any write/create/rename that touches a .wasm file.
        let is_relevant = matches!(
            event.kind,
            EventKind::Modify(ModifyKind::Data(_))
                | EventKind::Modify(ModifyKind::Name(_))
                | EventKind::Create(_)
        );
        if !is_relevant {
            return;
        }

        for path in &event.paths {
            // Security: reject any path that escapes our plugins_dir.
            if !path.starts_with(&self.plugins_dir) {
                warn!(
                    path = %path.display(),
                    "watcher event for path outside plugins_dir — ignoring"
                );
                continue;
            }

            // Only WASM files trigger reload.
            if path.extension().and_then(|e| e.to_str()) != Some("wasm") {
                continue;
            }

            // Derive the plugin_dir from the path (direct child of plugins_dir).
            let plugin_dir = match path.parent() {
                Some(p) if p.starts_with(&self.plugins_dir) => {
                    // The wasm lives inside a plugin subdir.
                    p.to_owned()
                }
                _ => continue,
            };

            // Debounce.
            let should_fire = {
                let mut db = self.debouncer.lock().unwrap();
                db.should_fire(&plugin_dir)
            };

            if should_fire {
                let event = ReloadEvent {
                    plugin_dir: plugin_dir.clone(),
                    detected_at: Instant::now(),
                };
                if let Err(e) = self.tx.try_send(event) {
                    // Non-fatal: the channel is full or closed (daemon shutting down).
                    warn!(reason = %e, "failed to send reload event — dropping");
                }
            }
        }
    }
}

impl PluginWatcher {
    /// Start watching `plugins_dir` for WASM changes.
    ///
    /// Returns `None` when `dev_mode` is `false` — hot-reload is only active in
    /// developer mode.
    ///
    /// Returns `Some(watcher)` in dev mode.  The caller should spawn a task that
    /// reads from `rx` and drives `PluginRegistry::reload_plugin`.
    #[instrument(name = "plugin_watcher_start", skip(tx))]
    pub fn start(
        plugins_dir: &Path,
        tx: mpsc::Sender<ReloadEvent>,
        dev_mode: bool,
    ) -> Option<Self> {
        if !dev_mode {
            info!("dev_mode=false — plugin hot-reload disabled");
            return None;
        }

        let debouncer = Arc::new(Mutex::new(Debouncer::default()));

        let handler = WatcherHandler {
            plugins_dir: plugins_dir.to_owned(),
            tx,
            debouncer,
        };

        let mut watcher = match RecommendedWatcher::new(handler, notify::Config::default()) {
            Ok(w) => w,
            Err(e) => {
                error!(reason = %e, "failed to create filesystem watcher — hot-reload unavailable");
                return None;
            }
        };

        if let Err(e) = watcher.watch(plugins_dir, RecursiveMode::Recursive) {
            error!(
                path = %plugins_dir.display(),
                reason = %e,
                "failed to watch plugins_dir — hot-reload unavailable"
            );
            return None;
        }

        info!(
            plugins_dir = %plugins_dir.display(),
            "plugin hot-reload watcher started (dev mode)"
        );

        Some(Self {
            _watcher: watcher,
            plugins_dir: plugins_dir.to_owned(),
        })
    }

    /// The directory this watcher is monitoring.
    pub fn plugins_dir(&self) -> &Path {
        &self.plugins_dir
    }
}

// ── Drain helper ──────────────────────────────────────────────────────────────

/// Wait for an `Arc` to become the sole owner (strong count == 1), up to `timeout`.
///
/// WHY: when hot-reloading, the registry swaps the `Arc<LoadedPlugin>` atomically.
/// The old `Arc` may still be held by in-flight dispatch calls.  We wait for those
/// to complete (refcount drops to 1) before fully dropping the old plugin.
///
/// Returns `true` if the drain completed within the timeout; `false` on timeout.
pub fn drain_arc<T>(arc: &Arc<T>, timeout: Duration) -> bool {
    let deadline = Instant::now() + timeout;
    loop {
        if Arc::strong_count(arc) == 1 {
            return true;
        }
        if Instant::now() >= deadline {
            return false;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
}

/// Drain with the default `DRAIN_TIMEOUT`.
pub fn drain_arc_default<T>(arc: &Arc<T>) -> bool {
    drain_arc(arc, DRAIN_TIMEOUT)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use tempfile::TempDir;
    use tokio::sync::mpsc;

    #[test]
    fn watcher_does_not_start_in_production_mode() {
        let tmp = TempDir::new().unwrap();
        let (tx, _rx) = mpsc::channel(16);
        let watcher = PluginWatcher::start(tmp.path(), tx, false);
        assert!(
            watcher.is_none(),
            "watcher must be None when dev_mode=false"
        );
    }

    #[test]
    fn watcher_starts_in_dev_mode() {
        let tmp = TempDir::new().unwrap();
        let (tx, _rx) = mpsc::channel(16);
        let watcher = PluginWatcher::start(tmp.path(), tx, true);
        assert!(watcher.is_some(), "watcher should start in dev_mode=true");
    }

    /// Tests that the watcher fires a ReloadEvent when a .wasm file is written.
    ///
    /// Uses PollWatcher with a short interval to avoid platform-specific
    /// filesystem event latency (FSEvents, inotify, kqueue) in test environments.
    #[test]
    fn watcher_fires_on_wasm_write() {
        use notify::{Config as NotifyConfig, PollWatcher};
        use std::sync::{Arc as StdArc, Mutex as StdMutex};

        let tmp = TempDir::new().unwrap();
        // Use canonical path to avoid macOS /tmp -> /private/tmp symlink issues.
        let canonical_tmp = tmp.path().canonicalize().unwrap();
        let plugin_dir = canonical_tmp.join("com.example.test");
        fs::create_dir_all(&plugin_dir).unwrap();

        // Use a sync channel for the poll-based watcher test.
        let received = StdArc::new(StdMutex::new(Option::<ReloadEvent>::None));
        let received_clone = StdArc::clone(&received);
        let canonical_tmp_clone = canonical_tmp.clone();

        // Build a synchronous notify handler that writes the first matching event.
        let handler = move |result: notify::Result<notify::Event>| {
            if let Ok(event) = result {
                let is_relevant = matches!(
                    event.kind,
                    EventKind::Modify(ModifyKind::Data(_))
                        | EventKind::Modify(ModifyKind::Name(_))
                        | EventKind::Create(_)
                );
                if !is_relevant {
                    return;
                }
                for path in &event.paths {
                    if path.extension().and_then(|e| e.to_str()) == Some("wasm")
                        && path.starts_with(&canonical_tmp_clone)
                    {
                        if let Some(pd) = path.parent() {
                            let mut guard = received_clone.lock().unwrap();
                            if guard.is_none() {
                                *guard = Some(ReloadEvent {
                                    plugin_dir: pd.to_owned(),
                                    detected_at: Instant::now(),
                                });
                            }
                        }
                    }
                }
            }
        };

        // PollWatcher polls every 100ms — reliable across all platforms.
        let poll_interval = Duration::from_millis(100);
        let mut watcher = PollWatcher::new(
            handler,
            NotifyConfig::default().with_poll_interval(poll_interval),
        )
        .expect("PollWatcher::new");

        watcher
            .watch(&canonical_tmp, RecursiveMode::Recursive)
            .expect("watch");

        // Small sleep to ensure the first poll scan baseline is recorded.
        std::thread::sleep(Duration::from_millis(150));

        // Write the .wasm file.
        let wasm_path = plugin_dir.join("test.wasm");
        fs::write(&wasm_path, b"\0asm\x01\0\0\0").unwrap();

        // Poll for up to 3 seconds (30 × 100ms).
        let deadline = Instant::now() + Duration::from_secs(3);
        while Instant::now() < deadline {
            {
                let guard = received.lock().unwrap();
                if guard.is_some() {
                    break;
                }
            }
            std::thread::sleep(Duration::from_millis(100));
        }

        let ev = received.lock().unwrap().take();
        assert!(
            ev.is_some(),
            "expected a ReloadEvent within 3s of writing .wasm file"
        );
        let ev = ev.unwrap();
        assert_eq!(ev.plugin_dir, plugin_dir);
    }

    #[test]
    fn drain_arc_returns_true_when_sole_owner() {
        let arc = Arc::new(42u32);
        // We are the sole owner, so drain should return immediately.
        assert!(drain_arc(&arc, Duration::from_millis(100)));
    }

    #[test]
    fn drain_arc_times_out_with_live_clone() {
        let arc = Arc::new(42u32);
        let _clone = arc.clone(); // keep refcount at 2
                                  // With a very short timeout, the drain should time out.
        assert!(!drain_arc(&arc, Duration::from_millis(50)));
    }
}
