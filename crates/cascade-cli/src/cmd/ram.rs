//! `cascade ram` — inspect and manually trigger the RAM Guardian.
//!
//! Purpose: Surface the daemon-owned RAM Guardian's state (free memory %,
//! status, stray-reap counters) and provide a manual `sweep` for on-demand
//! use outside the daemon's 30s loop (e.g. right after a machine felt tight).
//!
//! Inputs:
//!   - `cascade ram status` — reads `~/.cascade/ram-guardian/state.json`.
//!   - `cascade ram sweep`  — samples memory + scans/reaps strays right now,
//!     independent of whether the daemon is running, and updates the same
//!     state file so `status` reflects it afterward.
//!
//! Outputs: human-readable summary to stdout.
//!
//! Constraints:
//!   - This command is intentionally self-contained (no dependency on the
//!     `cascade-daemon` crate) — it re-implements the same conservative
//!     classification/reap-predicate logic locally so the CLI never needs to
//!     link the daemon binary's dependency graph. Keep this logic in sync
//!     with `crates/cascade-daemon/src/ram_guardian.rs` if either changes.
//!   - `sweep` honors `RAM_GUARDIAN_DISABLE` the same way the daemon does
//!     (log-only, no signal sent).
//!   - Never panics: sampling/scan failures print a friendly message and
//!     exit 0 rather than erroring out.
//!
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-cli, ram module

use std::path::{Path, PathBuf};
use std::time::Duration;

use async_trait::async_trait;
use cascade_types::error::Result;
use cascade_types::paths::global_cascade_dir;
use clap::{Args, Subcommand};
use serde::{Deserialize, Serialize};

use super::Command;

// ── Args ──────────────────────────────────────────────────────────────────────

/// `cascade ram` — inspect and manually trigger the RAM Guardian.
#[derive(Debug, Args)]
pub struct RamArgs {
    #[command(subcommand)]
    pub subcommand: RamCommands,
}

/// Subcommands under `cascade ram`.
#[derive(Debug, Subcommand)]
pub enum RamCommands {
    /// Print free%, status, strays reaped, and last sweep time.
    Status,
    /// Force one stray-reap pass now and print what was (or would be) killed.
    Sweep,
}

#[async_trait]
impl Command for RamArgs {
    async fn run(&self) -> Result<()> {
        match &self.subcommand {
            RamCommands::Status => run_status(),
            RamCommands::Sweep => run_sweep_cmd(),
        }
    }
}

// ── Tunables (mirror cascade-daemon::ram_guardian consts) ─────────────────────

const FREE_PCT_OK_THRESHOLD: f32 = 25.0;
const FREE_PCT_WARN_THRESHOLD: f32 = 15.0;
const MIN_STRAY_AGE: Duration = Duration::from_secs(10 * 60);
const REAPABLE_NAMES: &[&str] = &["rustc", "vitest"];
const DISABLE_ENV: &str = "RAM_GUARDIAN_DISABLE";

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
enum MemStatus {
    Ok,
    Warn,
    Critical,
}

impl MemStatus {
    fn reaper_allowed(self) -> bool {
        matches!(self, MemStatus::Warn | MemStatus::Critical)
    }

    fn label(self) -> &'static str {
        match self {
            MemStatus::Ok => "ok",
            MemStatus::Warn => "warn",
            MemStatus::Critical => "critical",
        }
    }
}

fn classify_free_pct(free_pct: f32) -> MemStatus {
    if free_pct > FREE_PCT_OK_THRESHOLD {
        MemStatus::Ok
    } else if free_pct > FREE_PCT_WARN_THRESHOLD {
        MemStatus::Warn
    } else {
        MemStatus::Critical
    }
}

#[derive(Debug, Clone, Serialize, Deserialize, PartialEq)]
struct GuardianState {
    free_pct: f32,
    status: MemStatus,
    last_swept: u64,
    strays_killed_total: u64,
    pause: bool,
}

fn state_path(cascade_dir: &Path) -> PathBuf {
    cascade_dir.join("ram-guardian").join("state.json")
}

