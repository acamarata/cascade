//! ProviderRegistry — thread-safe registry of AI provider adapters.
//!
//! # Purpose
//!
//! Central store for all registered [`ProviderAdapter`] instances.  Callers
//! `register` adapters by ID, then `get` or `list` them.  `default_for_task`
//! consults a [`RoutingTable`] to pick the first adapter that matches a given
//! [`TaskType`].  `health_check_all` fans out concurrent health-check calls
//! across every registered adapter and collects per-provider results.
//!
//! # Inputs / Outputs
//!
//! - `register(id, adapter)` → `Ok(())` or `Err(ProviderError)` on duplicate
//! - `get(id)` → `Option<Arc<dyn ProviderAdapter + Send + Sync>>`
//! - `list()` → `Vec<ProviderInfo>` (snapshot)
//! - `health_check_all()` → `HashMap<String, Result<(), ProviderError>>`
//! - `default_for_task(task)` → `Option<Arc<dyn ProviderAdapter + Send + Sync>>`
//!
//! # Constraints
//!
//! - `RwLock` is used so concurrent reads (list, get, default_for_task) do not
//!   block each other.  Write locks are only held during `register`.
//! - Duplicate IDs are rejected with `ProviderError::UnsupportedTaskType` (the
//!   closest available variant to a "conflict" error — see guide note).
//! - `health_check_all` collects Arc clones under the read lock, then releases
//!   the lock before awaiting — no lock is held across async `.await` points.

use std::{
    collections::HashMap,
    sync::{Arc, RwLock},
};

use crate::{ProviderAdapter, ProviderError, ProviderInfo, TaskType};

// ── RoutingTable ──────────────────────────────────────────────────────────────

/// Maps each [`TaskType`] to an ordered list of provider IDs.
///
/// `default_for_task` iterates the list in order and returns the first
/// adapter that is present in the [`ProviderRegistry`].  Entries are tried
/// left to right; providers absent from the registry are skipped silently.
///
/// # Default entries
///
/// The defaults reflect the recommended priority order at P3 build time and
/// may be overridden by the settings UI (T-P3-E04-future).
pub struct RoutingTable {
    /// Maps `TaskType` → ordered priority list of provider IDs.
    pub table: HashMap<TaskType, Vec<String>>,
}

impl Default for RoutingTable {
    fn default() -> Self {
        let mut table: HashMap<TaskType, Vec<String>> = HashMap::new();

        // CascadeMerge: prefer cloud models with large context windows.
        table.insert(
            TaskType::CascadeMerge,
            vec![
                "gemini".into(),
                "openai".into(),
                "anthropic".into(),
                "local".into(),
            ],
        );

        // Chat: prefer fast OpenAI, fall back to Anthropic, Gemini, local.
        table.insert(
            TaskType::Chat,
            vec![
                "openai".into(),
                "anthropic".into(),
                "gemini".into(),
                "local".into(),
            ],
        );

        // CodeCompletion: Anthropic excels at code; local as offline fallback.
        table.insert(
            TaskType::CodeCompletion,
            vec![
                "anthropic".into(),
                "openai".into(),
                "gemini".into(),
                "local".into(),
            ],
        );

        // RagEmbed: local embedding models preferred; cloud fallback via openai.
        table.insert(
            TaskType::RagEmbed,
            vec!["local".into(), "openai".into()],
        );

        // RagRerank: local reranker preferred (latency); cloud fallback.
        table.insert(
            TaskType::RagRerank,
            vec!["local".into(), "openai".into()],
        );

        // LocalFallback: only local models apply.
        table.insert(TaskType::LocalFallback, vec!["local".into()]);

        Self { table }
    }
}

impl RoutingTable {
    /// Return the ordered priority list for `task`, or an empty slice when
    /// `task` has no entry in the table.
    pub fn priority_list(&self, task: &TaskType) -> &[String] {
        self.table
            .get(task)
            .map(Vec::as_slice)
            .unwrap_or(&[])
    }
}

// ── ProviderRegistry ──────────────────────────────────────────────────────────

