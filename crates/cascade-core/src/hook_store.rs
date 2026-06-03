//! CRUD store for HookDef records in the SQLite events.db `hooks` table.
//!
//! Inputs:  Arc<Mutex<Connection>> shared with other stores; HookDef/HookEvent from cascade-types.
//! Outputs: HookDef lists and individual lookups; loaded from config.toml [[hooks]] arrays.
//! Constraints: Upsert uses (name, tier) natural key for idempotent config.toml reloads.
//!   Higher-tier hooks appear first in list_all() results (GCI < PCI < ... < PAC ordering).
//! SPORT: .claude/docs/MASTER-MODULES.md — hook_store entry.

use cascade_types::{
    hook::{HookAction, HookConfigEntry, HookDef, HookEvent},
    CascadeError, Result,
};
use rusqlite::{params, Connection, OptionalExtension};
use std::sync::{Arc, Mutex};
use uuid::Uuid;

/// HookStore manages CRUD operations on hook definitions in SQLite.
///
/// Hooks are loaded from each tier's `config.toml` `[[hooks]]` array at daemon
/// startup. The (name, tier) pair is the natural upsert key so re-loading is
/// idempotent. Higher-tier hooks appear first in `list_all()` results.
pub struct HookStore {
    conn: Arc<Mutex<Connection>>,
}

impl HookStore {
    /// Create a new HookStore from a shared database connection.
    pub fn new(conn: Arc<Mutex<Connection>>) -> Self {
        Self { conn }
    }

