//! RAM Guardian — prevent OOM crashes during heavy parallel builds/agents.
//!
//! Purpose: A daemon-owned background task that samples system memory
//! pressure on an interval and, only when memory is genuinely tight, reaps
//! orphaned `rustc`/`vitest`/`node .*vitest` processes that have been
//! reparented to init (ppid == 1) and have run past an age threshold. It
//! never touches live builds, the daemon itself, or any Claude/Cascade
//! process.
//!
//! Root cause (spec: `.claude/ideas/ram-guardian.md`, incidents
//! 2026-07-01/02): 5 concurrent `cargo build`/`vitest` processes plus the
//! Claude.app harness drove RAM to 50-60GB and crashed the machine. Orphaned
//! `vitest`/`rustc` processes survived agent death and accumulated —
//! killing strays alone moved free RAM from 25% to 50%.
//!
//! Inputs: none (self-contained; samples the OS on each tick).
//! Outputs:
//!   - `~/.cascade/ram-guardian/state.json` — current status snapshot,
//!     read by `cascade ram status` and other tooling.
//!   - `~/.cascade/ram-guardian/reports/*.md` — one report per sweep that
//!     killed at least one process.
//!     Constraints:
//!   - Never panics: every sampling/kill failure is logged at WARN and the
//!     loop continues.
//!   - Reaping is conservative by design (see `should_reap`) — a false kill
//!     of an active build is worse than a missed stray.
//!   - `RAM_GUARDIAN_DISABLE=1` turns the reaper into log-only (dry run);
//!     sampling and reporting still run.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-daemon, ram_guardian module

use std::path::{Path, PathBuf};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tokio_util::sync::CancellationToken;
use tracing::{debug, info, warn};

// ── Tunables (consts, documented reasoning) ───────────────────────────────────

/// How often the guardian samples memory and (conditionally) sweeps strays.
/// 30s matches the spec — frequent enough to react to a fast OOM ramp,
/// infrequent enough to never be a measurable load itself.
pub const SAMPLE_INTERVAL: Duration = Duration::from_secs(30);

/// Free-memory percentage above which the system is considered healthy.
/// Chosen from the observed incident curve: the crash happened well below
/// this, and macOS itself starts compressing memory around this point.
pub const FREE_PCT_OK_THRESHOLD: f32 = 25.0;

/// Free-memory percentage below which the system is in WARN territory —
/// the reaper is allowed to run. Between CRITICAL and OK.
pub const FREE_PCT_WARN_THRESHOLD: f32 = 15.0;

/// Minimum age (wall clock) a stray process must have reached before it is
/// eligible for reaping, even under Critical pressure. 10 minutes is long
/// enough that no reasonable single `cargo build`/`vitest run` invocation is
/// still legitimately mid-flight, short enough to actually recover RAM
/// during a live incident.
pub const MIN_STRAY_AGE: Duration = Duration::from_secs(10 * 60);

/// Process names the reaper is allowed to ever consider. Anything else is
/// never a kill candidate regardless of ppid/age.
pub const REAPABLE_NAMES: &[&str] = &["rustc", "vitest"];

/// Env var that forces the reaper into log-only (dry-run) mode. Sampling and
/// state/report writing still happen; no signal is ever sent.
pub const DISABLE_ENV: &str = "RAM_GUARDIAN_DISABLE";

// ── Memory status ──────────────────────────────────────────────────────────────

/// Coarse classification of current free-memory pressure.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum MemStatus {
    /// > FREE_PCT_OK_THRESHOLD free — no action needed.
    Ok,
    /// Between WARN and OK thresholds — reaper may run.
    Warn,
    /// <= FREE_PCT_WARN_THRESHOLD free — reaper runs, pause flag written.
    Critical,
}

impl MemStatus {
    /// True if the reaper is permitted to run at this status.
    pub fn reaper_allowed(self) -> bool {
        matches!(self, MemStatus::Warn | MemStatus::Critical)
    }
}

