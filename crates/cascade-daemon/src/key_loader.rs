//! key_loader — Load Gemini free-pool API keys with providers.json as the
//! single source of truth.
//!
//! Purpose: Provides `load_api_keys()` for daemon startup. `~/.cascade/providers.json`
//! (gemini-free-N entries) is the canonical store — it is what the live `:3761`
//! proxy `RoutingTable` reads. The OS keychain and `GEMINI_API_KEY_{n}` env vars
//! are import-only fallbacks used solely when providers.json has no gemini-free
//! entries yet; when that fallback fires, the imported keys are written into
//! providers.json (one-time migration) so all subsequent boots read the
//! canonical store directly and the fallback path is never exercised again.
//! Inputs: `~/.cascade/providers.json` (primary) or OS keychain / env vars (fallback).
//! Outputs: Vec<SecretString> — zeroized on drop.
//! Constraints: NEVER log key values. Keychain reads are wrapped in a hard
//!   timeout (~3s) so an ACL-blocked keychain entry cannot hang daemon startup
//!   (see 2026-07-02 incident: `security`-CLI-written keychain items carry an
//!   ACL that blocks the daemon's native reads).
//! SPORT: cascade-daemon key management.

use std::path::{Path, PathBuf};
use std::sync::mpsc;
use std::sync::Arc;
use std::time::Duration;

use cascade_core::providers_store::{
    read_providers_store, write_providers_store, ProviderEntry, ProvidersStore,
    PROVIDERS_STORE_SCHEMA_VERSION,
};
use cascade_keychain::{platform_keychain, Keychain, KeychainError};
use secrecy::{ExposeSecret, SecretString};

/// Hard timeout for a single keychain read. Keychain items written by the
/// `security` CLI (rather than the daemon's own writes) can carry ACLs that
/// block native reads indefinitely — this bounds the damage to ~3s per slot
/// scan rather than hanging startup forever.
const KEYCHAIN_READ_TIMEOUT: Duration = Duration::from_secs(3);

/// Prefix used for gemini-free provider ids/display names, matching the
/// existing live convention in providers.json ("gemini-free-1", "Gemini Free Key 1").
const GEMINI_FREE_ID_PREFIX: &str = "gemini-free-";
const GEMINI_FREE_DISPLAY_PREFIX: &str = "Gemini Free Key ";

// ── providers.json — canonical load path ────────────────────────────────────

/// Load Gemini free-pool keys from `providers.json`, sorted by slot number.
///
/// Purpose: canonical read path — matches exactly what the `:3761` proxy's
///   `RoutingTable` consumes, so the daemon's in-memory key map and the
///   routing table never disagree about which keys exist.
/// Inputs: `path` — path to `providers.json`.
/// Outputs: keys for every `gemini-free-N` entry whose `account_id` is
///   AIza-shaped, in ascending `N` order. Empty vec if the file is absent,
///   unparseable, or has no gemini-free entries.
/// Constraints: does not filter on `enabled`/`local_ok` — callers that need
///   routing-eligible keys should build a `RoutingTable` instead; this
///   function loads the full known key set for keychain-map wiring.
pub fn load_from_providers_store(path: &Path) -> Vec<SecretString> {
    let store = match read_providers_store(path) {
        Ok(s) => s,
        Err(_) => return Vec::new(),
    };
    gemini_free_keys_sorted(&store)
}

/// Extract and sort gemini-free-N keys from an already-loaded store.
fn gemini_free_keys_sorted(store: &ProvidersStore) -> Vec<SecretString> {
    let mut entries: Vec<&ProviderEntry> = store
        .providers
        .iter()
        .filter(|p| {
            p.harness == "gemini"
                && p.id.starts_with(GEMINI_FREE_ID_PREFIX)
                && is_aiza_shaped(&p.account_id)
        })
        .collect();
    entries.sort_by_key(|p| gemini_free_slot_number(&p.id));
    entries
        .into_iter()
        .map(|p| SecretString::new(p.account_id.clone()))
        .collect()
}