/// Thread-safe registry of [`ProviderAdapter`] instances.
///
/// Wrap in an `Arc<ProviderRegistry>` to share across tasks.
///
/// ```rust,ignore
/// let registry = Arc::new(ProviderRegistry::default());
/// registry.register("noop".into(), Arc::new(NoopProvider))?;
/// let adapter = registry.get("noop");
/// ```
pub struct ProviderRegistry {
    /// Adapter store, keyed by stable provider ID.
    ///
    /// `RwLock` allows concurrent reads; writes lock exclusively.
    adapters: RwLock<HashMap<String, Arc<dyn ProviderAdapter + Send + Sync>>>,

    /// Routing table used by [`ProviderRegistry::default_for_task`].
    routing: RoutingTable,
}

impl Default for ProviderRegistry {
    fn default() -> Self {
        Self {
            adapters: RwLock::new(HashMap::new()),
            routing: RoutingTable::default(),
        }
    }
}

impl ProviderRegistry {
    /// Create a new empty registry with default routing rules.
    pub fn new() -> Self {
        Self::default()
    }

    /// Create a registry with a custom routing table.
    pub fn with_routing(routing: RoutingTable) -> Self {
        Self {
            adapters: RwLock::new(HashMap::new()),
            routing,
        }
    }

    /// Register an adapter under `id`.
    ///
    /// Returns `Err(ProviderError::UnsupportedTaskType)` when `id` is already
    /// registered.  Call `unregister` first to replace an existing entry.
    ///
    /// # Errors
    ///
    /// - `UnsupportedTaskType` — `id` is already registered.
    pub fn register(
        &self,
        id: String,
        adapter: Arc<dyn ProviderAdapter + Send + Sync>,
    ) -> Result<(), ProviderError> {
        let mut map = self
            .adapters
            .write()
            .expect("ProviderRegistry adapter lock poisoned");

        if map.contains_key(&id) {
            return Err(ProviderError::UnsupportedTaskType(format!(
                "provider '{}' is already registered; unregister it first",
                id
            )));
        }

        map.insert(id, adapter);
        Ok(())
    }

    /// Remove the adapter registered under `id`.
    ///
    /// Returns `true` when an entry was removed, `false` when `id` was unknown.
    pub fn unregister(&self, id: &str) -> bool {
        let mut map = self
            .adapters
            .write()
            .expect("ProviderRegistry adapter lock poisoned");
        map.remove(id).is_some()
    }

    /// Return the adapter registered under `id`, or `None`.
    ///
    /// The returned `Arc` clone keeps the adapter alive even if it is later
    /// unregistered.
    pub fn get(&self, id: &str) -> Option<Arc<dyn ProviderAdapter + Send + Sync>> {
        let map = self
            .adapters
            .read()
            .expect("ProviderRegistry adapter lock poisoned");
        map.get(id).cloned()
    }

    /// Return a snapshot of `ProviderInfo` for every registered adapter.
    ///
    /// Order is non-deterministic (HashMap iteration order).
    pub fn list(&self) -> Vec<ProviderInfo> {
        let map = self
            .adapters
            .read()
            .expect("ProviderRegistry adapter lock poisoned");
        map.values().map(|a| a.provider_info()).collect()
    }

    /// Fan-out concurrent health checks across all registered adapters.
    ///
    /// The read lock is acquired only to clone Arc references; it is released
    /// before any async `.await` point to avoid holding a lock across a yield.
    ///
    /// Returns a `HashMap` with one entry per registered provider.  Each entry
    /// is `Ok(())` on success or `Err(ProviderError)` on failure.
    pub async fn health_check_all(&self) -> HashMap<String, Result<(), ProviderError>> {
        // Collect (id, Arc) pairs under the read lock; release before awaiting.
        let pairs: Vec<(String, Arc<dyn ProviderAdapter + Send + Sync>)> = {
            let map = self
                .adapters
                .read()
                .expect("ProviderRegistry adapter lock poisoned");
            map.iter()
                .map(|(id, arc)| (id.clone(), arc.clone()))
                .collect()
        };

        // Fan out concurrent health checks without holding any lock.
        let futures: Vec<_> = pairs
            .into_iter()
            .map(|(id, adapter)| async move { (id, adapter.health_check().await) })
            .collect();

        let results = futures::future::join_all(futures).await;

        results.into_iter().collect()
    }