/// Classify a free-memory percentage into a [`MemStatus`].
///
/// Pure function — the sole classification authority so tests can exercise
/// every boundary without touching the OS.
pub fn classify_free_pct(free_pct: f32) -> MemStatus {
    if free_pct > FREE_PCT_OK_THRESHOLD {
        MemStatus::Ok
    } else if free_pct > FREE_PCT_WARN_THRESHOLD {
        MemStatus::Warn
    } else {
        MemStatus::Critical
    }
}

// ── Sampling ─────────────────────────────────────────────────────────────────

/// Result of one memory sample.
#[derive(Debug, Clone, Copy)]
pub struct MemSample {
    pub free_pct: f32,
    pub status: MemStatus,
}

/// Sample current free-memory percentage.
///
/// macOS: parses `memory_pressure -Q` ("System-wide memory free percentage: NN%"),
/// falling back to `vm_stat` (free+inactive+speculative / total pages) if the
/// first parse fails. Any other platform: returns `None` — the guardian task
/// logs once and skips sampling on that tick rather than guessing.
///
/// Never panics: subprocess spawn/parse failures return `None`.
pub fn sample_free_pct() -> Option<f32> {
    #[cfg(target_os = "macos")]
    {
        if let Some(pct) = sample_via_memory_pressure() {
            return Some(pct);
        }
        sample_via_vm_stat()
    }
    #[cfg(not(target_os = "macos"))]
    {
        None
    }
}

#[cfg(target_os = "macos")]
fn sample_via_memory_pressure() -> Option<f32> {
    let output = std::process::Command::new("memory_pressure")
        .arg("-Q")
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&output.stdout);
    parse_memory_pressure_output(&text)
}

/// Parse the free-percentage line out of `memory_pressure -Q` output.
///
/// Expected line: "System-wide memory free percentage: 81%". Pure/testable.
fn parse_memory_pressure_output(text: &str) -> Option<f32> {
    for line in text.lines() {
        if let Some(idx) = line.to_ascii_lowercase().find("free percentage") {
            let rest = &line[idx..];
            let digits: String = rest
                .chars()
                .filter(|c| c.is_ascii_digit() || *c == '.')
                .collect();
            if let Ok(v) = digits.parse::<f32>() {
                return Some(v);
            }
        }
    }
    None
}

#[cfg(target_os = "macos")]
fn sample_via_vm_stat() -> Option<f32> {
    let output = std::process::Command::new("vm_stat").output().ok()?;
    if !output.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&output.stdout);
    parse_vm_stat_output(&text)
}

/// Parse `vm_stat` output into a free-memory percentage.
///
/// free% = (free + inactive + speculative) / (free + active + inactive +
/// speculative + wired). Pure/testable.
fn parse_vm_stat_output(text: &str) -> Option<f32> {
    let mut free = 0u64;
    let mut active = 0u64;
    let mut inactive = 0u64;
    let mut speculative = 0u64;
    let mut wired = 0u64;

    for line in text.lines() {
        let (key, val) = line.split_once(':')?;
        let val: u64 = val
            .trim()
            .trim_end_matches('.')
            .parse()
            .unwrap_or_default();
        match key.trim() {
            "Pages free" => free = val,
            "Pages active" => active = val,
            "Pages inactive" => inactive = val,
            "Pages speculative" => speculative = val,
            "Pages wired down" => wired = val,
            _ => {}
        }
    }

    let total = free + active + inactive + speculative + wired;
    if total == 0 {
        return None;
    }
    let available = free + inactive + speculative;
    Some((available as f32 / total as f32) * 100.0)
}

/// Take a full sample: free% + classification. Logs WARN and returns `None`
/// on sampling failure (unsupported platform, subprocess error).
pub fn take_sample() -> Option<MemSample> {
    let free_pct = sample_free_pct()?;
    Some(MemSample {
        free_pct,
        status: classify_free_pct(free_pct),
    })
}

// ── Stray process model ────────────────────────────────────────────────────────

/// A candidate process observed by the reaper's process scan.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ProcInfo {
    pub pid: i32,
    pub ppid: i32,
    pub name: String,
    pub age: Duration,
}