    /// Create an in-memory HookStore with the `hooks` schema applied.
    ///
    /// Convenience constructor for daemon startup and tests — skips the caller
    /// needing to open a Connection and run the DDL manually.
    ///
    /// # Errors
    ///
    /// Returns [`CascadeError::Other`] if the in-memory SQLite DB cannot be opened
    /// or the schema DDL fails (both are fatal startup conditions).
    pub fn in_memory() -> Result<Self> {
        let conn = Connection::open(":memory:")
            .map_err(|e| CascadeError::Other(format!("HookStore::in_memory open: {e}")))?;
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS hooks (
                id          TEXT    PRIMARY KEY,
                name        TEXT    NOT NULL,
                tier        TEXT    NOT NULL DEFAULT '',
                event_json  TEXT    NOT NULL,
                action_json TEXT    NOT NULL,
                enabled     INTEGER NOT NULL DEFAULT 1
            );
            CREATE UNIQUE INDEX IF NOT EXISTS idx_hooks_name_tier ON hooks(name, tier);",
        )
        .map_err(|e| CascadeError::Other(format!("HookStore::in_memory schema: {e}")))?;
        Ok(Self {
            conn: Arc::new(Mutex::new(conn)),
        })
    }

    /// Insert a new hook definition.
    pub fn insert(&self, hook: &HookDef) -> Result<()> {
        self.insert_with_tier(hook, "")
    }

    /// Insert a hook associated with a specific tier name (e.g. "GCI", "PRC").
    ///
    /// Using the tier name allows `list_all()` to sort GCI hooks first.
    pub fn insert_with_tier(&self, hook: &HookDef, tier: &str) -> Result<()> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let event_json = serde_json::to_string(&hook.event)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize event: {}", e)))?;

        let action_json = serde_json::to_string(&hook.action)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize action: {}", e)))?;

        conn.execute(
            "INSERT INTO hooks (id, name, tier, event_json, action_json, enabled)
             VALUES (?, ?, ?, ?, ?, ?)",
            params![
                hook.id.to_string(),
                &hook.name,
                tier,
                &event_json,
                &action_json,
                hook.enabled as i32,
            ],
        )
        .map_err(|e| CascadeError::Other(format!("Failed to insert hook: {}", e)))?;

        Ok(())
    }

    /// Upsert a hook by (name, tier) natural key.
    ///
    /// Used when loading `[[hooks]]` from config.toml on daemon startup or regen.
    /// If a hook with the same (name, tier) already exists, its fields are updated.
    pub fn upsert(&self, hook: &HookDef, tier: &str) -> Result<()> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let event_json = serde_json::to_string(&hook.event)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize event: {}", e)))?;

        let action_json = serde_json::to_string(&hook.action)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize action: {}", e)))?;

        conn.execute(
            "INSERT INTO hooks (id, name, tier, event_json, action_json, enabled)
             VALUES (?, ?, ?, ?, ?, ?)
             ON CONFLICT(name, tier) DO UPDATE SET
                id         = excluded.id,
                event_json  = excluded.event_json,
                action_json = excluded.action_json,
                enabled     = excluded.enabled",
            params![
                hook.id.to_string(),
                &hook.name,
                tier,
                &event_json,
                &action_json,
                hook.enabled as i32,
            ],
        )
        .map_err(|e| CascadeError::Other(format!("Failed to upsert hook: {}", e)))?;

        Ok(())
    }

    /// Get a hook by its UUID.
    pub fn get(&self, id: Uuid) -> Result<Option<HookDef>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let mut stmt = conn
            .prepare(
                "SELECT id, name, event_json, action_json, enabled
                 FROM hooks WHERE id = ?",
            )
            .map_err(|e| CascadeError::Other(format!("Failed to prepare statement: {}", e)))?;

        let hook = stmt
            .query_row([id.to_string()], |row| {
                let event_json: String = row.get(2)?;
                let action_json: String = row.get(3)?;
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    event_json,
                    action_json,
                    row.get::<_, i32>(4)?,
                ))
            })
            .optional()
            .map_err(|e| CascadeError::Other(format!("Query failed: {}", e)))?;

        match hook {
            None => Ok(None),
            Some((id_str, name, event_json, action_json, enabled)) => {
                let event: HookEvent = serde_json::from_str(&event_json).map_err(|e| {
                    CascadeError::Other(format!("Failed to deserialize event: {}", e))
                })?;
                let action: HookAction = serde_json::from_str(&action_json).map_err(|e| {
                    CascadeError::Other(format!("Failed to deserialize action: {}", e))
                })?;
                Ok(Some(HookDef {
                    id: Uuid::parse_str(&id_str)
                        .map_err(|e| CascadeError::Other(format!("Invalid UUID: {}", e)))?,
                    name,
                    event,
                    action,
                    enabled: enabled != 0,
                }))
            }
        }
    }

    /// List all hooks, ordered by tier precedence (GCI first, PAC last).
    ///
    /// The ordering ensures that when the execution engine iterates hooks for a
    /// given event, higher-priority tier hooks fire first.
    pub fn list_all(&self) -> Result<Vec<HookDef>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        // Order by tier in GCI-first precedence: GCI < PCI < APC < PPC < PRC < PAC.
        // SQLite CASE expression maps tier name to sort key.
        let mut stmt = conn
            .prepare(
                "SELECT id, name, event_json, action_json, enabled
                 FROM hooks
                 ORDER BY
                   CASE tier
                     WHEN 'GCI' THEN 0
                     WHEN 'PCI' THEN 1
                     WHEN 'APC' THEN 2
                     WHEN 'PPC' THEN 3
                     WHEN 'PRC' THEN 4
                     WHEN 'PAC' THEN 5
                     ELSE 6
                   END ASC,
                   name ASC",
            )
            .map_err(|e| CascadeError::Other(format!("Failed to prepare statement: {}", e)))?;

        self.collect_hooks(&mut stmt, [])
    }

    /// List all enabled hooks that match the given event.
    ///
    /// Returns hooks in tier-precedence order (GCI first).
    pub fn list_for_event(&self, event: &HookEvent) -> Result<Vec<HookDef>> {
        // Load all enabled hooks and filter by event match in Rust.
        // This avoids JSON expression-index complexity while keeping the
        // performance acceptable for the expected hook count (<100).
        let all = self.list_enabled()?;
        Ok(all
            .into_iter()
            .filter(|h| event.matches(&h.event))
            .collect())
    }

    /// List all enabled hooks.
    pub fn list_enabled(&self) -> Result<Vec<HookDef>> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let mut stmt = conn
            .prepare(
                "SELECT id, name, event_json, action_json, enabled
                 FROM hooks
                 WHERE enabled = 1
                 ORDER BY
                   CASE tier
                     WHEN 'GCI' THEN 0
                     WHEN 'PCI' THEN 1
                     WHEN 'APC' THEN 2
                     WHEN 'PPC' THEN 3
                     WHEN 'PRC' THEN 4
                     WHEN 'PAC' THEN 5
                     ELSE 6
                   END ASC,
                   name ASC",
            )
            .map_err(|e| CascadeError::Other(format!("Failed to prepare statement: {}", e)))?;

        self.collect_hooks(&mut stmt, [])
    }

    /// Update an existing hook by ID.
    pub fn update(&self, hook: &HookDef) -> Result<()> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        let event_json = serde_json::to_string(&hook.event)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize event: {}", e)))?;

        let action_json = serde_json::to_string(&hook.action)
            .map_err(|e| CascadeError::Other(format!("Failed to serialize action: {}", e)))?;

        conn.execute(
            "UPDATE hooks
             SET name = ?, event_json = ?, action_json = ?, enabled = ?
             WHERE id = ?",
            params![
                &hook.name,
                &event_json,
                &action_json,
                hook.enabled as i32,
                hook.id.to_string(),
            ],
        )
        .map_err(|e| CascadeError::Other(format!("Failed to update hook: {}", e)))?;

        Ok(())
    }

    /// Delete a hook by UUID.
    pub fn delete(&self, id: Uuid) -> Result<()> {
        let conn = self
            .conn
            .lock()
            .map_err(|_| CascadeError::Other("Failed to acquire database lock".into()))?;

        conn.execute("DELETE FROM hooks WHERE id = ?", [id.to_string()])
            .map_err(|e| CascadeError::Other(format!("Failed to delete hook: {}", e)))?;

        Ok(())
    }

    /// Load hooks from a tier's `[[hooks]]` config.toml array entries and upsert
    /// them into the store.
    ///
    /// `entries` comes from deserializing the tier's `config.toml`.
    /// `tier_name` is the string name (e.g. "GCI", "PRC") for ordering.
    pub fn load_from_config(
        &self,
        entries: Vec<HookConfigEntry>,
        tier_name: &str,
    ) -> Result<Vec<String>> {
        let mut errors = Vec::new();

        for entry in entries {
            let name = entry.name.clone();
            match HookDef::try_from(entry) {
                Ok(hook) => {
                    if let Err(e) = self.upsert(&hook, tier_name) {
                        errors.push(format!("Hook '{}': {}", name, e));
                    }
                }
                Err(e) => {
                    errors.push(format!("Hook '{}' parse error: {}", name, e));
                }
            }
        }

        Ok(errors)
    }

    // ── Internal helpers ──────────────────────────────────────────────────────

    fn collect_hooks<P: rusqlite::Params>(
        &self,
        stmt: &mut rusqlite::Statement<'_>,
        params: P,
    ) -> Result<Vec<HookDef>> {
        let rows = stmt
            .query_map(params, |row| {
                let event_json: String = row.get(2)?;
                let action_json: String = row.get(3)?;
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    event_json,
                    action_json,
                    row.get::<_, i32>(4)?,
                ))
            })
            .map_err(|e| CascadeError::Other(format!("Query failed: {}", e)))?;

        let mut hooks = Vec::new();
        for row in rows {
            let (id_str, name, event_json, action_json, enabled) =
                row.map_err(|e| CascadeError::Other(format!("Row error: {}", e)))?;

            let event: HookEvent = serde_json::from_str(&event_json)
                .map_err(|e| CascadeError::Other(format!("Bad event JSON in '{}': {}", name, e)))?;
            let action: HookAction = serde_json::from_str(&action_json).map_err(|e| {
                CascadeError::Other(format!("Bad action JSON in '{}': {}", name, e))
            })?;

            hooks.push(HookDef {
                id: Uuid::parse_str(&id_str)
                    .map_err(|e| CascadeError::Other(format!("Invalid UUID: {}", e)))?,
                name,
                event,
                action,
                enabled: enabled != 0,
            });
        }

        Ok(hooks)
    }
}

