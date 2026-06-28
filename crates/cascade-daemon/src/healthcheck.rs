//! Healthcheck state for cascaded.
//!
//! Purpose: Maintain a point-in-time snapshot of daemon health that can be
//! queried at zero cost by the IPC handler. Written to by background sampler
//! tasks; read by the IPC `health` method.
//!
//! Output shape (matches IPC protocol frozen in S06-FREEZE):
//!   { "status": "ok" }
//!
//! NOTE: Detailed stats (pid, uptime_secs, queue_depth, ram_kb, cpu_pct) are no
//! longer exposed via the health endpoint to minimize security-relevant information
//! leakage to unauthenticated clients. Detailed stats remain available internally
//! via the status command and the CLI (authenticated, local only).

use std::sync::Arc;
use std::time::Instant;

use serde::Serialize;
use tokio::sync::RwLock;

/// Shared health state. Cloned via Arc into each IPC connection.
// IPC health surface — cloned into each IPC connection handler.
#[allow(dead_code)]
pub type SharedHealth = Arc<HealthState>;

#[derive(Debug)]
pub struct HealthState {
    // Daemon start time — used to compute uptime_secs for status output.
    #[allow(dead_code)]
    start: Instant,
    // Resource metrics updated by the background sampler task.
    #[allow(dead_code)]
    inner: RwLock<HealthInner>,
}

#[derive(Debug, Clone, Serialize)]
struct HealthInner {
    queue_depth: u64,
    ram_kb: u64,
    cpu_pct: f32,
}

#[derive(Debug, Clone, Serialize)]
pub struct HealthSnapshot {
    pub status: &'static str,
}

impl HealthState {
    /// Create new health state anchored at `start`.
    pub fn new(start: Instant) -> Arc<Self> {
        Arc::new(Self {
            start,
            inner: RwLock::new(HealthInner {
                queue_depth: 0,
                ram_kb: 0,
                cpu_pct: 0.0,
            }),
        })
    }

    /// Capture a point-in-time snapshot. Called from the IPC handler — must
    /// be non-blocking (acquires the RwLock read guard only).
    ///
    /// Returns a minimal health snapshot with only the status field to avoid
    /// exposing internal state to unauthenticated clients.
    pub fn snapshot(&self) -> HealthSnapshot {
        HealthSnapshot { status: "ok" }
    }

    /// Update resource metrics. Called by the background sampler task.
    // Called by background sampler — not yet wired through to IPC status output.
    #[allow(dead_code)]
    pub async fn update(&self, queue_depth: u64, ram_kb: u64, cpu_pct: f32) {
        let mut inner = self.inner.write().await;
        inner.queue_depth = queue_depth;
        inner.ram_kb = ram_kb;
        inner.cpu_pct = cpu_pct;
    }
}

/// Background task that samples process memory and (on supported platforms)
/// CPU every `interval_secs` and writes results into the shared health state.
// Planned background task — wired in P3 when detailed health metrics are exposed.
#[allow(dead_code)]
///
/// Intentionally simple: reads `/proc/self/status` on Linux, `ps` on macOS,
/// skips gracefully on Windows. CPU % is a rough 1-sample estimate.
pub async fn sampler(health: Arc<HealthState>, interval_secs: u64) {
    let interval = std::time::Duration::from_secs(interval_secs.max(1));
    loop {
        tokio::time::sleep(interval).await;
        let (ram_kb, cpu_pct) = sample_resources().await;
        // queue_depth is updated by the event bus; pass 0 here so we don't
        // overwrite it (the event bus calls update() with queue_depth set).
        let snap = health.inner.read().await;
        let q = snap.queue_depth;
        drop(snap);
        health.update(q, ram_kb, cpu_pct).await;
    }
}

// Called by the sampler task to read RAM and CPU from the OS.
#[allow(dead_code)]
async fn sample_resources() -> (u64, f32) {
    #[cfg(target_os = "linux")]
    {
        sample_linux().await
    }
    #[cfg(target_os = "macos")]
    {
        sample_macos().await
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos")))]
    {
        (0, 0.0)
    }
}

#[cfg(target_os = "linux")]
async fn sample_linux() -> (u64, f32) {
    // VmRSS line in /proc/self/status gives RSS in kB.
    let content = tokio::fs::read_to_string("/proc/self/status")
        .await
        .unwrap_or_default();
    let ram_kb = content
        .lines()
        .find(|l| l.starts_with("VmRSS:"))
        .and_then(|l| l.split_whitespace().nth(1))
        .and_then(|s| s.parse::<u64>().ok())
        .unwrap_or(0);
    (ram_kb, 0.0) // CPU % via /proc/self/stat requires two samples; skip for now
}

// macOS-specific resource sampler — called by sample_resources() on macOS.
#[cfg(target_os = "macos")]
#[allow(dead_code)]
async fn sample_macos() -> (u64, f32) {
    let pid = std::process::id().to_string();
    let output = tokio::process::Command::new("ps")
        .args(["-o", "rss=,pcpu=", "-p", &pid])
        .output()
        .await;
    match output {
        Ok(o) => {
            let s = String::from_utf8_lossy(&o.stdout);
            let mut parts = s.split_whitespace();
            let ram_kb = parts.next().and_then(|v| v.parse().ok()).unwrap_or(0);
            let cpu_pct = parts.next().and_then(|v| v.parse().ok()).unwrap_or(0.0);
            (ram_kb, cpu_pct)
        }
        Err(_) => (0, 0.0),
    }
}
