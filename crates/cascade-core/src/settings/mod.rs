//! # cascade_core::settings
//!
//! Application settings — load/save `~/.cascade/settings.json` and expose
//! the top-level [`CascadeSettings`] struct.
//!
//! ## Modules
//!
//! - [`types`]   — `CascadeSettings` struct + serde derives.
//! - [`store`]   — `load(path)` / `save(path, settings)` with atomic write.
//!
//! ## Quick start
//!
//! ```no_run
//! use cascade_core::settings::store::{load, save};
//! use cascade_types::paths::global_cascade_dir;
//!
//! let path = global_cascade_dir().join("settings.json");
//! let mut s = load(&path).unwrap();
//! s.schema_version = "2".to_string();
//! save(&path, &s).unwrap();
//! ```
//!
//! ## SPORT
//!
//! MASTER-RUST-CRATES.md — cascade-core: settings module (T-P3-E07-14)

pub mod store;
pub mod types;

pub use types::CascadeSettings;

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use tempfile::TempDir;

    use super::store::{load, save};
    use super::types::{
        CascadeSettings, GeminiPoolSettings, HarnessBridgesSettings, HookDefinition,
        LibrarySettings, McpServerEntry, PluginsSettings, ProvidersSettings,
        ScheduledTaskEntry, TelemetrySettings, VaultDisplaySettings, WidgetGeometry,
        WidgetsSettings,
    };

    // ── helpers ────────────────────────────────────────────────────────────────

    fn settings_path(tmp: &TempDir) -> std::path::PathBuf {
        tmp.path().join("settings.json")
    }

    // ── baseline tests (from T-P3-E07-00, preserved) ──────────────────────────

    /// Loading from a non-existent path returns Default without error.
    #[test]
    fn load_missing_file_returns_default() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let s = load(&path).expect("load should succeed for missing file");
        assert_eq!(s.schema_version, "2");
        assert!(s.providers.anthropic.is_none());
        assert!(s.gemini_pool.keys.is_empty());
    }

    /// Saving then loading round-trips the full struct without data loss.
    #[test]
    fn save_then_load_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut original = CascadeSettings::default();
        original.providers.anthropic = Some("sk-test".to_string());
        original.gemini_pool.keys = vec!["key1".to_string(), "key2".to_string()];
        original.gemini_pool.enabled = true;

        save(&path, &original).expect("save should succeed");
        assert!(path.exists(), "settings.json must exist after save");

        let tmp_path = path.with_extension("json.tmp");
        assert!(!tmp_path.exists(), "settings.json.tmp must not exist after atomic rename");

        let loaded = load(&path).expect("load after save should succeed");
        assert_eq!(loaded.schema_version, original.schema_version);
        assert_eq!(loaded.providers.anthropic, original.providers.anthropic);
        assert_eq!(loaded.gemini_pool.keys, original.gemini_pool.keys);
        assert_eq!(loaded.gemini_pool.enabled, original.gemini_pool.enabled);
    }

    // ── schema migration: missing sections filled from Default ─────────────────

    /// An old-schema file missing all new sections deserialises without error
    /// and fills every new section from its Default.
    #[test]
    fn old_schema_missing_sections_filled_with_defaults() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        // Write a v1 file with only the baseline fields.
        let v1 = serde_json::json!({
            "schemaVersion": "1",
            "providers": {},
            "geminiPool": {}
        });
        std::fs::write(&path, serde_json::to_string_pretty(&v1).unwrap()).unwrap();

        let s = load(&path).expect("old-schema file must load without error");

        // New fields default correctly.
        assert!(s.library.default_harness_targets.is_empty());
        assert!(s.context.sites_root.is_none());
        assert!(s.project_map.phase_root.is_none());
        assert!(s.harness_bridges.cc_config_path.is_none());
        assert!(s.hooks.is_empty());
        assert!(s.scheduled_tasks.is_empty());
        assert!(s.plugins.enabled.is_empty());
        assert!(s.widgets.positions.is_empty());
        assert!(s.mcp_servers.is_empty());
        assert!(!s.vault_display.show_masked);
        assert!(!s.telemetry.enabled);
        assert_eq!(s.gemini_pool.proxy_port, 3761);
    }

    // ── round-trip per section ─────────────────────────────────────────────────

    /// LibrarySettings round-trips correctly.
    #[test]
    fn library_settings_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.library = LibrarySettings {
            default_harness_targets: vec!["cc".to_string(), "oc".to_string()],
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.library.default_harness_targets, vec!["cc", "oc"]);
    }

    /// ContextSettings + ProjectMapSettings round-trip.
    #[test]
    fn context_and_projectmap_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.context.sites_root = Some("/custom/Sites".to_string());
        s.project_map.phase_root = Some("/custom/phases".to_string());
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.context.sites_root.as_deref(), Some("/custom/Sites"));
        assert_eq!(loaded.project_map.phase_root.as_deref(), Some("/custom/phases"));
    }

    /// ProvidersSettings: all key fields optional, google is a vec.
    #[test]
    fn providers_settings_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.providers = ProvidersSettings {
            anthropic: Some("sk-ant-test".to_string()),
            openai: Some("sk-oai-test".to_string()),
            google: vec!["g1".to_string(), "g2".to_string()],
            codex: None,
            default_routing: Some("anthropic".to_string()),
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.providers.anthropic.as_deref(), Some("sk-ant-test"));
        assert_eq!(loaded.providers.openai.as_deref(), Some("sk-oai-test"));
        assert_eq!(loaded.providers.google, vec!["g1", "g2"]);
        assert!(loaded.providers.codex.is_none());
        assert_eq!(loaded.providers.default_routing.as_deref(), Some("anthropic"));
    }

    /// GeminiPoolSettings round-trip including proxy_port.
    #[test]
    fn gemini_pool_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.gemini_pool = GeminiPoolSettings {
            keys: vec!["k1".to_string()],
            proxy_port: 9999,
            enabled: true,
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.gemini_pool.keys, vec!["k1"]);
        assert_eq!(loaded.gemini_pool.proxy_port, 9999);
        assert!(loaded.gemini_pool.enabled);
    }

    /// HarnessBridgesSettings round-trip.
    #[test]
    fn harness_bridges_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.harness_bridges = HarnessBridgesSettings {
            cc_config_path: Some("/home/user/.claude/settings.json".to_string()),
            oc_config_path: Some("/home/user/.opencode/settings.json".to_string()),
            codex_config_path: None,
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(
            loaded.harness_bridges.cc_config_path.as_deref(),
            Some("/home/user/.claude/settings.json")
        );
        assert!(loaded.harness_bridges.codex_config_path.is_none());
    }

    /// Hooks array round-trips correctly.
    #[test]
    fn hooks_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.hooks = vec![HookDefinition {
            id: "on-start".to_string(),
            event: "session_start".to_string(),
            command: "echo hello".to_string(),
            enabled: true,
            extra: HashMap::new(),
        }];
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.hooks.len(), 1);
        assert_eq!(loaded.hooks[0].id, "on-start");
        assert_eq!(loaded.hooks[0].event, "session_start");
        assert!(loaded.hooks[0].enabled);
    }

    /// ScheduledTaskEntry array round-trips.
    #[test]
    fn scheduled_tasks_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.scheduled_tasks = vec![ScheduledTaskEntry {
            id: "nightly-index".to_string(),
            label: "Nightly index rebuild".to_string(),
            cron: "0 2 * * *".to_string(),
            command: "cascade index rebuild".to_string(),
            enabled: true,
            extra: HashMap::new(),
        }];
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.scheduled_tasks.len(), 1);
        assert_eq!(loaded.scheduled_tasks[0].id, "nightly-index");
        assert_eq!(loaded.scheduled_tasks[0].cron, "0 2 * * *");
    }

    /// PluginsSettings round-trip including per-plugin config.
    #[test]
    fn plugins_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut config = HashMap::new();
        config.insert("my-plugin".to_string(), serde_json::json!({ "key": "value" }));

        let mut s = CascadeSettings::default();
        s.plugins = PluginsSettings {
            enabled: vec!["my-plugin".to_string()],
            config,
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.plugins.enabled, vec!["my-plugin"]);
        assert_eq!(loaded.plugins.config["my-plugin"]["key"], "value");
    }

    /// WidgetsSettings round-trip.
    #[test]
    fn widgets_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut positions = HashMap::new();
        positions.insert(
            "usage-widget".to_string(),
            WidgetGeometry {
                x: 100,
                y: 200,
                width: 400,
                height: 250,
                extra: HashMap::new(),
            },
        );

        let mut s = CascadeSettings::default();
        s.widgets = WidgetsSettings {
            positions,
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        let geom = &loaded.widgets.positions["usage-widget"];
        assert_eq!(geom.x, 100);
        assert_eq!(geom.y, 200);
        assert_eq!(geom.width, 400);
        assert_eq!(geom.height, 250);
    }

    /// McpServerEntry array round-trip.
    #[test]
    fn mcp_servers_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut env = HashMap::new();
        env.insert("API_KEY".to_string(), "abc123".to_string());

        let mut s = CascadeSettings::default();
        s.mcp_servers = vec![McpServerEntry {
            name: "my-mcp".to_string(),
            command: "/usr/local/bin/my-mcp-server".to_string(),
            args: vec!["--port".to_string(), "8080".to_string()],
            env,
            enabled: true,
            extra: HashMap::new(),
        }];
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert_eq!(loaded.mcp_servers.len(), 1);
        assert_eq!(loaded.mcp_servers[0].name, "my-mcp");
        assert_eq!(loaded.mcp_servers[0].args, vec!["--port", "8080"]);
        assert_eq!(loaded.mcp_servers[0].env["API_KEY"], "abc123");
    }

    /// VaultDisplaySettings round-trip.
    #[test]
    fn vault_display_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.vault_display = VaultDisplaySettings {
            show_masked: true,
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert!(loaded.vault_display.show_masked);
    }

    /// TelemetrySettings round-trip.
    #[test]
    fn telemetry_roundtrip() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        let mut s = CascadeSettings::default();
        s.telemetry = TelemetrySettings {
            enabled: true,
            endpoint: Some("https://telemetry.example.com".to_string()),
            extra: HashMap::new(),
        };
        save(&path, &s).unwrap();
        let loaded = load(&path).unwrap();
        assert!(loaded.telemetry.enabled);
        assert_eq!(
            loaded.telemetry.endpoint.as_deref(),
            Some("https://telemetry.example.com")
        );
    }

    // ── unknown-field preservation ─────────────────────────────────────────────

    /// Unknown top-level fields are preserved on round-trip (forward-compat).
    #[test]
    fn unknown_top_level_fields_preserved() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        // Write a "future" file with a field this version doesn't know.
        let future = serde_json::json!({
            "schemaVersion": "99",
            "unknownFutureSection": { "someKey": "someValue" }
        });
        std::fs::write(&path, serde_json::to_string_pretty(&future).unwrap()).unwrap();

        let loaded = load(&path).expect("future-schema file must load");
        // Unknown field is in extra.
        assert!(loaded.extra.contains_key("unknownFutureSection"));

        // Re-save and reload — field must still be there.
        save(&path, &loaded).unwrap();
        let reloaded = load(&path).unwrap();
        assert!(reloaded.extra.contains_key("unknownFutureSection"));
        assert_eq!(reloaded.extra["unknownFutureSection"]["someKey"], "someValue");
    }

    // ── legacy update_settings top-level merge test (kept) ────────────────────

    /// update_settings: merging partial JSON updates only the specified top-level keys.
    #[test]
    fn partial_update_merges_top_level_fields() {
        let tmp = TempDir::new().unwrap();
        let path = settings_path(&tmp);

        // Save baseline
        let mut base = CascadeSettings::default();
        base.providers.anthropic = Some("keep-me".to_string());
        base.gemini_pool.proxy_port = 4444;
        save(&path, &base).unwrap();

        // Simulate partial update: change only providers via the merge helper.
        let partial = serde_json::json!({ "providers": { "anthropic": "new-key" } });
        let mut current = load(&path).unwrap();
        merge_partial(&mut current, partial);
        save(&path, &current).unwrap();

        // Reload and verify.
        let result = load(&path).unwrap();
        // providers.anthropic updated
        assert_eq!(result.providers.anthropic.as_deref(), Some("new-key"));
        // gemini_pool was NOT touched
        assert_eq!(result.gemini_pool.proxy_port, 4444);
    }

    /// Apply a top-level JSON merge into a `CascadeSettings` in place.
    ///
    /// Only keys present in `partial` are updated; all other keys are left
    /// unchanged. This mirrors the `update_settings` IPC command logic.
    fn merge_partial(settings: &mut CascadeSettings, partial: serde_json::Value) {
        if let serde_json::Value::Object(map) = partial {
            for (k, v) in map {
                match k.as_str() {
                    "schemaVersion" => {
                        if let serde_json::Value::String(s) = v {
                            settings.schema_version = s;
                        }
                    }
                    "providers" => {
                        if let Ok(p) = serde_json::from_value(v) {
                            settings.providers = p;
                        }
                    }
                    "geminiPool" | "gemini_pool" => {
                        if let Ok(g) = serde_json::from_value(v) {
                            settings.gemini_pool = g;
                        }
                    }
                    "library" => {
                        if let Ok(l) = serde_json::from_value(v) {
                            settings.library = l;
                        }
                    }
                    "context" => {
                        if let Ok(c) = serde_json::from_value(v) {
                            settings.context = c;
                        }
                    }
                    "projectMap" | "project_map" => {
                        if let Ok(pm) = serde_json::from_value(v) {
                            settings.project_map = pm;
                        }
                    }
                    "harnessBridges" | "harness_bridges" => {
                        if let Ok(hb) = serde_json::from_value(v) {
                            settings.harness_bridges = hb;
                        }
                    }
                    "hooks" => {
                        if let Ok(h) = serde_json::from_value(v) {
                            settings.hooks = h;
                        }
                    }
                    "scheduledTasks" | "scheduled_tasks" => {
                        if let Ok(st) = serde_json::from_value(v) {
                            settings.scheduled_tasks = st;
                        }
                    }
                    "plugins" => {
                        if let Ok(p) = serde_json::from_value(v) {
                            settings.plugins = p;
                        }
                    }
                    "widgets" => {
                        if let Ok(w) = serde_json::from_value(v) {
                            settings.widgets = w;
                        }
                    }
                    "mcpServers" | "mcp_servers" => {
                        if let Ok(m) = serde_json::from_value(v) {
                            settings.mcp_servers = m;
                        }
                    }
                    "vaultDisplay" | "vault_display" => {
                        if let Ok(vd) = serde_json::from_value(v) {
                            settings.vault_display = vd;
                        }
                    }
                    "telemetry" => {
                        if let Ok(t) = serde_json::from_value(v) {
                            settings.telemetry = t;
                        }
                    }
                    _ => {} // unknown keys: preserved in extra on serde round-trip
                }
            }
        }
    }
}