// ── Test helpers ──────────────────────────────────────────────────────────────

#[cfg(test)]
pub(crate) fn setup_test_db() -> Result<(Arc<Mutex<Connection>>, HookStore)> {
    let conn = Connection::open(":memory:")
        .map_err(|e| CascadeError::Other(format!("Failed to open memory db: {}", e)))?;

    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS hooks (
            id          TEXT    PRIMARY KEY,
            name        TEXT    NOT NULL,
            tier        TEXT    NOT NULL DEFAULT '',
            event_json  TEXT    NOT NULL,
            action_json TEXT    NOT NULL,
            enabled     INTEGER NOT NULL DEFAULT 1
        );
        CREATE UNIQUE INDEX IF NOT EXISTS idx_hooks_name_tier ON hooks(name, tier);",
    )
    .map_err(|e| CascadeError::Other(format!("Failed to create table: {}", e)))?;

    let conn_arc = Arc::new(Mutex::new(conn));
    let store = HookStore::new(conn_arc.clone());
    Ok((conn_arc, store))
}

#[cfg(test)]
mod tests {
    use super::*;
    use cascade_types::hook::{HookAction, HookEvent};

    fn make_log_hook(name: &str, event: HookEvent) -> HookDef {
        HookDef::new(
            name,
            event,
            HookAction::LogMessage {
                level: "info".to_string(),
                message: format!("Hook '{}' fired", name),
            },
        )
    }

    // ── CRUD ──────────────────────────────────────────────────────────────────

