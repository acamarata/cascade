//! # cascade_core::settings::plugins
//!
//! Helper methods for [`PluginsSettings`] that make the `enabled` vector in
//! `settings.json` an actionable persistence path for plugin enable/disable
//! state (T-P7-E20-26).
//!
//! ## Purpose
//!
//! The `PluginsSettings.enabled: Vec<String>` field has existed in the
//! `settings.json` schema since T-P3-E07-14 and round-trips correctly, but
//! nothing consumed it — it was vestigial schema. These helpers turn it into a
//! real persistence path: callers can query, enable, and disable plugins by id,
//! then `store::save` to persist across restarts.
//!
//! This complements (does NOT replace) the `.disabled` marker-file mechanism in
//! `cascade-plugins/src/loader.rs`, which is the active load-time gate. Both
//! mechanisms are additive and independent.
//!
//! ## Inputs
//!
//! `&self` / `&mut self` on [`PluginsSettings`]; a plugin id string.
//!
//! ## Outputs
//!
//! `bool` for `is_enabled`; `()` for `enable`/`disable` (mutate in place).
//!
//! ## Constraints
//!
//! - `enable` and `disable` are idempotent.
//! - Callers must invoke `store::save` to persist changes to disk.
//! - An unknown plugin id in the `enabled` list is harmless — it is just a
//!   string; loading never crashes (see `unknown_plugin_in_config_does_not_crash_load`).
//!
//! ## SPORT
//!
//! MASTER-RUST-CRATES.md — cascade-core: settings::plugins (T-P7-E20-26)

use super::types::PluginsSettings;

impl PluginsSettings {
    /// Returns `true` if the plugin with the given id is in the enabled list.
    ///
    /// "Enabled" here means the user has opted in via `settings.json`; the
    /// loader still needs to find the plugin on disk to actually load it.
    pub fn is_enabled(&self, id: &str) -> bool {
        self.enabled.iter().any(|e| e == id)
    }

    /// Mark a plugin as enabled (append its id to the enabled list).
    ///
    /// Idempotent: enabling an already-enabled plugin is a no-op.
    /// Call `cascade_core::settings::store::save` afterwards to persist.
    pub fn enable(&mut self, id: &str) {
        if !self.is_enabled(id) {
            self.enabled.push(id.to_string());
        }
    }

    /// Mark a plugin as disabled (remove its id from the enabled list).
    ///
    /// Idempotent: disabling a plugin that is not enabled is a no-op.
    /// Call `cascade_core::settings::store::save` afterwards to persist.
    pub fn disable(&mut self, id: &str) {
        self.enabled.retain(|e| e != id);
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::super::store::{load, save};
    use super::super::types::{CascadeSettings, PluginsSettings};
    use tempfile::TempDir;

    /// Helper: settings.json path inside a temp dir.
    fn settings_path(tmp: &TempDir) -> std::path::PathBuf {
        tmp.path().join("settings.json")
    }

    // ── unit tests for the helper methods ─────────────────────────────────────

    /// `is_enabled` returns false for a plugin that was never enabled.
    #[test]
    fn is_enabled_false_for_unknown() {
        let p = PluginsSettings::default();
        assert!(!p.is_enabled("com.example.alpha"));
    }

    /// `enable` adds the id; `is_enabled` returns true afterwards.
    #[test]
    fn enable_makes_is_enabled_true() {
        let mut p = PluginsSettings::default();
        p.enable("com.example.alpha");
        assert!(p.is_enabled("com.example.alpha"));
    }

    /// `enable` is idempotent — no duplicate entries.
    #[test]
    fn enable_is_idempotent() {
        let mut p = PluginsSettings::default();
        p.enable("com.example.dup");
        p.enable("com.example.dup");
        let count = p
            .enabled
            .iter()
            .filter(|e| **e == "com.example.dup")
            .count();
        assert_eq!(count, 1, "enable must not duplicate entries");
    }

    /// `disable` removes the id; `is_enabled` returns false afterwards.
    #[test]
    fn disable_makes_is_enabled_false() {
        let mut p = PluginsSettings::default();
        p.enable("com.example.alpha");
        p.disable("com.example.alpha");
        assert!(!p.is_enabled("com.example.alpha"));
    }

    /// `disable` on a plugin that was never enabled is a no-op (no crash).
    #[test]
    fn disable_when_not_enabled_is_noop() {
        let mut p = PluginsSettings::default();
        p.disable("com.example.ghost");
        assert!(p.enabled.is_empty());
    }

    // ── persistence round-trip across a simulated restart ─────────────────────

    /// A plugin's enabled state survives a simulated restart (save → drop → load).
    ///
    /// This is the primary acceptance test for T-P7-E20-26: "state round-trips
    /// across a simulated restart".
    #[test]
    fn plugin_enable_state_round_trips_across_restart() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        // Session 1: enable a plugin and persist.
        let mut s = CascadeSettings::default();
        assert!(!s.plugins.is_enabled("com.example.alpha"));
        s.plugins.enable("com.example.alpha");
        assert!(s.plugins.is_enabled("com.example.alpha"));
        save(&path, &s).expect("save after enable");

        // Simulated restart: drop in-memory state, reload from disk.
        drop(s);
        let reloaded = load(&path).expect("load after restart");
        assert!(
            reloaded.plugins.is_enabled("com.example.alpha"),
            "plugin must remain enabled after restart"
        );

        // Session 2: disable the plugin and persist.
        let mut s2 = reloaded;
        s2.plugins.disable("com.example.alpha");
        assert!(!s2.plugins.is_enabled("com.example.alpha"));
        save(&path, &s2).expect("save after disable");

        // Simulated restart again.
        drop(s2);
        let reloaded2 = load(&path).expect("load after second restart");
        assert!(
            !reloaded2.plugins.is_enabled("com.example.alpha"),
            "plugin must remain disabled after restart"
        );
    }