    /// Return the first adapter that matches `task` according to the routing
    /// table priority order, or `None` when no registered adapter matches.
    pub fn default_for_task(
        &self,
        task: &TaskType,
    ) -> Option<Arc<dyn ProviderAdapter + Send + Sync>> {
        let priority = self.routing.priority_list(task);
        let map = self
            .adapters
            .read()
            .expect("ProviderRegistry adapter lock poisoned");

        for id in priority {
            if let Some(arc) = map.get(id.as_str()) {
                return Some(arc.clone());
            }
        }
        None
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;
    use futures_core::Stream;
    use std::pin::Pin;
    use crate::{
        AuthMethod, CompletionRequest, CompletionResponse, ModelInfo, NoopProvider,
        ProviderCapabilities, StreamChunk,
    };

    /// A minimal mock adapter used in tests only.
    ///
    /// Every method returns `NotImplemented` (same as `NoopProvider`); `provider_info`
    /// returns a fixed ID so we can verify `list()` output.
    struct MockProvider {
        id: &'static str,
    }

    #[async_trait]
    impl ProviderAdapter for MockProvider {
        async fn complete(
            &self,
            _req: CompletionRequest,
        ) -> Result<CompletionResponse, ProviderError> {
            Err(ProviderError::NotImplemented)
        }

        async fn complete_stream(
            &self,
            _req: CompletionRequest,
        ) -> Result<
            Pin<Box<dyn Stream<Item = Result<StreamChunk, ProviderError>> + Send>>,
            ProviderError,
        > {
            Err(ProviderError::NotImplemented)
        }

        async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
            Err(ProviderError::NotImplemented)
        }

        async fn health_check(&self) -> Result<(), ProviderError> {
            Err(ProviderError::NotImplemented)
        }

        fn provider_info(&self) -> ProviderInfo {
            ProviderInfo {
                id: self.id.into(),
                name: format!("Mock({})", self.id),
                auth_method: AuthMethod::None,
                base_url: String::new(),
                capabilities: ProviderCapabilities {
                    supports_streaming: false,
                    supports_vision: false,
                    max_context_tokens: 0,
                    supports_function_calling: false,
                },
            }
        }
    }

    fn make_mock(id: &'static str) -> Arc<dyn ProviderAdapter + Send + Sync> {
        Arc::new(MockProvider { id })
    }

    // ── register / get round-trip ─────────────────────────────────────────────

    #[test]
    fn register_and_get_round_trip() {
        let registry = ProviderRegistry::new();
        registry.register("alpha".into(), make_mock("alpha")).unwrap();

        let found = registry.get("alpha");
        assert!(found.is_some(), "adapter should be retrievable after register");
        let info = found.unwrap().provider_info();
        assert_eq!(info.id, "alpha");
    }

    #[test]
    fn get_unknown_returns_none() {
        let registry = ProviderRegistry::new();
        assert!(registry.get("unknown").is_none());
    }

    // ── duplicate-id policy ───────────────────────────────────────────────────

    #[test]
    fn register_duplicate_id_is_rejected() {
        let registry = ProviderRegistry::new();
        registry.register("dup".into(), make_mock("dup")).unwrap();
        let err = registry
            .register("dup".into(), make_mock("dup"))
            .expect_err("second register with same id must fail");
        assert!(
            matches!(err, ProviderError::UnsupportedTaskType(_)),
            "expected UnsupportedTaskType, got {err:?}"
        );
    }

    // ── list ──────────────────────────────────────────────────────────────────

    #[test]
    fn list_returns_all_registered() {
        let registry = ProviderRegistry::new();
        assert!(registry.list().is_empty(), "fresh registry must be empty");

        registry.register("a".into(), make_mock("a")).unwrap();
        registry.register("b".into(), make_mock("b")).unwrap();

        let mut ids: Vec<String> = registry.list().into_iter().map(|i| i.id).collect();
        ids.sort();
        assert_eq!(ids, vec!["a", "b"]);
    }

    // ── health_check_all ──────────────────────────────────────────────────────