fn load_state(path: &Path) -> Option<GuardianState> {
    let text = std::fs::read_to_string(path).ok()?;
    serde_json::from_str(&text).ok()
}

fn save_state(state: &GuardianState, path: &Path) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let json = serde_json::to_string_pretty(state).unwrap_or_else(|_| "{}".to_owned());
    std::fs::write(path, json)
}

fn now_unix() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}

// ── status ────────────────────────────────────────────────────────────────────

fn run_status() -> Result<()> {
    let path = state_path(&global_cascade_dir());
    match load_state(&path) {
        Some(state) => {
            println!("RAM Guardian status");
            println!("  free:              {:.1}%", state.free_pct);
            println!("  status:            {}", state.status.label());
            println!("  last sweep:        {}", format_relative(state.last_swept));
            println!("  strays reaped:     {}", state.strays_killed_total);
            println!(
                "  pause flag:        {}",
                if state.pause {
                    "SET (critical)"
                } else {
                    "clear"
                }
            );
        }
        None => {
            println!(
                "(no RAM Guardian state found at {} — daemon may not have run a sweep yet)",
                path.display()
            );
        }
    }
    Ok(())
}

fn format_relative(ts: u64) -> String {
    if ts == 0 {
        return "never".to_owned();
    }
    let now = now_unix();
    let delta = now.saturating_sub(ts);
    if delta < 60 {
        format!("{delta}s ago")
    } else if delta < 3600 {
        format!("{}m ago", delta / 60)
    } else {
        format!("{}h ago", delta / 3600)
    }
}

// ── sweep ─────────────────────────────────────────────────────────────────────

fn run_sweep_cmd() -> Result<()> {
    let dry_run = std::env::var_os(DISABLE_ENV).is_some();
    if dry_run {
        println!(
            "(RAM_GUARDIAN_DISABLE set — running in log-only mode, no processes will be killed)"
        );
    }

    let cascade_dir = global_cascade_dir();
    let path = state_path(&cascade_dir);
    let mut state = load_state(&path).unwrap_or(GuardianState {
        free_pct: 100.0,
        status: MemStatus::Ok,
        last_swept: 0,
        strays_killed_total: 0,
        pause: false,
    });

    let free_pct = match sample_free_pct() {
        Some(p) => p,
        None => {
            println!("(memory sampling unavailable on this platform — skipping sweep)");
            return Ok(());
        }
    };
    let status = classify_free_pct(free_pct);
    println!("free: {free_pct:.1}%  status: {}", status.label());

    let mut killed = 0u64;
    if status.reaper_allowed() {
        let procs = scan_processes();
        let mut any = false;
        for proc in procs {
            if should_reap(&proc, status) {
                any = true;
                if dry_run {
                    println!(
                        "  would reap: pid={} name={} age={}s",
                        proc.pid,
                        proc.name,
                        proc.age.as_secs()
                    );
                } else {
                    println!(
                        "  reaping: pid={} name={} age={}s",
                        proc.pid,
                        proc.name,
                        proc.age.as_secs()
                    );
                    terminate_then_kill(proc.pid);
                    killed += 1;
                }
            }
        }
        if !any {
            println!("  no eligible strays found (reparented + >=10min old + rustc/vitest)");
        }
    } else {
        println!("  memory healthy — reaper not run");
    }

    state.free_pct = free_pct;
    state.status = status;
    state.last_swept = now_unix();
    state.pause = matches!(status, MemStatus::Critical);
    if !dry_run {
        state.strays_killed_total += killed;
    }
    if let Err(e) = save_state(&state, &path) {
        eprintln!("warning: failed to write {}: {e}", path.display());
    }

    Ok(())
}

// ── Process scan + kill (mirrors cascade-daemon::ram_guardian) ────────────────

#[derive(Debug, Clone, PartialEq, Eq)]
struct ProcInfo {
    pid: i32,
    ppid: i32,
    name: String,
    age: Duration,
}