    /// Multiple plugins can be enabled/disabled independently and round-trip.
    #[test]
    fn multiple_plugins_independent_round_trip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.plugins.enable("com.example.one");
        s.plugins.enable("com.example.two");
        s.plugins.enable("com.example.three");
        save(&path, &s).unwrap();

        let loaded = load(&path).unwrap();
        assert!(loaded.plugins.is_enabled("com.example.one"));
        assert!(loaded.plugins.is_enabled("com.example.two"));
        assert!(loaded.plugins.is_enabled("com.example.three"));
        assert!(!loaded.plugins.is_enabled("com.example.four"));

        // Disable one, restart, verify only that one changed.
        let mut s2 = loaded;
        s2.plugins.disable("com.example.two");
        save(&path, &s2).unwrap();
        let loaded2 = load(&path).unwrap();
        assert!(loaded2.plugins.is_enabled("com.example.one"));
        assert!(!loaded2.plugins.is_enabled("com.example.two"));
        assert!(loaded2.plugins.is_enabled("com.example.three"));
    }

    // ── unknown / removed plugin robustness ───────────────────────────────────

    /// An unknown/removed plugin slug in config does not crash load.
    ///
    /// Scenario: a user enabled a plugin, then deleted its directory. On next
    /// restart the config still lists the slug, but the plugin is gone. Loading
    /// the settings file must succeed without error — `enabled` is just a list
    /// of strings, and deserialising it never touches the filesystem plugin.
    #[test]
    fn unknown_plugin_in_config_does_not_crash_load() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        // Write a settings file that references plugins that do not exist on disk.
        let mut s = CascadeSettings::default();
        s.plugins.enable("com.example.nonexistent");
        s.plugins.enable("com.example.also-gone");
        save(&path, &s).expect("save");

        // Load must succeed — the config is just a list of strings.
        let loaded = load(&path).expect("load must not crash for unknown plugins");
        assert!(loaded.plugins.is_enabled("com.example.nonexistent"));
        assert!(loaded.plugins.is_enabled("com.example.also-gone"));

        // The per-plugin config map can also reference unknown plugins safely.
        let mut s2 = CascadeSettings::default();
        s2.plugins.config.insert(
            "com.example.ghost".to_string(),
            serde_json::json!({ "key": "value" }),
        );
        save(&path, &s2).expect("save with ghost config");
        let loaded2 = load(&path).expect("load must not crash for ghost config");
        assert_eq!(loaded2.plugins.config["com.example.ghost"]["key"], "value");
    }

    /// A config file with an empty `enabled` array loads cleanly.
    #[test]
    fn empty_enabled_array_loads_cleanly() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let json = serde_json::json!({
            "schemaVersion": "2",
            "plugins": { "enabled": [] }
        });
        std::fs::write(&path, serde_json::to_string_pretty(&json).unwrap()).unwrap();

        let loaded = load(&path).expect("empty enabled array must load");
        assert!(loaded.plugins.enabled.is_empty());
    }
}