    #[test]
    fn test_insert_and_get() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let hook = make_log_hook("test-hook", HookEvent::RegenComplete);
        let id = hook.id;
        store.insert(&hook)?;

        let retrieved = store.get(id)?;
        assert!(retrieved.is_some());
        let h = retrieved.unwrap();
        assert_eq!(h.name, "test-hook");
        assert!(matches!(h.event, HookEvent::RegenComplete));
        Ok(())
    }

    #[test]
    fn test_update_hook() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let mut hook = make_log_hook("to-update", HookEvent::DaemonStart);
        let id = hook.id;
        store.insert(&hook)?;

        hook.name = "updated-name".to_string();
        hook.enabled = false;
        store.update(&hook)?;

        let h = store.get(id)?.expect("exists");
        assert_eq!(h.name, "updated-name");
        assert!(!h.enabled);
        Ok(())
    }

    #[test]
    fn test_delete_hook() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let hook = make_log_hook("to-delete", HookEvent::BackupComplete);
        let id = hook.id;
        store.insert(&hook)?;
        assert!(store.get(id)?.is_some());

        store.delete(id)?;
        assert!(store.get(id)?.is_none());
        Ok(())
    }

    // ── list_for_event ────────────────────────────────────────────────────────

    #[test]
    fn test_list_for_event_returns_only_matching() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        store.insert(&make_log_hook("hook-regen-1", HookEvent::RegenComplete))?;
        store.insert(&make_log_hook("hook-regen-2", HookEvent::RegenComplete))?;
        store.insert(&make_log_hook("hook-start", HookEvent::DaemonStart))?;
        store.insert(&make_log_hook("hook-cascade", HookEvent::CascadeChanged))?;

        let regen_hooks = store.list_for_event(&HookEvent::RegenComplete)?;
        assert_eq!(regen_hooks.len(), 2, "Expected 2 RegenComplete hooks");
        for h in &regen_hooks {
            assert!(matches!(h.event, HookEvent::RegenComplete));
        }

        let start_hooks = store.list_for_event(&HookEvent::DaemonStart)?;
        assert_eq!(start_hooks.len(), 1);

        Ok(())
    }

    // ── config loading + tier ordering ────────────────────────────────────────

    #[test]
    fn test_load_from_config_parses_all_tiers() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        // Simulate 6 tiers each contributing 1 hook.
        let tiers = [
            ("GCI", HookEvent::DaemonStart),
            ("PCI", HookEvent::RegenComplete),
            ("APC", HookEvent::CascadeChanged),
            ("PPC", HookEvent::BackupComplete),
            ("PRC", HookEvent::DaemonStop),
            ("PAC", HookEvent::RegenComplete),
        ];

        for (tier, event) in &tiers {
            let entry = cascade_types::hook::HookConfigEntry {
                name: format!("{}-hook", tier.to_lowercase()),
                event: cascade_types::hook::HookEventShorthand::Full(event.clone()),
                action: HookAction::LogMessage {
                    level: "info".to_string(),
                    message: format!("{} fired", tier),
                },
                enabled: true,
            };
            let errors = store.load_from_config(vec![entry], tier)?;
            assert!(errors.is_empty(), "Errors for tier {}: {:?}", tier, errors);
        }

        // All 6 hooks loaded.
        let all = store.list_all()?;
        assert_eq!(all.len(), 6, "Expected 6 hooks from 6 tiers");

        // GCI hook appears first.
        assert_eq!(all[0].name, "gci-hook");

        Ok(())
    }

    #[test]
    fn test_upsert_is_idempotent() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        let hook = make_log_hook("idempotent-hook", HookEvent::RegenComplete);
        store.upsert(&hook, "PRC")?;
        store.upsert(&hook, "PRC")?; // Second upsert must not error or duplicate.

        let all = store.list_all()?;
        assert_eq!(all.len(), 1, "Upsert must not duplicate");
        Ok(())
    }

    #[test]
    fn test_gci_hooks_appear_first() -> Result<()> {
        let (_conn, store) = setup_test_db()?;

        // Insert in reverse precedence order.
        store.upsert(&make_log_hook("pac-hook", HookEvent::DaemonStart), "PAC")?;
        store.upsert(&make_log_hook("prc-hook", HookEvent::DaemonStart), "PRC")?;
        store.upsert(&make_log_hook("gci-hook", HookEvent::DaemonStart), "GCI")?;

        let all = store.list_all()?;
        assert_eq!(all[0].name, "gci-hook", "GCI hook must appear first");
        assert_eq!(all[2].name, "pac-hook", "PAC hook must appear last");
        Ok(())
    }
}