/// Parse the trailing `N` out of a `gemini-free-N` id for stable sort order.
/// Falls back to `u32::MAX` (sorts last) for malformed ids so a bad entry
/// never silently reorders the well-formed ones.
fn gemini_free_slot_number(id: &str) -> u32 {
    id.strip_prefix(GEMINI_FREE_ID_PREFIX)
        .and_then(|n| n.parse::<u32>().ok())
        .unwrap_or(u32::MAX)
}

/// Shape-validate a Google API key: `AIza` prefix, reasonable minimum length.
/// Mirrors the check in `cascade_core::accounts_store::collect_gfp_keys_from_path`.
fn is_aiza_shaped(v: &str) -> bool {
    v.starts_with("AIza") && v.len() >= 30
}

// ── Keychain fallback (import-only) ──────────────────────────────────────────

/// Load API keys from the provided keychain implementation, guarded by a hard
/// per-slot timeout so an ACL-blocked entry cannot hang the caller.
///
/// Purpose: Testable core — inject any Keychain (including InMemoryKeychain in
///   tests, or a mock that blocks to exercise the timeout path).
/// Inputs: `kc` — shared handle to any Keychain implementation (`Arc` so the
///   bounded reader can hand it to a detached thread).
/// Outputs: Vec of SecretString keys loaded from "dev.cascade" / "gemini_key_{n}".
/// Constraints: Breaks on first NotFound or first timeout; warns on other
///   errors; never logs key values.
pub fn load_from_keychain(kc: Arc<dyn Keychain>) -> Vec<SecretString> {
    let mut keys = Vec::new();
    for n in 1..=28 {
        match read_keychain_slot_with_timeout(Arc::clone(&kc), n) {
            KeychainSlotResult::Found(k) => keys.push(SecretString::new(k)),
            KeychainSlotResult::NotFound => break,
            KeychainSlotResult::TimedOut => {
                tracing::warn!(
                    slot = n,
                    "keychain read timed out after {:?} (ACL-blocked entry?) — \
                     stopping keychain scan, falling back to env",
                    KEYCHAIN_READ_TIMEOUT
                );
                break;
            }
            KeychainSlotResult::Error(e) => {
                tracing::warn!("keychain read error for slot {n}: {e}");
            }
        }
    }
    keys
}

/// Outcome of a single bounded keychain read.
enum KeychainSlotResult {
    Found(String),
    NotFound,
    TimedOut,
    Error(KeychainError),
}

/// Read one `dev.cascade` / `gemini_key_{n}` slot with a hard timeout.
///
/// # ACL-hang defense
/// Keychain entries written by the `security` CLI (rather than the daemon's
/// own `set_key`) can carry access-control lists that cause the daemon's
/// native read call to block indefinitely waiting for a UI prompt that will
/// never be answered (headless daemon, no interactive session). This
/// happened live on 2026-07-02 and hung proxy startup.
///
/// The blocking call runs on a **detached** thread (not a scoped one — a
/// scoped thread would be joined at scope exit, so a truly hung OS call
/// would block the caller anyway and defeat the timeout). On timeout the
/// detached thread is abandoned; it holds only the `Arc` handle and exits
/// whenever the OS call finally returns, so nothing else leaks.
fn read_keychain_slot_with_timeout(kc: Arc<dyn Keychain>, n: u32) -> KeychainSlotResult {
    let (tx, rx) = mpsc::channel();
    std::thread::spawn(move || {
        let result = kc.get_key("dev.cascade", &format!("gemini_key_{n}"));
        // Ignore send errors — happens only if the receiver already timed
        // out and dropped; the thread still exits cleanly either way.
        let _ = tx.send(result);
    });
    match rx.recv_timeout(KEYCHAIN_READ_TIMEOUT) {
        Ok(Ok(k)) => KeychainSlotResult::Found(k),
        Ok(Err(KeychainError::NotFound)) => KeychainSlotResult::NotFound,
        Ok(Err(e)) => KeychainSlotResult::Error(e),
        Err(_) => KeychainSlotResult::TimedOut,
    }
}

