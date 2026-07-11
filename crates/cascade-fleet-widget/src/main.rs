//! cascade-fleet-widget — macOS menu-bar fleet-usage widget.
//!
//! Purpose: always-running NSStatusItem displaying live per-account AI quota
//!   data from `~/.cascade/accounts.json` + `~/.cascade/quota-store.json`.
//!   Refreshes every 30 s. Shows "—" for accounts with no quota data.
//! Inputs:  `~/.cascade/accounts.json`, `~/.cascade/quota-store.json`.
//! Outputs: native macOS menu-bar icon + dropdown (macOS); stdout notice (others).
//! Constraints: no unwrap() outside tests; no faked data; <300 lines.
//! SPORT: `.claude/docs/MASTER-CRATES.md` — cascade-fleet-widget

use cascade_core::accounts_store::read_accounts_registry;
use cascade_core::quota_store::read_quota_store;
use cascade_types::accounts::{Account, AccountFamily, AccountRole, AccountsRegistry};
use cascade_types::quota_store::QuotaStore;
use std::path::PathBuf;

// ── Paths ─────────────────────────────────────────────────────────────────────

fn accounts_path() -> PathBuf {
    cascade_types::paths::home_dir()
        .join(".cascade")
        .join("accounts.json")
}

fn quota_store_path() -> PathBuf {
    cascade_types::paths::home_dir()
        .join(".cascade")
        .join("quota-store.json")
}

// ── Data ──────────────────────────────────────────────────────────────────────

struct FleetData {
    registry: Option<AccountsRegistry>,
    store: Option<QuotaStore>,
}

fn load_fleet_data() -> FleetData {
    FleetData {
        registry: read_accounts_registry(&accounts_path()).ok(),
        store: read_quota_store(&quota_store_path()).ok(),
    }
}

// ── Display helpers ───────────────────────────────────────────────────────────

fn family_label(family: &AccountFamily) -> &'static str {
    match family {
        AccountFamily::Claude => "Claude",
        AccountFamily::Openai => "OpenAI",
        AccountFamily::Google => "Google",
        AccountFamily::Opencode => "OpenCode",
        AccountFamily::Gfp => "GFP (key pool)",
        AccountFamily::Zai => "Za (z.ai)",
    }
}

fn role_badge(role: &AccountRole) -> &'static str {
    match role {
        AccountRole::PrimaryT0 => "[PRIMARY]",
        AccountRole::Pooled => "Pooled",
        AccountRole::Free => "Free",
        AccountRole::Fallback => "Fallback",
    }
}

fn access_methods_concise(account: &Account) -> String {
    use cascade_types::accounts::AccessMethod;
    account
        .access_methods
        .iter()
        .map(|m| match m {
            AccessMethod::NativeCc => "cc",
            AccessMethod::SmithersClaudeP => "smithers",
            AccessMethod::CodexCli => "codex",
            AccessMethod::AgyCli => "agy",
            AccessMethod::OpencodeRun => "opencode",
            AccessMethod::GfpKeypool => "key-pool",
        })
        .collect::<Vec<_>>()
        .join("/")
}

/// Build a usage string for one account; returns "—" when no store data exists.
fn quota_summary(account: &Account, store: &Option<QuotaStore>) -> String {
    let store = match store {
        Some(s) => s,
        None => return "—".to_string(),
    };

    let entry = store
        .accounts
        .iter()
        .find(|e| {
            account
                .quota_account_id
                .as_deref()
                .map(|q| e.account_id == q)
                .unwrap_or(false)
        })
        .or_else(|| store.accounts.iter().find(|e| e.account_id == account.id));

    let entry = match entry {
        Some(e) => e,
        None => return "—".to_string(),
    };

    if let Some(win) = entry.rate_windows.first() {
        let lim = win
            .limit
            .map(|l| format!("/{}", l / 1_000))
            .unwrap_or_default();
        return format!("{}k{} ({})", win.used / 1_000, lim, win.label);
    }

    let used: u64 = entry.models.values().map(|m| m.used).sum();
    let limit: Option<u64> = entry.models.values().filter_map(|m| m.limit).next();
    match limit {
        Some(lim) => format!("{}k/{}k tokens", used / 1_000, lim / 1_000),
        None => format!("{}k tokens", used / 1_000),
    }
}

fn format_account_line(account: &Account, store: &Option<QuotaStore>) -> String {
    format!(
        "{}  {}  {}  cli:{}  keys:{}  {}",
        account.id,
        role_badge(&account.role),
        access_methods_concise(account),
        if account.cli_available { "✓" } else { "✗" },
        account.key_count,
        quota_summary(account, store),
    )
}

// ── Menu builder ──────────────────────────────────────────────────────────────

/// Build (title, menu_lines) from current fleet data.
fn build_menu_content(data: &FleetData) -> (String, Vec<String>) {
    let registry = match &data.registry {
        Some(r) => r,
        None => {
            return (
                "Fleet: no data".to_string(),
                vec!["accounts.json not found".to_string()],
            )
        }
    };

    let title = format!("Fleet: {} accounts", registry.accounts.len());
    let mut lines = Vec::new();

    for family in &[
        AccountFamily::Claude,
        AccountFamily::Openai,
        AccountFamily::Google,
        AccountFamily::Opencode,
        AccountFamily::Gfp,
        AccountFamily::Zai,
    ] {
        let members: Vec<_> = registry
            .accounts
            .iter()
            .filter(|a| &a.family == family)
            .collect();
        if members.is_empty() {
            continue;
        }
        lines.push(format!("── {} ──", family_label(family)));
        for account in members {
            lines.push(format_account_line(account, &data.store));
        }
    }

    (title, lines)
}