    #[tokio::test]
    async fn health_check_all_returns_entry_per_provider() {
        let registry = ProviderRegistry::new();
        registry.register("p1".into(), make_mock("p1")).unwrap();
        registry.register("p2".into(), make_mock("p2")).unwrap();

        let results = registry.health_check_all().await;
        assert_eq!(results.len(), 2, "must have one entry per registered adapter");

        // MockProvider.health_check returns NotImplemented.
        for (id, res) in &results {
            assert!(
                matches!(res, Err(ProviderError::NotImplemented)),
                "expected NotImplemented for {id}, got {res:?}"
            );
        }
    }

    #[tokio::test]
    async fn health_check_all_empty_registry_returns_empty_map() {
        let registry = ProviderRegistry::new();
        let results = registry.health_check_all().await;
        assert!(results.is_empty());
    }

    // ── default_for_task ──────────────────────────────────────────────────────

    #[test]
    fn default_for_task_returns_none_when_empty() {
        let registry = ProviderRegistry::new();
        let result = registry.default_for_task(&TaskType::Chat);
        assert!(result.is_none(), "empty registry must return None");
    }

    #[test]
    fn default_for_task_returns_first_matching_priority() {
        let registry = ProviderRegistry::new();
        // Default Chat priority: openai, anthropic, gemini, local.
        // Register only "anthropic" — should be returned.
        registry
            .register("anthropic".into(), make_mock("anthropic"))
            .unwrap();

        let adapter = registry
            .default_for_task(&TaskType::Chat)
            .expect("should find anthropic as fallback");
        assert_eq!(adapter.provider_info().id, "anthropic");
    }

    #[test]
    fn default_for_task_prefers_higher_priority() {
        let registry = ProviderRegistry::new();
        // Register both openai (priority 1) and anthropic (priority 2).
        registry
            .register("openai".into(), make_mock("openai"))
            .unwrap();
        registry
            .register("anthropic".into(), make_mock("anthropic"))
            .unwrap();

        let adapter = registry
            .default_for_task(&TaskType::Chat)
            .expect("should find openai");
        assert_eq!(
            adapter.provider_info().id,
            "openai",
            "openai must win over anthropic for Chat"
        );
    }

    #[test]
    fn default_for_task_unregistered_task_type_returns_none() {
        // Build a registry with an empty routing table so no task has an entry.
        let routing = RoutingTable {
            table: HashMap::new(),
        };
        let registry = ProviderRegistry::with_routing(routing);
        registry.register("local".into(), make_mock("local")).unwrap();

        // No routing entry for any TaskType → always None.
        assert!(registry.default_for_task(&TaskType::Chat).is_none());
        assert!(registry.default_for_task(&TaskType::RagEmbed).is_none());
    }

    // ── unregister ────────────────────────────────────────────────────────────

    #[test]
    fn unregister_removes_adapter() {
        let registry = ProviderRegistry::new();
        registry.register("x".into(), make_mock("x")).unwrap();
        assert!(registry.unregister("x"));
        assert!(registry.get("x").is_none());
        assert!(registry.list().is_empty());
    }

    #[test]
    fn unregister_unknown_returns_false() {
        let registry = ProviderRegistry::new();
        assert!(!registry.unregister("ghost"));
    }

    // ── concurrent access ─────────────────────────────────────────────────────

    #[tokio::test]
    async fn concurrent_list_calls_do_not_panic() {
        use std::sync::Arc as StdArc;
        use tokio::task;

        let registry = StdArc::new(ProviderRegistry::new());
        registry.register("shared".into(), make_mock("shared")).unwrap();

        let mut handles = Vec::new();
        for _ in 0..10 {
            let r = registry.clone();
            handles.push(task::spawn(async move {
                let _ = r.list();
            }));
        }
        for h in handles {
            h.await.expect("task must not panic");
        }
    }

    // ── NoopProvider in registry ──────────────────────────────────────────────

    #[test]
    fn noop_provider_registers_and_lists() {
        let registry = ProviderRegistry::new();
        registry
            .register("noop".into(), Arc::new(NoopProvider))
            .unwrap();
        let list = registry.list();
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].id, "noop");
    }
}