/// Load API keys from `GEMINI_API_KEY_{n}` environment variables.
fn load_from_env() -> Vec<SecretString> {
    let mut keys = Vec::new();
    for n in 1..=28 {
        match std::env::var(format!("GEMINI_API_KEY_{n}")) {
            Ok(v) => keys.push(SecretString::new(v)),
            Err(_) => break,
        }
    }
    keys
}

// ── Boot-time permission hardening ───────────────────────────────────────────

/// Best-effort: tighten `providers.json` to `0o600` (owner read/write only)
/// if it exists with looser permissions.
///
/// Purpose: `providers.json` is the plaintext-secret source of truth (Gemini
///   free-pool keys, etc.); [`write_providers_store`] has always written new
///   copies at `0o600` (tmp-perms-before-rename), but a file created before
///   that hardening landed, or written by some other tool, may still carry
///   the default umask perms (typically `0o644`). This runs once at daemon
///   boot to close that gap for pre-existing files without waiting for the
///   next write.
/// Inputs: `path` — providers.json path.
/// Outputs: none. Logs at INFO when permissions were actually tightened;
///   silent no-op when the file is absent, already `0o600`, or on non-Unix.
/// Constraints: best-effort only — a metadata/chmod failure (e.g. no
///   permission, unsupported filesystem) is silently ignored so it can never
///   block daemon startup.
#[cfg(unix)]
fn tighten_providers_store_permissions(path: &Path) {
    use std::os::unix::fs::PermissionsExt;

    let Ok(metadata) = std::fs::metadata(path) else {
        return; // absent — nothing to tighten.
    };
    let mode = metadata.permissions().mode() & 0o777;
    if mode != 0o600
        && std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600)).is_ok()
    {
        tracing::info!(
            path = %path.display(),
            previous_mode = format!("{mode:o}"),
            "tightened providers.json permissions to 0600"
        );
    }
}

/// Non-Unix no-op — permission bits are not modeled the same way; skip.
#[cfg(not(unix))]
fn tighten_providers_store_permissions(_path: &Path) {}

// ── Migration: write imported fallback keys back into providers.json ────────

/// Write freshly-imported fallback keys into `providers.json` as gemini-free-N
/// entries, starting after the highest existing gemini-free slot number.
///
/// Purpose: one-time migration so a keychain/env fallback only ever fires
///   once per key — subsequent boots find these keys in providers.json and
///   never touch the keychain/env path again.
///
/// # Migration rule (never resurrect a revoked key)
///
/// This is called only when `load_from_providers_store` returned zero
/// gemini-free entries for this boot — but "zero gemini-free entries" is
/// ambiguous: it could mean "never migrated yet" (safe to import) or "the
/// user intentionally deleted every gemini-free entry to revoke them" (must
/// NOT re-import — that would silently resurrect revoked keys forever).
///
/// The honest rule this function enforces: **only migrate when
/// `providers.json` is absent, unreadable/unparseable, or contains zero
/// provider entries of ANY kind** (a truly fresh store). If the file exists
/// and parses with at least one provider entry — of any harness — that is
/// treated as proof the store has been intentionally curated at least once,
/// so an empty gemini-free subset means "revoked," not "never set up," and
/// no fallback import happens.
///
/// Even when migration is permitted, entries are also deduped **by key
/// value** against every existing `account_id` in the store (not just
/// AIza-shaped gemini-free ones) so a key that already exists anywhere in
/// the store — under any id/slot — is never appended a second time. This
/// bounds growth even if this function is ever called with a mix of
/// already-known and genuinely-new keys.
///
/// Inputs: `path` — providers.json path; `keys` — imported keys, in order.
/// Outputs: appends new `ProviderEntry` rows (harness "gemini", source
///   "vault", enabled + local_ok true) and writes atomically. Best-effort —
///   failures are logged and non-fatal (the caller already has the keys in
///   memory for this boot; migration just improves the next boot).
/// `true` when providers.json exists, parses, and holds at least one
/// provider entry of ANY kind — proof the store has been intentionally
/// curated at least once (see the never-resurrect rule above).
fn store_is_curated(path: &Path) -> bool {
    match read_providers_store(path) {
        Ok(s) => !s.providers.is_empty(),
        Err(_) => false,
    }
}