fn should_reap(proc: &ProcInfo, status: MemStatus) -> bool {
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

fn is_reapable_name(name: &str) -> bool {
    let lower = name.to_ascii_lowercase();
    REAPABLE_NAMES.iter().any(|n| lower.contains(n))
}

#[cfg(target_os = "macos")]
fn sample_free_pct() -> Option<f32> {
    sample_via_memory_pressure().or_else(sample_via_vm_stat)
}

#[cfg(not(target_os = "macos"))]
fn sample_free_pct() -> Option<f32> {
    None
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

#[allow(dead_code)]
fn parse_vm_stat_output(text: &str) -> Option<f32> {
    let mut free = 0u64;
    let mut active = 0u64;
    let mut inactive = 0u64;
    let mut speculative = 0u64;
    let mut wired = 0u64;

    for line in text.lines() {
        let (key, val) = line.split_once(':')?;
        let val: u64 = val.trim().trim_end_matches('.').parse().unwrap_or_default();
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

fn scan_processes() -> Vec<ProcInfo> {
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

fn parse_etime(etime: &str) -> Option<Duration> {
    let (days, rest) = match etime.split_once('-') {
        Some((d, r)) => (d.parse::<u64>().ok()?, r),
        None => (0u64, etime),
    };
    let fields: Vec<&str> = rest.split(':').collect();
    let (hours, mins, secs) = match fields.as_slice() {
        [h, m, s] => (
            h.parse::<u64>().ok()?,
            m.parse::<u64>().ok()?,
            s.parse::<u64>().ok()?,
        ),
        [m, s] => (0u64, m.parse::<u64>().ok()?, s.parse::<u64>().ok()?),
        _ => return None,
    };
    let total = days * 86_400 + hours * 3_600 + mins * 60 + secs;
    Some(Duration::from_secs(total))
}

#[cfg(unix)]
fn terminate_then_kill(pid: i32) {
    unsafe {
        libc::kill(pid, libc::SIGTERM);
    }
    std::thread::sleep(Duration::from_millis(500));
    let alive = unsafe { libc::kill(pid, 0) } == 0;
    if alive {
        unsafe {
            libc::kill(pid, libc::SIGKILL);
        }
    }
}

#[cfg(not(unix))]
fn terminate_then_kill(_pid: i32) {}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classify_free_pct_boundaries() {
        assert_eq!(classify_free_pct(50.0), MemStatus::Ok);
        assert_eq!(classify_free_pct(25.0), MemStatus::Warn);
        assert_eq!(classify_free_pct(15.0), MemStatus::Critical);
    }

    #[test]
    fn should_reap_requires_all_conditions() {
        let p = ProcInfo {
            pid: 1,
            ppid: 1,
            name: "rustc".to_owned(),
            age: MIN_STRAY_AGE + Duration::from_secs(1),
        };
        assert!(should_reap(&p, MemStatus::Critical));

        let not_reparented = ProcInfo {
            ppid: 999,
            ..p.clone()
        };
        assert!(!should_reap(&not_reparented, MemStatus::Critical));

        let too_young = ProcInfo {
            age: Duration::from_secs(1),
            ..p.clone()
        };
        assert!(!should_reap(&too_young, MemStatus::Critical));

        assert!(!should_reap(&p, MemStatus::Ok));

        let unrelated_name = ProcInfo {
            name: "claude".to_owned(),
            ..p
        };
        assert!(!should_reap(&unrelated_name, MemStatus::Critical));
    }

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
    fn format_relative_never_for_zero() {
        assert_eq!(format_relative(0), "never");
    }

    #[test]
    fn parse_etime_variants() {
        assert_eq!(parse_etime("05:12"), Some(Duration::from_secs(312)));
        assert_eq!(parse_etime("01:02:26"), Some(Duration::from_secs(3746)));
        assert_eq!(
            parse_etime("2-03:04:05"),
            Some(Duration::from_secs(2 * 86_400 + 3 * 3600 + 4 * 60 + 5))
        );
        assert_eq!(parse_etime("garbage"), None);
    }

    #[test]
    fn ram_args_help_builds_without_panic() {
        use clap::Args as ClapArgs;
        let _ = RamArgs::augment_args(clap::Command::new("ram"));
    }
}