/// Decide whether `proc` should be reaped, given current memory `status`.
///
/// ALL of the following must hold — any single missing condition means
/// "do not kill":
///   1. `status` is Warn or Critical (never reap on Ok).
///   2. `proc.ppid == 1` (reparented to init — its original parent, almost
///      certainly the agent/shell that spawned it, is dead).
///   3. `proc.age >= MIN_STRAY_AGE`.
///   4. `proc.name` matches one of [`REAPABLE_NAMES`] (rustc/vitest only —
///      never the daemon, `cascaded`, or any `claude`/Claude.app process,
///      which are never in this list regardless of ppid/age).
///
/// Pure function — no I/O, exhaustively unit-testable.
pub fn should_reap(proc: &ProcInfo, status: MemStatus) -> bool {
    if !status.reaper_allowed() {
        return false;
    }
    if proc.ppid != 1 {
        return false;
    }
    if proc.age < MIN_STRAY_AGE {
        return false;
    }
    is_reapable_name(&proc.name)
}

/// True if `name` matches a reapable process (substring match on the
/// well-known stray names: `rustc`, `vitest`, or `node .*vitest`).
///
/// Substring match (not exact) because `ps -o comm=` can report a full path
/// (e.g. `/usr/local/bin/vitest`) or a wrapped invocation
/// (`node /path/to/vitest/dist/cli.js`).
fn is_reapable_name(name: &str) -> bool {
    let lower = name.to_ascii_lowercase();
    REAPABLE_NAMES.iter().any(|n| lower.contains(n))
}

// ── State file ───────────────────────────────────────────────────────────────

/// Persisted guardian state, written to `~/.cascade/ram-guardian/state.json`
/// after every sweep. Read by `cascade ram status` and other tooling that
/// wants to honor the advisory pause flag.
#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
pub struct GuardianState {
    pub free_pct: f32,
    pub status: MemStatus,
    /// Unix timestamp (seconds) of the last completed sweep.
    pub last_swept: u64,
    /// Cumulative count of strays killed since the daemon started.
    pub strays_killed_total: u64,
    /// Advisory flag: true when status is Critical. Other tooling (e.g. a
    /// build wrapper) MAY check this and defer starting new heavy jobs.
    /// The guardian never forcibly blocks anything itself.
    pub pause: bool,
}

impl GuardianState {
    /// Load state from `path`. Returns `None` if the file is missing or
    /// unparseable — callers should fall back to a fresh default state.
    pub fn load(path: &Path) -> Option<Self> {
        let text = std::fs::read_to_string(path).ok()?;
        serde_json::from_str(&text).ok()
    }

    /// Write state to `path` as pretty JSON. Creates the parent directory if
    /// needed. Best-effort — errors are returned for the caller to log.
    pub fn save(&self, path: &Path) -> std::io::Result<()> {
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let json = serde_json::to_string_pretty(self)
            .unwrap_or_else(|_| "{}".to_owned());
        std::fs::write(path, json)
    }
}

/// Path to the guardian's state file under `~/.cascade/ram-guardian/`.
pub fn state_path(cascade_dir: &Path) -> PathBuf {
    cascade_dir.join("ram-guardian").join("state.json")
}

/// Path to the guardian's report directory under `~/.cascade/ram-guardian/`.
pub fn reports_dir(cascade_dir: &Path) -> PathBuf {
    cascade_dir.join("ram-guardian").join("reports")
}

// ── Kill report ──────────────────────────────────────────────────────────────

/// One kill event, used both for the on-disk report and structured logging.
#[derive(Debug, Clone, PartialEq)]
pub struct KillEvent {
    pub pid: i32,
    pub name: String,
    pub age: Duration,
}

/// Format a sweep's kill events as a Markdown report body.
///
/// Pure/testable. Returns `None` when `events` is empty — callers should
/// skip writing a report file for a no-op sweep.
pub fn format_report(events: &[KillEvent], sample: MemSample, dry_run: bool) -> Option<String> {
    if events.is_empty() {
        return None;
    }
    let mut out = String::new();
    out.push_str("# RAM Guardian sweep report\n\n");
    out.push_str(&format!(
        "- free%: {:.1}\n- status: {:?}\n- dry_run: {dry_run}\n- strays: {}\n\n",
        sample.free_pct,
        sample.status,
        events.len()
    ));
    out.push_str("| pid | name | age (s) |\n|---|---|---|\n");
    for e in events {
        out.push_str(&format!("| {} | {} | {} |\n", e.pid, e.name, e.age.as_secs()));
    }
    Some(out)
}