fn migrate_fallback_keys_into_providers_store(path: &Path, keys: &[SecretString]) {
    if keys.is_empty() {
        return;
    }

    let existing_store = read_providers_store(path);

    // Never-resurrect rule: only migrate into a truly fresh store (absent,
    // unreadable, or zero provider entries of any kind). A store that exists
    // and has at least one entry has been intentionally curated — an empty
    // gemini-free subset in that case means "revoked," not "never set up."
    let store_is_fresh = match &existing_store {
        Ok(s) => s.providers.is_empty(),
        Err(_) => true,
    };
    if !store_is_fresh {
        tracing::info!(
            "providers.json exists with curated entries but no gemini-free keys — \
             treating as intentional revocation, skipping fallback import"
        );
        return;
    }

    let mut store = existing_store.unwrap_or_else(|_| ProvidersStore {
        schema_version: PROVIDERS_STORE_SCHEMA_VERSION,
        updated_at: chrono::Utc::now().to_rfc3339(),
        providers: Vec::new(),
    });

    // Dedup by value: skip any imported key whose value already exists
    // anywhere in the store (any harness/id), not just AIza-shaped
    // gemini-free slots — this is what makes repeated boots idempotent.
    // Owned Strings (not &str) so the set does not borrow `store.providers`
    // while the loop below pushes into it (E0502).
    let existing_values: std::collections::HashSet<String> = store
        .providers
        .iter()
        .map(|p| p.account_id.clone())
        .collect();

    let next_slot_start = store
        .providers
        .iter()
        .filter(|p| p.harness == "gemini" && p.id.starts_with(GEMINI_FREE_ID_PREFIX))
        .map(|p| gemini_free_slot_number(&p.id))
        .filter(|n| *n != u32::MAX)
        .max()
        .map(|n| n + 1)
        .unwrap_or(1);

    let mut appended = 0u32;
    let mut slot = next_slot_start;
    for key in keys {
        let value = key.expose_secret();
        if existing_values.contains(value.as_str()) {
            continue; // already present under some id — never duplicate.
        }
        store.providers.push(ProviderEntry {
            id: format!("{GEMINI_FREE_ID_PREFIX}{slot}"),
            harness: "gemini".to_string(),
            account_id: value.to_owned(),
            display_name: format!("{GEMINI_FREE_DISPLAY_PREFIX}{slot}"),
            auth_kind: "ApiKey".to_string(),
            enabled: true,
            source: "vault".to_string(),
            local_ok: true,
        });
        slot += 1;
        appended += 1;
    }

    if appended == 0 {
        return; // nothing new — avoid a needless write + updated_at bump.
    }

    store.updated_at = chrono::Utc::now().to_rfc3339();

    match write_providers_store(path, &store) {
        Ok(()) => tracing::info!(
            imported = appended,
            "migrated fallback Gemini keys into providers.json (one-time)"
        ),
        Err(e) => tracing::warn!(%e, "failed to migrate fallback keys into providers.json"),
    }
}

// ── Public entry point ───────────────────────────────────────────────────────

/// Default location of `~/.cascade/providers.json`.
fn default_providers_path() -> PathBuf {
    dirs::home_dir()
        .unwrap_or_else(|| PathBuf::from("/tmp"))
        .join(".cascade")
        .join("providers.json")
}