// ── Entry point ───────────────────────────────────────────────────────────────

fn main() {
    run_widget();
}

// ── macOS ─────────────────────────────────────────────────────────────────────

#[cfg(target_os = "macos")]
fn run_widget() {
    use cascade_tray::{MacosTrayImpl, TrayAction, TrayHandle};
    use std::time::{Duration, Instant};

    let mut data = load_fleet_data();
    let (title, lines) = build_menu_content(&data);

    let mut tray = match MacosTrayImpl::new(&title) {
        Ok(t) => t,
        Err(e) => {
            eprintln!("cascade-fleet-widget: tray init failed: {e}");
            std::process::exit(1);
        }
    };

    apply_menu(&mut tray, &title, &lines);
    if let Err(e) = tray.show() {
        eprintln!("cascade-fleet-widget: show failed: {e}");
    }
    push_state(&mut tray, &lines);

    let interval = Duration::from_secs(30);
    let mut last = Instant::now();

    loop {
        while let Some(action) = tray.last_action() {
            match action {
                TrayAction::Quit => {
                    println!("cascade-fleet-widget: quit");
                    std::process::exit(0);
                }
                TrayAction::OpenDashboard => {
                    data = load_fleet_data();
                    let (t, l) = build_menu_content(&data);
                    apply_menu(&mut tray, &t, &l);
                    push_state(&mut tray, &l);
                    last = Instant::now();
                }
                TrayAction::OpenApp => {
                    let home = cascade_types::paths::home_dir();
                    let app_path = home.join("Applications").join("Cascade.app");
                    if app_path.exists() {
                        let _ = std::process::Command::new("open")
                            .arg("-a")
                            .arg(&app_path)
                            .spawn();
                    } else {
                        let fallback = home.join(".cascade").join("accounts");
                        let _ = std::process::Command::new("open").arg(&fallback).spawn();
                    }
                }
                _ => {}
            }
        }
        if last.elapsed() >= interval {
            data = load_fleet_data();
            let (t, l) = build_menu_content(&data);
            apply_menu(&mut tray, &t, &l);
            push_state(&mut tray, &l);
            last = Instant::now();
        }
        std::thread::sleep(Duration::from_millis(100));
    }
}

#[cfg(target_os = "macos")]
fn apply_menu(tray: &mut cascade_tray::MacosTrayImpl, title: &str, lines: &[String]) {
    use cascade_tray::{TrayAction, TrayHandle, TrayMenuItem, TrayMenuSpec};

    let mut items = vec![
        TrayMenuItem::Action {
            label: title.to_string(),
            action: TrayAction::OpenApp,
        },
        TrayMenuItem::Separator,
    ];
    for line in lines {
        items.push(TrayMenuItem::Action {
            label: line.clone(),
            action: TrayAction::OpenApp,
        });
    }
    items.push(TrayMenuItem::Separator);
    items.push(TrayMenuItem::Action {
        label: "Refresh now".to_string(),
        action: TrayAction::OpenDashboard,
    });
    items.push(TrayMenuItem::Action {
        label: "Quit".to_string(),
        action: TrayAction::Quit,
    });

    if let Err(e) = tray.set_menu(&TrayMenuSpec { items }) {
        eprintln!("cascade-fleet-widget: set_menu failed: {e}");
    }
}

#[cfg(target_os = "macos")]
fn push_state(tray: &mut cascade_tray::MacosTrayImpl, lines: &[String]) {
    use cascade_tray::{TrayHandle, TrayState};
    let state = TrayState {
        fleet_summary: lines.to_vec(),
        ..TrayState::default()
    };
    if let Err(e) = tray.update(&state) {
        eprintln!("cascade-fleet-widget: update failed: {e}");
    }
}

// ── Non-macOS stub ────────────────────────────────────────────────────────────

#[cfg(not(target_os = "macos"))]
fn run_widget() {
    eprintln!("cascade-fleet-widget: tray widget is macOS-only in this build.");
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn no_data_returns_fallback_title() {
        let data = FleetData {
            registry: None,
            store: None,
        };
        let (title, _) = build_menu_content(&data);
        assert!(
            title.contains("Fleet") || title.contains("no data"),
            "bad title: {title}"
        );
    }

    #[test]
    fn quota_summary_no_store_is_dash() {
        use cascade_types::accounts::AccessMethod;
        let account = Account {
            id: "t".to_string(),
            family: AccountFamily::Claude,
            subscription: "s".to_string(),
            access_methods: vec![AccessMethod::NativeCc],
            role: AccountRole::PrimaryT0,
            exhaustion_priority: 255,
            models: vec![],
            cli_available: true,
            key_count: 0,
            quota_account_id: None,
            notes: None,
        };
        assert_eq!(quota_summary(&account, &None), "—");
    }

    #[test]
    fn role_badges_are_distinct() {
        use std::collections::HashSet;
        let badge_arr = [
            role_badge(&AccountRole::PrimaryT0),
            role_badge(&AccountRole::Pooled),
            role_badge(&AccountRole::Free),
            role_badge(&AccountRole::Fallback),
        ];
        let badges: HashSet<_> = badge_arr.iter().collect();
        assert_eq!(badges.len(), 4);
    }

    #[test]
    fn family_labels_non_empty() {
        for f in &[
            AccountFamily::Claude,
            AccountFamily::Openai,
            AccountFamily::Google,
            AccountFamily::Opencode,
            AccountFamily::Gfp,
        ] {
            assert!(!family_label(f).is_empty());
        }
    }
}