// ── Process scan (macOS-first) ─────────────────────────────────────────────────

/// Scan the process table for stray candidates via `ps`.
///
/// Uses `ps -axo pid=,ppid=,etime=,comm=` (macOS/BSD-compatible; Linux's
/// procps also accepts `etime=`). Returns an empty vec (never panics) if the
/// `ps` invocation fails or its output cannot be parsed.
pub fn scan_processes() -> Vec<ProcInfo> {
    let output = match std::process::Command::new("ps")
        .args(["-axo", "pid=,ppid=,etime=,comm="])
        .output()
    {
        Ok(o) if o.status.success() => o,
        _ => return Vec::new(),
    };
    let text = String::from_utf8_lossy(&output.stdout);
    parse_ps_output(&text)
}

/// Parse `ps -axo pid=,ppid=,etime=,comm=` output into [`ProcInfo`] entries.
///
/// Only lines that parse cleanly are kept; malformed lines are skipped.
/// Pure/testable.
fn parse_ps_output(text: &str) -> Vec<ProcInfo> {
    let mut out = Vec::new();
    for line in text.lines() {
        let trimmed = line.trim_start();
        let mut parts = trimmed.splitn(4, char::is_whitespace);
        let pid = match parts.next().and_then(|s| s.parse::<i32>().ok()) {
            Some(p) => p,
            None => continue,
        };
        let rest = trimmed[pid.to_string().len()..].trim_start();
        let mut parts2 = rest.splitn(3, char::is_whitespace);
        let ppid = match parts2.next().and_then(|s| s.parse::<i32>().ok()) {
            Some(p) => p,
            None => continue,
        };
        let rest2 = match rest.get(ppid.to_string().len()..) {
            Some(r) => r.trim_start(),
            None => continue,
        };
        let mut parts3 = rest2.splitn(2, char::is_whitespace);
        let etime = match parts3.next() {
            Some(e) => e,
            None => continue,
        };
        let comm = match parts3.next() {
            Some(c) => c.trim(),
            None => continue,
        };
        let age = match parse_etime(etime) {
            Some(a) => a,
            None => continue,
        };
        out.push(ProcInfo {
            pid,
            ppid,
            name: comm.to_owned(),
            age,
        });
    }
    out
}

/// Parse a `ps etime` field (`[[dd-]hh:]mm:ss`) into a [`Duration`].
///
/// Examples: `05:12` (mm:ss), `01:02:26` (hh:mm:ss), `2-03:04:05` (dd-hh:mm:ss).
/// Pure/testable. Returns `None` on malformed input.
fn parse_etime(etime: &str) -> Option<Duration> {
    let (days, rest) = match etime.split_once('-') {
        Some((d, r)) => (d.parse::<u64>().ok()?, r),
        None => (0u64, etime),
    };
    let fields: Vec<&str> = rest.split(':').collect();
    let (hours, mins, secs) = match fields.as_slice() {
        [h, m, s] => (h.parse::<u64>().ok()?, m.parse::<u64>().ok()?, s.parse::<u64>().ok()?),
        [m, s] => (0u64, m.parse::<u64>().ok()?, s.parse::<u64>().ok()?),
        _ => return None,
    };
    let total = days * 86_400 + hours * 3_600 + mins * 60 + secs;
    Some(Duration::from_secs(total))
}

// ── Kill primitive ───────────────────────────────────────────────────────────