/// Load API keys at daemon startup.
///
/// Purpose: Production entry point. providers.json is the canonical source —
///   read first. If it has no gemini-free entries yet, falls back to the OS
///   keychain (timeout-guarded) then `GEMINI_API_KEY_{n}` env vars, and on a
///   successful fallback writes the imported keys into providers.json so
///   every subsequent boot reads the canonical store directly.
/// Outputs: Vec of SecretString keys; logs count and source at INFO level.
/// Constraints: Never logs key values. Never blocks on a hung keychain read
///   past `KEYCHAIN_READ_TIMEOUT`.
// Called in main.rs under #[cfg(feature = "gemini-proxy")] — warning is a feature-gate FP.
#[allow(dead_code)]
pub fn load_api_keys() -> Vec<SecretString> {
    load_api_keys_from_path(&default_providers_path())
}

/// Testable variant of [`load_api_keys`] accepting an explicit providers.json path.
pub fn load_api_keys_from_path(providers_path: &Path) -> Vec<SecretString> {
    tighten_providers_store_permissions(providers_path);

    let from_store = load_from_providers_store(providers_path);
    if !from_store.is_empty() {
        tracing::info!(
            count = from_store.len(),
            source = "providers.json",
            "API keys loaded"
        );
        return from_store;
    }

    // Never-resurrect rule, applied to LOADING as well as migration: a
    // store that exists and has been curated (>=1 provider entry of any
    // kind) with zero gemini-free entries means "revoked", not "never set
    // up" — the fallbacks must not resurrect revoked keys even in memory
    // for this boot. Same rule `migrate_fallback_keys_into_providers_store`
    // enforces for the write-back (kept there too as defense-in-depth).
    if store_is_curated(providers_path) {
        tracing::info!(
            "providers.json curated with zero gemini-free entries — \
             treating as intentional revocation, skipping key fallbacks"
        );
        return Vec::new();
    }

    // Fallback: keychain first, then env. Both are import-only — a
    // successful fallback triggers a one-time write-back into providers.json.
    let kc: Arc<dyn Keychain> = Arc::from(platform_keychain());
    let mut keys = load_from_keychain(kc);
    let mut source = "OS keychain fallback";
    if keys.is_empty() {
        keys = load_from_env();
        source = "vault.env fallback";
    }

    if !keys.is_empty() {
        migrate_fallback_keys_into_providers_store(providers_path, &keys);
    }

    tracing::info!(count = keys.len(), source, "API keys loaded");
    keys
}

/// Whether the daemon should advise the user to run `cascade migrate-keys`.
///
/// Purpose: nudge users still holding plaintext keys in vault.env to migrate
///   them into providers.json — but only when migration is actually pending
///   (providers.json has no gemini-free entries AND vault.env-style env vars
///   are present).
/// Outputs: `true` when providers.json has no gemini-free keys but
///   `GEMINI_API_KEY_1` is set in the environment; `false` otherwise.
/// Constraints: reads no secret values; safe to call at startup.
pub fn should_advise_migration() -> bool {
    should_advise_migration_from_path(&default_providers_path())
}

/// Testable variant of [`should_advise_migration`] accepting an explicit providers.json path.
fn should_advise_migration_from_path(providers_path: &Path) -> bool {
    let store_empty = load_from_providers_store(providers_path).is_empty();
    let vault_has_keys = std::env::var("GEMINI_API_KEY_1").is_ok();
    store_empty && vault_has_keys
}

#[cfg(test)]
mod tests {
    use super::*;
    // The `Keychain` trait must be in scope to call its methods (set_key/get_key)
    // on the concrete `InMemoryKeychain` — unlike the `&dyn Keychain` call sites
    // above, which dispatch through the trait object without needing the import.
    use cascade_core::providers_store::write_providers_store;
    use cascade_keychain::InMemoryKeychain;
    use tempfile::TempDir;

    fn make_store(entries: Vec<ProviderEntry>) -> ProvidersStore {
        ProvidersStore {
            schema_version: PROVIDERS_STORE_SCHEMA_VERSION,
            updated_at: "2026-07-02T00:00:00Z".to_string(),
            providers: entries,
        }
    }