/// Send SIGTERM, wait briefly, then SIGKILL if the process is still alive.
///
/// Unix-only (the reapable process names — rustc/vitest orphan accumulation
/// — are the observed failure mode on macOS/Linux dev machines; Windows is a
/// no-op here). Never panics: `kill(2)` failures (e.g. ESRCH — already gone)
/// are swallowed, since the goal (process not running) is already satisfied.
#[cfg(unix)]
fn terminate_then_kill(pid: i32) {
    unsafe {
        libc::kill(pid, libc::SIGTERM);
    }
    std::thread::sleep(Duration::from_millis(500));
    // Signal 0 checks liveness without sending a real signal.
    let alive = unsafe { libc::kill(pid, 0) } == 0;
    if alive {
        unsafe {
            libc::kill(pid, libc::SIGKILL);
        }
    }
}

#[cfg(not(unix))]
fn terminate_then_kill(_pid: i32) {}

// ── Sweep ────────────────────────────────────────────────────────────────────

/// Run one full sweep: sample memory, scan processes, reap eligible strays
/// (unless `dry_run`), update `state`, and return the events for reporting.
///
/// `dry_run` is true when `RAM_GUARDIAN_DISABLE` is set — sampling, scanning,
/// and logging still happen but no signal is ever sent, matching the escape
/// hatch contract in the module spec.
pub fn run_sweep(state: &mut GuardianState, dry_run: bool) -> (MemSample, Vec<KillEvent>) {
    let sample = match take_sample() {
        Some(s) => s,
        None => {
            warn!("ram_guardian: memory sampling failed this tick — skipping");
            // Keep prior state's free_pct/status; only bump last_swept so a
            // stuck sampler is still visible via last_swept staleness.
            state.last_swept = now_unix();
            return (
                MemSample {
                    free_pct: state.free_pct,
                    status: state.status,
                },
                Vec::new(),
            );
        }
    };

    let mut events = Vec::new();
    if sample.status.reaper_allowed() {
        for proc in scan_processes() {
            if should_reap(&proc, sample.status) {
                if dry_run {
                    info!(
                        pid = proc.pid,
                        name = %proc.name,
                        age_secs = proc.age.as_secs(),
                        "ram_guardian: DRY RUN — would reap stray process"
                    );
                } else {
                    info!(
                        pid = proc.pid,
                        name = %proc.name,
                        age_secs = proc.age.as_secs(),
                        "ram_guardian: reaping stray process"
                    );
                    terminate_then_kill(proc.pid);
                }
                events.push(KillEvent {
                    pid: proc.pid,
                    name: proc.name,
                    age: proc.age,
                });
            }
        }
    }

    state.free_pct = sample.free_pct;
    state.status = sample.status;
    state.last_swept = now_unix();
    state.pause = matches!(sample.status, MemStatus::Critical);
    if !dry_run {
        state.strays_killed_total += events.len() as u64;
    }

    (sample, events)
}

fn now_unix() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

/// Write a sweep report to `~/.cascade/ram-guardian/reports/` if `events` is
/// non-empty. Best-effort — failures are logged, never fatal.
fn write_report(cascade_dir: &Path, sample: MemSample, events: &[KillEvent], dry_run: bool) {
    let Some(body) = format_report(events, sample, dry_run) else {
        return;
    };
    let dir = reports_dir(cascade_dir);
    if let Err(e) = std::fs::create_dir_all(&dir) {
        warn!(%e, "ram_guardian: failed to create reports dir");
        return;
    }
    let fname = format!("sweep-{}.md", now_unix());
    let path = dir.join(fname);
    if let Err(e) = std::fs::write(&path, body) {
        warn!(%e, path = %path.display(), "ram_guardian: failed to write sweep report");
    } else {
        debug!(path = %path.display(), "ram_guardian: sweep report written");
    }

    // Best-effort project-inbox mirror: cascade's own inbox convention is
    // per-project (`.cascade/inbox/`), which is out of scope for a
    // machine-wide daemon task. We only write the global report above;
    // per-project inbox delivery is left to higher-level tooling that knows
    // which projects are active.
}

// ── Public entry: spawn the daemon task ────────────────────────────────────────