    fn gemini_free_entry(n: u32, key: &str, enabled: bool, local_ok: bool) -> ProviderEntry {
        ProviderEntry {
            id: format!("gemini-free-{n}"),
            harness: "gemini".to_string(),
            account_id: key.to_string(),
            display_name: format!("Gemini Free Key {n}"),
            auth_kind: "ApiKey".to_string(),
            enabled,
            source: "vault".to_string(),
            local_ok,
        }
    }

    // ── providers.json-first load ────────────────────────────────────────

    #[test]
    fn load_from_providers_store_reads_gemini_free_entries_sorted() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        let key1 = "AIzaFixtureAAAAAAAAAAAAAAAAAAAAAA1";
        let key2 = "AIzaFixtureAAAAAAAAAAAAAAAAAAAAAA2";
        // Written out of order to prove sort-by-slot-number.
        let store = make_store(vec![
            gemini_free_entry(2, key2, true, true),
            gemini_free_entry(1, key1, true, true),
        ]);
        write_providers_store(&path, &store).unwrap();

        let keys = load_from_providers_store(&path);
        assert_eq!(keys.len(), 2);
        assert_eq!(keys[0].expose_secret(), key1);
        assert_eq!(keys[1].expose_secret(), key2);
    }

    #[test]
    fn load_from_providers_store_skips_non_aiza_shaped() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        let store = make_store(vec![gemini_free_entry(1, "not-a-real-key", true, true)]);
        write_providers_store(&path, &store).unwrap();

        assert!(load_from_providers_store(&path).is_empty());
    }

    #[test]
    fn load_from_providers_store_missing_file_returns_empty() {
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("nonexistent.json");
        assert!(load_from_providers_store(&path).is_empty());
    }

    // These tests mutate / depend on process-global GEMINI_API_KEY_* env vars,
    // so each takes ENV_TEST_LOCK — without it they race each other (and any
    // other env-mutating test) under the parallel test runner.
    #[test]
    fn load_api_keys_from_path_prefers_providers_store_over_fallback() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        let key1 = "AIzaFixtureAAAAAAAAAAAAAAAAAAAAAA1";
        let store = make_store(vec![gemini_free_entry(1, key1, true, true)]);
        write_providers_store(&path, &store).unwrap();

        // No env vars set, no keychain used — proves the store path alone is sufficient.
        let keys = load_api_keys_from_path(&path);
        assert_eq!(keys.len(), 1);
        assert_eq!(keys[0].expose_secret(), key1);
    }

    // ── fallback import + write-back ─────────────────────────────────────

    #[test]
    fn fallback_import_writes_back_into_providers_store() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        // providers.json exists but has zero gemini-free entries.
        let store = make_store(vec![]);
        write_providers_store(&path, &store).unwrap();

        std::env::set_var("GEMINI_API_KEY_1", "AIzaFixtureEnvAAAAAAAAAAAAAAAA1");
        std::env::set_var("GEMINI_API_KEY_2", "AIzaFixtureEnvAAAAAAAAAAAAAAAA2");
        std::env::remove_var("GEMINI_API_KEY_3");

        let keys = load_api_keys_from_path(&path);
        assert_eq!(keys.len(), 2, "should import both env-fallback keys");

        // Second call must now read providers.json directly (migration landed).
        let reloaded = read_providers_store(&path).unwrap();
        let gfp: Vec<_> = reloaded
            .providers
            .iter()
            .filter(|p| p.id.starts_with("gemini-free-"))
            .collect();
        assert_eq!(
            gfp.len(),
            2,
            "fallback keys must be migrated into providers.json"
        );
        assert!(gfp.iter().all(|p| p.enabled && p.local_ok));

        std::env::remove_var("GEMINI_API_KEY_1");
        std::env::remove_var("GEMINI_API_KEY_2");
    }

    #[test]
    fn migrate_appends_after_highest_existing_slot() {
        // Slot numbering must still increment correctly for a multi-key
        // migration batch into a genuinely fresh (empty) store.
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        write_providers_store(&path, &make_store(vec![])).unwrap();

        let imported = vec![
            SecretString::new("AIzaFixtureImportedAAAAAAAAAAAA1".to_string()),
            SecretString::new("AIzaFixtureImportedAAAAAAAAAAAA2".to_string()),
        ];
        migrate_fallback_keys_into_providers_store(&path, &imported);

        let reloaded = read_providers_store(&path).unwrap();
        assert!(
            reloaded.providers.iter().any(|p| p.id == "gemini-free-1"),
            "first imported key must take slot 1 in a fresh store"
        );
        assert!(
            reloaded.providers.iter().any(|p| p.id == "gemini-free-2"),
            "second imported key must continue numbering to slot 2"
        );
    }

    // ── never-resurrect + dedup-by-value rules ───────────────────────────

    #[test]
    fn repeated_boot_is_idempotent_no_duplicate_entries() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        // Two consecutive load_api_keys_from_path calls with the same env
        // fallback keys present must not create duplicate provider entries —
        // dedup-by-value must hold even across separate migration calls.
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        write_providers_store(&path, &make_store(vec![])).unwrap();

        std::env::set_var("GEMINI_API_KEY_1", "AIzaFixtureIdemAAAAAAAAAAAAAAAA1");
        std::env::remove_var("GEMINI_API_KEY_2");

        let first = load_api_keys_from_path(&path);
        assert_eq!(first.len(), 1);

        // Second boot: providers.json now has the migrated key, so the
        // providers-store-first path should short-circuit and the fallback
        // should never re-fire — but exercise migrate directly too, to prove
        // dedup-by-value holds even if it were called again with the same
        // fallback key.
        let second = load_api_keys_from_path(&path);
        assert_eq!(second.len(), 1, "second boot must read the same single key");

        migrate_fallback_keys_into_providers_store(
            &path,
            &[SecretString::new(
                "AIzaFixtureIdemAAAAAAAAAAAAAAAA1".to_string(),
            )],
        );

        let reloaded = read_providers_store(&path).unwrap();
        let gfp_count = reloaded
            .providers
            .iter()
            .filter(|p| p.id.starts_with(GEMINI_FREE_ID_PREFIX))
            .count();
        assert_eq!(
            gfp_count, 1,
            "dedup-by-value must prevent a duplicate entry for the same key value"
        );

        std::env::remove_var("GEMINI_API_KEY_1");
    }

    #[test]
    fn revocation_is_respected_no_import_when_store_has_other_providers_but_no_gemini_free() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        // The store exists and has been curated (one non-gemini-free entry)
        // but deliberately has zero gemini-free entries — this must be read
        // as intentional revocation, not "never set up," so no fallback
        // import may occur even though GEMINI_API_KEY_1 is set.
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        let curated_entry = ProviderEntry {
            id: "cc-cc-default".to_string(),
            harness: "cc".to_string(),
            account_id: "cc-default".to_string(),
            display_name: "Claude Code".to_string(),
            auth_kind: "OAuthToken".to_string(),
            enabled: true,
            source: "manual".to_string(),
            local_ok: true,
        };
        write_providers_store(&path, &make_store(vec![curated_entry])).unwrap();

        std::env::set_var("GEMINI_API_KEY_1", "AIzaFixtureRevokedAAAAAAAAAAAAA1");
        std::env::remove_var("GEMINI_API_KEY_2");

        let keys = load_api_keys_from_path(&path);
        assert!(
            keys.is_empty(),
            "revoked gemini-free keys must not be resurrected from env fallback"
        );

        let reloaded = read_providers_store(&path).unwrap();
        assert!(
            !reloaded
                .providers
                .iter()
                .any(|p| p.id.starts_with(GEMINI_FREE_ID_PREFIX)),
            "no gemini-free entry may be written back when revocation is intentional"
        );

        std::env::remove_var("GEMINI_API_KEY_1");
    }

    // ── keychain timeout path ────────────────────────────────────────────

    /// A mock keychain whose `get_key` blocks forever on slot 1 to exercise
    /// the ACL-hang defense: the scan must stop (not hang) within the timeout.
    struct BlockingKeychain;

    impl Keychain for BlockingKeychain {
        fn get_key(&self, _service: &str, account: &str) -> Result<String, KeychainError> {
            if account == "gemini_key_1" {
                std::thread::sleep(Duration::from_secs(3600));
                unreachable!("test must not wait for the full sleep");
            }
            Err(KeychainError::NotFound)
        }
        fn set_key(&self, _s: &str, _a: &str, _v: &str) -> Result<(), KeychainError> {
            Ok(())
        }
        fn delete_key(&self, _s: &str, _a: &str) -> Result<(), KeychainError> {
            Ok(())
        }
        fn list_keys(&self, _s: &str) -> Result<Vec<String>, KeychainError> {
            Ok(vec![])
        }
    }

    #[test]
    fn keychain_read_timeout_does_not_hang() {
        let kc: Arc<dyn Keychain> = Arc::new(BlockingKeychain);
        let start = std::time::Instant::now();
        let keys = load_from_keychain(kc);
        let elapsed = start.elapsed();

        assert!(keys.is_empty(), "blocked slot yields no keys");
        assert!(
            elapsed < Duration::from_secs(10),
            "keychain scan must return well under the 3600s block, got {:?}",
            elapsed
        );
    }

    #[test]
    fn load_from_keychain_returns_injected_keys() {
        let kc = InMemoryKeychain::new();
        for n in 1..=3 {
            cascade_keychain::Keychain::set_key(
                &kc,
                "dev.cascade",
                &format!("gemini_key_{n}"),
                &format!("secret{n}"),
            )
            .unwrap();
        }
        let keys = load_from_keychain(Arc::new(kc));
        assert_eq!(keys.len(), 3);
    }

    #[test]
    fn load_from_keychain_stops_at_not_found() {
        let kc = InMemoryKeychain::new();
        cascade_keychain::Keychain::set_key(&kc, "dev.cascade", "gemini_key_1", "s1").unwrap();
        cascade_keychain::Keychain::set_key(&kc, "dev.cascade", "gemini_key_2", "s2").unwrap();
        let keys = load_from_keychain(Arc::new(kc));
        assert_eq!(keys.len(), 2);
    }

    #[test]
    fn load_from_keychain_empty_keychain_returns_empty() {
        let kc = InMemoryKeychain::new();
        let keys = load_from_keychain(Arc::new(kc));
        assert!(keys.is_empty());
    }

    // ── should_advise_migration ──────────────────────────────────────────

    #[test]
    fn should_advise_migration_true_when_store_empty_and_env_set() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        write_providers_store(&path, &make_store(vec![])).unwrap();

        std::env::set_var("GEMINI_API_KEY_1", "AIzaFixtureAdviseAAAAAAAAAAAAAA1");
        assert!(should_advise_migration_from_path(&path));
        std::env::remove_var("GEMINI_API_KEY_1");
    }

    #[test]
    fn should_advise_migration_false_when_store_has_keys() {
        let _env_guard = crate::test_support::ENV_TEST_LOCK
            .lock()
            .unwrap_or_else(|e| e.into_inner());
        let tmp = TempDir::new().unwrap();
        let path = tmp.path().join("providers.json");
        let store = make_store(vec![gemini_free_entry(
            1,
            "AIzaFixtureAdviseAAAAAAAAAAAAAA2",
            true,
            true,
        )]);
        write_providers_store(&path, &store).unwrap();

        std::env::set_var("GEMINI_API_KEY_1", "AIzaFixtureAdviseAAAAAAAAAAAAAA3");
        assert!(!should_advise_migration_from_path(&path));
        std::env::remove_var("GEMINI_API_KEY_1");
    }
}