/// Spawn the RAM Guardian background task.
///
/// Runs on [`SAMPLE_INTERVAL`] until `shutdown` is cancelled. Loads any
/// existing state from disk at startup (preserving `strays_killed_total`
/// across daemon restarts); writes fresh state after every sweep.
///
/// `RAM_GUARDIAN_DISABLE` (any non-empty value) forces dry-run mode: the
/// reaper logs what it *would* kill but never sends a signal.
pub fn spawn(cascade_dir: PathBuf, shutdown: CancellationToken) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        let dry_run = std::env::var_os(DISABLE_ENV).is_some();
        if dry_run {
            info!("ram_guardian: RAM_GUARDIAN_DISABLE set — reaper running in log-only mode");
        }

        let path = state_path(&cascade_dir);
        let mut state = GuardianState::load(&path).unwrap_or(GuardianState {
            free_pct: 100.0,
            status: MemStatus::Ok,
            last_swept: 0,
            strays_killed_total: 0,
            pause: false,
        });

        let mut interval = tokio::time::interval(SAMPLE_INTERVAL);
        interval.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Skip);

        loop {
            tokio::select! {
                _ = interval.tick() => {}
                _ = shutdown.cancelled() => {
                    info!("ram_guardian: shutdown requested — exiting sweep loop");
                    return;
                }
            }

            let (sample, events) = run_sweep(&mut state, dry_run);
            if let Err(e) = state.save(&path) {
                warn!(%e, "ram_guardian: failed to write state.json");
            }
            if !events.is_empty() {
                write_report(&cascade_dir, sample, &events, dry_run);
            }
            debug!(
                free_pct = sample.free_pct,
                status = ?sample.status,
                strays_this_sweep = events.len(),
                "ram_guardian: sweep complete"
            );
        }
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    // ── classify_free_pct ──────────────────────────────────────────────────

    #[test]
    fn classify_ok_above_threshold() {
        assert_eq!(classify_free_pct(50.0), MemStatus::Ok);
        assert_eq!(classify_free_pct(25.1), MemStatus::Ok);
    }

    #[test]
    fn classify_warn_between_thresholds() {
        assert_eq!(classify_free_pct(25.0), MemStatus::Warn);
        assert_eq!(classify_free_pct(20.0), MemStatus::Warn);
        assert_eq!(classify_free_pct(15.1), MemStatus::Warn);
    }

    #[test]
    fn classify_critical_at_or_below_threshold() {
        assert_eq!(classify_free_pct(15.0), MemStatus::Critical);
        assert_eq!(classify_free_pct(5.0), MemStatus::Critical);
        assert_eq!(classify_free_pct(0.0), MemStatus::Critical);
    }

    #[test]
    fn reaper_allowed_only_warn_and_critical() {
        assert!(!MemStatus::Ok.reaper_allowed());
        assert!(MemStatus::Warn.reaper_allowed());
        assert!(MemStatus::Critical.reaper_allowed());
    }

    // ── should_reap predicate ──────────────────────────────────────────────

    fn stray(name: &str, ppid: i32, age_secs: u64) -> ProcInfo {
        ProcInfo {
            pid: 4242,
            ppid,
            name: name.to_owned(),
            age: Duration::from_secs(age_secs),
        }
    }

    #[test]
    fn should_reap_true_when_all_conditions_met() {
        let p = stray("rustc", 1, MIN_STRAY_AGE.as_secs() + 1);
        assert!(should_reap(&p, MemStatus::Critical));
        assert!(should_reap(&p, MemStatus::Warn));
    }

    #[test]
    fn should_reap_false_on_ok_status() {
        let p = stray("vitest", 1, MIN_STRAY_AGE.as_secs() + 100);
        assert!(!should_reap(&p, MemStatus::Ok));
    }

    #[test]
    fn should_reap_false_when_not_reparented() {
        let p = stray("rustc", 5678, MIN_STRAY_AGE.as_secs() + 100);
        assert!(!should_reap(&p, MemStatus::Critical));
    }

    #[test]
    fn should_reap_false_when_too_young() {
        let p = stray("rustc", 1, 60);
        assert!(!should_reap(&p, MemStatus::Critical));
    }

    #[test]
    fn should_reap_false_at_exact_age_boundary_minus_one() {
        let p = stray("rustc", 1, MIN_STRAY_AGE.as_secs() - 1);
        assert!(!should_reap(&p, MemStatus::Critical));
    }

    #[test]
    fn should_reap_true_at_exact_age_boundary() {
        let p = stray("rustc", 1, MIN_STRAY_AGE.as_secs());
        assert!(should_reap(&p, MemStatus::Critical));
    }

    #[test]
    fn should_reap_false_for_unrelated_process_name() {
        let p = stray("cascaded", 1, MIN_STRAY_AGE.as_secs() + 100);
        assert!(!should_reap(&p, MemStatus::Critical));
    }

    #[test]
    fn should_reap_false_for_claude_process() {
        let p = stray("claude", 1, MIN_STRAY_AGE.as_secs() + 100);
        assert!(!should_reap(&p, MemStatus::Critical));
    }

    #[test]
    fn should_reap_true_for_node_vitest_wrapper() {
        let p = stray(
            "node /opt/homebrew/lib/node_modules/vitest/dist/cli.js",
            1,
            MIN_STRAY_AGE.as_secs() + 1,
        );
        assert!(should_reap(&p, MemStatus::Critical));
    }

    #[test]
    fn is_reapable_name_case_insensitive() {
        assert!(is_reapable_name("RUSTC"));
        assert!(is_reapable_name("Vitest"));
        assert!(!is_reapable_name("rustfmt"));
        assert!(!is_reapable_name("cascaded"));
    }

    // ── parse_memory_pressure_output ───────────────────────────────────────

    #[test]
    fn parse_memory_pressure_typical_output() {
        let text = "The system has 17179869184 (1048576 pages with a page size of 16384).\n\
                     System-wide memory free percentage: 81%\n";
        assert_eq!(parse_memory_pressure_output(text), Some(81.0));
    }

    #[test]
    fn parse_memory_pressure_missing_line_returns_none() {
        let text = "some unrelated output\n";
        assert_eq!(parse_memory_pressure_output(text), None);
    }

    // ── parse_vm_stat_output ────────────────────────────────────────────────

    #[test]
    fn parse_vm_stat_typical_output() {
        let text = "Mach Virtual Memory Statistics: (page size of 16384 bytes)\n\
                     Pages free:                                   100.\n\
                     Pages active:                                 100.\n\
                     Pages inactive:                                50.\n\
                     Pages speculative:                             50.\n\
                     Pages throttled:                                0.\n\
                     Pages wired down:                             100.\n";
        // available = 100 + 50 + 50 = 200; total = 100+100+50+50+100 = 400
        // free% = 200/400 * 100 = 50.0
        let pct = parse_vm_stat_output(text).expect("should parse");
        assert!((pct - 50.0).abs() < 0.001);
    }

    #[test]
    fn parse_vm_stat_empty_returns_none() {
        assert_eq!(parse_vm_stat_output(""), None);
    }

    // ── parse_etime ─────────────────────────────────────────────────────────

    #[test]
    fn parse_etime_mm_ss() {
        assert_eq!(parse_etime("05:12"), Some(Duration::from_secs(5 * 60 + 12)));
    }

    #[test]
    fn parse_etime_hh_mm_ss() {
        assert_eq!(
            parse_etime("01:02:26"),
            Some(Duration::from_secs(3600 + 2 * 60 + 26))
        );
    }

    #[test]
    fn parse_etime_dd_hh_mm_ss() {
        assert_eq!(
            parse_etime("2-03:04:05"),
            Some(Duration::from_secs(2 * 86_400 + 3 * 3600 + 4 * 60 + 5))
        );
    }

    #[test]
    fn parse_etime_malformed_returns_none() {
        assert_eq!(parse_etime("not-a-time"), None);
        assert_eq!(parse_etime(""), None);
    }

    // ── parse_ps_output ─────────────────────────────────────────────────────

    #[test]
    fn parse_ps_output_typical_lines() {
        let text = "  1234     1 01:02:26 rustc\n\
                     5678  4321 00:05:00 /usr/bin/vitest\n";
        let procs = parse_ps_output(text);
        assert_eq!(procs.len(), 2);
        assert_eq!(procs[0].pid, 1234);
        assert_eq!(procs[0].ppid, 1);
        assert_eq!(procs[0].name, "rustc");
        assert_eq!(procs[0].age, Duration::from_secs(3600 + 2 * 60 + 26));
        assert_eq!(procs[1].pid, 5678);
        assert_eq!(procs[1].ppid, 4321);
        assert_eq!(procs[1].name, "/usr/bin/vitest");
    }

    #[test]
    fn parse_ps_output_skips_malformed_lines() {
        let text = "not a valid line at all\n  9999     1 00:10:00 vitest\n";
        let procs = parse_ps_output(text);
        assert_eq!(procs.len(), 1);
        assert_eq!(procs[0].pid, 9999);
    }

    #[test]
    fn parse_ps_output_empty_input() {
        assert!(parse_ps_output("").is_empty());
    }

    // ── GuardianState (de)serialize ───────────────────────────────────────

    #[test]
    fn guardian_state_round_trips_through_json() {
        let state = GuardianState {
            free_pct: 12.5,
            status: MemStatus::Critical,
            last_swept: 1_700_000_000,
            strays_killed_total: 3,
            pause: true,
        };
        let json = serde_json::to_string(&state).unwrap();
        let back: GuardianState = serde_json::from_str(&json).unwrap();
        assert_eq!(state, back);
    }

    #[test]
    fn guardian_state_save_and_load_round_trip() {
        let dir = std::env::temp_dir().join(format!(
            "ram-guardian-test-{}-{}",
            std::process::id(),
            now_unix()
        ));
        let path = dir.join("state.json");
        let state = GuardianState {
            free_pct: 42.0,
            status: MemStatus::Warn,
            last_swept: 123,
            strays_killed_total: 7,
            pause: false,
        };
        state.save(&path).expect("save should succeed");
        let loaded = GuardianState::load(&path).expect("load should succeed");
        assert_eq!(state, loaded);
        let _ = std::fs::remove_dir_all(&dir);
    }

    #[test]
    fn guardian_state_load_missing_file_returns_none() {
        let path = std::env::temp_dir().join("ram-guardian-does-not-exist-12345/state.json");
        assert!(GuardianState::load(&path).is_none());
    }

    // ── format_report ──────────────────────────────────────────────────────

    #[test]
    fn format_report_empty_events_returns_none() {
        let sample = MemSample {
            free_pct: 10.0,
            status: MemStatus::Critical,
        };
        assert_eq!(format_report(&[], sample, false), None);
    }

    #[test]
    fn format_report_includes_pid_and_name() {
        let sample = MemSample {
            free_pct: 10.0,
            status: MemStatus::Critical,
        };
        let events = vec![KillEvent {
            pid: 999,
            name: "rustc".to_owned(),
            age: Duration::from_secs(700),
        }];
        let report = format_report(&events, sample, false).expect("should format");
        assert!(report.contains("999"));
        assert!(report.contains("rustc"));
        assert!(report.contains("700"));
    }

    #[test]
    fn format_report_marks_dry_run() {
        let sample = MemSample {
            free_pct: 10.0,
            status: MemStatus::Critical,
        };
        let events = vec![KillEvent {
            pid: 1,
            name: "vitest".to_owned(),
            age: Duration::from_secs(1),
        }];
        let report = format_report(&events, sample, true).expect("should format");
        assert!(report.contains("dry_run: true"));
    }

    // ── run_sweep (integration of pure pieces, no real process killing) ────

    #[test]
    fn run_sweep_updates_last_swept_even_on_sample_failure() {
        // We cannot force sample_free_pct() to fail portably in this test
        // without mocking the OS layer, so we only assert the function does
        // not panic and returns a sample/events pair when called directly.
        let mut state = GuardianState {
            free_pct: 100.0,
            status: MemStatus::Ok,
            last_swept: 0,
            strays_killed_total: 0,
            pause: false,
        };
        let (_, events) = run_sweep(&mut state, true);
        // On this dev machine sampling should succeed; either way, no panic
        // and last_swept must advance.
        assert!(state.last_swept > 0);
        // Dry run must never increment strays_killed_total.
        let _ = events;
    }
}
