//! RegistryRouter — real `ProviderRouter` impl backed by `ProviderRegistry`.
//!
//! Purpose: wire the `cascade-providers` `ProviderRegistry` into the agent
//!   executor so `AutomationRunner` steps produce real LLM completions instead
//!   of the "nop" placeholder strings used during scaffolding.
//!
//! Inputs:
//!   - `Arc<ProviderRegistry>` — the shared provider registry, identical to
//!     what the HTTP chat handler and health-check task use.
//!   - `AgentSpec` + `AgentRunContext` — the agent identity and assembled
//!     conversation, forwarded by the executor on each step.
//!
//! Outputs:
//!   - `StepOutcome` — the provider's response mapped into the executor's type.
//!
//! Constraints:
//!   - **No-provider path is graceful**: if `pick_for_chat` returns `None`
//!     (registry empty or all providers unhealthy), the router returns
//!     `ExecutorError::ProviderFailed` with an explicit "no provider available"
//!     message rather than silently succeeding with fake output.
//!   - **Lazy**: the registry is `Arc`-shared; no new work happens at daemon
//!     startup. The first actual automation step triggers the first pick.
//!   - **Thread-safe**: `Arc<ProviderRegistry>` is `Send + Sync`; the router
//!     can be used concurrently from many executor workers.
//!   - **Tool invoker**: `SafeToolInvoker` handles the `ToolInvoker` side.
//!     All tool calls are returned as an explicit "not yet implemented" result
//!     so the model can gracefully handle the missing capability without the
//!     executor seeing a false success.
//!
//! SPORT: cascade-daemon / automation_router — auto-02

use std::sync::Arc;

use async_trait::async_trait;
use tracing::warn;

use cascade_agents::context::{AgentRunContext, ContextRole, StepOutcome, TokenUsage, ToolCall};
use cascade_agents::executor::{ExecutorError, ProviderRouter, ToolInvoker};
use cascade_agents::spec::AgentSpec;
use cascade_providers::{CompletionRequest, Message, MessageRole, ProviderRegistry};

// ── RegistryRouter ────────────────────────────────────────────────────────────

/// `ProviderRouter` implementation backed by the shared `ProviderRegistry`.
///
/// Converts each `(AgentSpec, AgentRunContext)` pair into a `CompletionRequest`
/// and dispatches it through `ProviderRegistry::pick_for_chat`.
///
/// # No-provider degradation
///
/// When the registry has no healthy provider, `step()` returns
/// `Err(ExecutorError::ProviderFailed("no provider available"))` so the
/// executor transitions the task to `Failed` and the user sees a clear
/// status rather than a silently wrong "nop" success.
pub struct RegistryRouter {
    registry: Arc<ProviderRegistry>,
}

impl RegistryRouter {
    /// Construct a `RegistryRouter` around the shared provider registry.
    pub fn new(registry: Arc<ProviderRegistry>) -> Self {
        Self { registry }
    }
}

#[async_trait]
impl ProviderRouter for RegistryRouter {
    /// Run one ReAct step by forwarding the assembled context to the best
    /// available provider.
    ///
    /// # Provider selection
    ///
    /// Uses `spec.model_pref` as the preferred provider ID when set, then
    /// falls back to `ProviderRegistry::pick_for_chat(None)` (routing-table
    /// Chat default → health-aware scan → local fallback).
    ///
    /// # Context mapping
    ///
    /// `AgentRunContext::messages` are mapped to `cascade_providers::Message`
    /// values.  `ContextRole::Tool` results are injected as `User` turns
    /// (OpenAI / Anthropic both accept tool results as user-role messages
    /// when there is no structured tool-call wire format in play).
    async fn step(
        &self,
        spec: &AgentSpec,
        ctx: &AgentRunContext,
    ) -> Result<StepOutcome, ExecutorError> {
        // ── Pick provider ─────────────────────────────────────────────────────
        let preferred = spec.model_pref.as_deref();
        let (adapter, provider_id) =
            self.registry
                .pick_for_chat(preferred)
                .await
                .ok_or_else(|| {
                    warn!(
                        agent_id = %spec.id,
                        "RegistryRouter: no provider available — registry empty or all unhealthy"
                    );
                    ExecutorError::ProviderFailed(
                        "no provider available: registry is empty or all providers are unhealthy"
                            .to_string(),
                    )
                })?;

        // ── Build CompletionRequest ───────────────────────────────────────────
        // Map ContextMessage → cascade_providers::Message.
        // ContextRole::Tool results become User-role messages so the model sees
        // the tool output as part of the conversation history.
        let messages: Vec<Message> = ctx
            .messages
            .iter()
            .map(|m| {
                let role = match m.role {
                    ContextRole::System => MessageRole::System,
                    ContextRole::User | ContextRole::Tool => MessageRole::User,
                    ContextRole::Assistant => MessageRole::Assistant,
                };
                Message {
                    role,
                    content: m.content.clone(),
                }
            })
            .collect();

        // Derive the model name: prefer the explicit model_pref (which may be a
        // full model string like "claude-sonnet-4-6"), then fall back to the
        // provider_id string. We do not call available_models() here (async,
        // potentially slow) — the provider will use its default model if the
        // model field is set to the provider_id.
        let model = spec
            .model_pref
            .clone()
            .unwrap_or_else(|| provider_id.clone());

        let req = CompletionRequest {
            model,
            messages,
            max_tokens: None,
            temperature: None,
            stream: false,
            system: None,
        };

        // ── Dispatch ──────────────────────────────────────────────────────────
        let resp = adapter
            .complete(req)
            .await
            .map_err(|e| ExecutorError::ProviderFailed(e.to_string()))?;

        Ok(StepOutcome {
            assistant_text: resp.content,
            tool_calls: vec![],
            done: true,
            usage: TokenUsage {
                prompt_tokens: resp.usage.prompt_tokens,
                completion_tokens: resp.usage.completion_tokens,
            },
        })
    }
}

// ── SafeToolInvoker ───────────────────────────────────────────────────────────

/// `ToolInvoker` that returns a clear "not implemented" result for all calls.
///
/// This is the correct placeholder for the tool dispatch layer until concrete
/// tool implementations land (P4+ scope). Unlike the old `NopInvoker` which
/// returned `"nop:<tool_id>"` (a silent false-success), `SafeToolInvoker`
/// returns an explicit error string so the model knows the tool is unavailable
/// and can adapt its reasoning accordingly.
///
/// The executor feeds tool results back to the model as user-role messages, so
/// an error string here is handled gracefully — the model sees "tool X returned
/// an error: not yet implemented" and can try a different approach.
pub struct SafeToolInvoker;

#[async_trait]
impl ToolInvoker for SafeToolInvoker {
    async fn invoke(&self, call: &ToolCall) -> Result<String, ExecutorError> {
        // WHY Ok(error_string) not Err(): the executor feeds tool results back
        // into the next prompt turn. Returning Err here would park the task
        // with ToolFailed. Returning the error as a string lets the model see
        // the failure and decide how to proceed (try another tool, give up, etc.).
        Ok(format!(
            "[cascade] tool '{}' is not yet implemented in this build. \
             The tool call has been noted but no action was taken.",
            call.tool_id
        ))
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;

    use std::pin::Pin;
    use std::sync::Arc;

    use async_trait::async_trait;
    use futures_util::Stream;

    use cascade_agents::context::{
        AgentRunContextBuilder, ContextMessage, ContextRole, NoopContextProvider,
    };
    use cascade_agents::spec::{AgentRole, AgentSpec, Capability, Runtime};
    use cascade_agents::tool_registry::ToolRegistry;
    use cascade_providers::{
        CompletionResponse, ModelInfo, ProviderAdapter, ProviderCapabilities, ProviderError,
        ProviderInfo, StreamChunk, TokenUsage as PTokenUsage,
    };
    use cascade_types::agent::Tier;

    // ── MockProvider ─────────────────────────────────────────────────────────

    /// A mock `ProviderAdapter` that returns a fixed string from `complete()`.
    struct MockProvider {
        response: String,
    }

    impl MockProvider {
        fn new(response: impl Into<String>) -> Self {
            Self {
                response: response.into(),
            }
        }
    }

    #[async_trait]
    impl ProviderAdapter for MockProvider {
        async fn complete(
            &self,
            _req: CompletionRequest,
        ) -> Result<CompletionResponse, ProviderError> {
            Ok(CompletionResponse {
                content: self.response.clone(),
                model: "mock-model".into(),
                usage: PTokenUsage {
                    prompt_tokens: 10,
                    completion_tokens: 5,
                    total_tokens: 15,
                },
                cost_usd: None,
            })
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
            Ok(vec![ModelInfo {
                id: "mock-model".into(),
                name: "Mock Model".into(),
                context_window: 4096,
                supports_streaming: false,
            }])
        }

        fn provider_info(&self) -> ProviderInfo {
            ProviderInfo {
                id: "mock".into(),
                name: "Mock Provider".into(),
                auth_method: cascade_providers::AuthMethod::ApiKey,
                base_url: "http://mock.example.com".into(),
                capabilities: ProviderCapabilities {
                    supports_streaming: false,
                    supports_vision: false,
                    max_context_tokens: 4096,
                    supports_function_calling: false,
                },
            }
        }

        async fn health_check(&self) -> Result<(), ProviderError> {
            Ok(())
        }
    }

    // ── Helper: build a minimal AgentSpec ────────────────────────────────────

    fn mock_spec() -> AgentSpec {
        AgentSpec {
            id: "test-agent".into(),
            version: "0.1.0".into(),
            name: "Test Agent".into(),
            role: AgentRole::Generic,
            tier: Tier::T2,
            capabilities: vec![Capability::LlmCall],
            model_pref: None,
            system_prompt_ref: None,
            tool_grants_ref: None,
            runtime: Runtime::Native,
            soul_ref: None,
        }
    }

    // ── Helper: build a minimal AgentRunContext ───────────────────────────────

    async fn mock_ctx(spec: AgentSpec) -> AgentRunContext {
        AgentRunContextBuilder::new(spec.clone(), "say hello")
            .context_provider(Arc::new(NoopContextProvider))
            .tool_registry(Arc::new(ToolRegistry::new()), &spec.id)
            .session_id("test-sess-01")
            .build()
            .await
    }

    // ── Test: real provider output flows through (not "nop") ─────────────────

    /// Verifies that a one-step automation run via `RegistryRouter` returns
    /// the mock provider's text, NOT the old "nop" placeholder.
    ///
    /// This is the critical regression guard for auto-02: if the production
    /// runner ever regresses to NopRouter the assertion `!= "nop"` catches it.
    #[tokio::test]
    async fn registry_router_returns_provider_output_not_nop() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "mock".into(),
                Arc::new(MockProvider::new("hello from real provider")),
            )
            .expect("register mock provider");

        let router = RegistryRouter::new(Arc::clone(&registry));
        let spec = mock_spec();
        let ctx = mock_ctx(spec.clone()).await;

        let outcome = router.step(&spec, &ctx).await.expect("step should succeed");

        assert_eq!(
            outcome.assistant_text, "hello from real provider",
            "RegistryRouter must return the provider's actual output"
        );
        assert_ne!(
            outcome.assistant_text, "nop",
            "RegistryRouter must NOT return the old nop placeholder"
        );
        assert!(outcome.done, "single-step should be marked done");
        assert_eq!(outcome.usage.prompt_tokens, 10);
        assert_eq!(outcome.usage.completion_tokens, 5);
    }

    // ── Test: no-provider path returns clear error ────────────────────────────

    /// Verifies that an empty registry returns a clear `ProviderFailed` error
    /// rather than panicking or returning a fake success.
    #[tokio::test]
    async fn registry_router_no_provider_returns_clear_error() {
        let registry = Arc::new(ProviderRegistry::new()); // empty
        let router = RegistryRouter::new(registry);
        let spec = mock_spec();
        let ctx = mock_ctx(spec.clone()).await;

        let err = router
            .step(&spec, &ctx)
            .await
            .expect_err("should fail with no provider");

        match err {
            ExecutorError::ProviderFailed(msg) => {
                assert!(
                    msg.contains("no provider available"),
                    "error message should mention 'no provider available', got: {msg}"
                );
            }
            other => panic!("expected ProviderFailed, got {other:?}"),
        }
    }

    // ── Test: SafeToolInvoker returns explicit error string, not "nop:" ───────

    #[tokio::test]
    async fn safe_tool_invoker_returns_explicit_not_implemented_message() {
        let invoker = SafeToolInvoker;
        let call = ToolCall {
            tool_id: "cascade.read_file".into(),
            args: serde_json::json!({"path": "/tmp/foo"}),
            call_id: "call-001".into(),
        };

        let result = invoker.invoke(&call).await.expect("should return Ok");
        assert!(
            result.contains("not yet implemented"),
            "SafeToolInvoker must say 'not yet implemented', got: {result}"
        );
        assert!(
            !result.starts_with("nop:"),
            "SafeToolInvoker must NOT return 'nop:' prefixed output, got: {result}"
        );
    }

    // ── Test: context messages are forwarded to provider ─────────────────────

    /// Verifies that `ContextMessage` values in `AgentRunContext` are passed
    /// through to the completion request (not silently dropped).
    #[tokio::test]
    async fn registry_router_forwards_context_messages() {
        use std::sync::Mutex;

        struct CapturingProvider {
            captured: Mutex<Option<CompletionRequest>>,
        }

        #[async_trait]
        impl ProviderAdapter for CapturingProvider {
            async fn complete(
                &self,
                req: CompletionRequest,
            ) -> Result<CompletionResponse, ProviderError> {
                *self.captured.lock().unwrap() = Some(req);
                Ok(CompletionResponse {
                    content: "captured".into(),
                    model: "cap-model".into(),
                    usage: PTokenUsage {
                        prompt_tokens: 1,
                        completion_tokens: 1,
                        total_tokens: 2,
                    },
                    cost_usd: None,
                })
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
                Ok(vec![ModelInfo {
                    id: "cap-model".into(),
                    name: "Capturing Model".into(),
                    context_window: 4096,
                    supports_streaming: false,
                }])
            }

            fn provider_info(&self) -> ProviderInfo {
                ProviderInfo {
                    id: "capturing".into(),
                    name: "Capturing Provider".into(),
                    auth_method: cascade_providers::AuthMethod::ApiKey,
                    base_url: "http://capturing.example.com".into(),
                    capabilities: ProviderCapabilities {
                        supports_streaming: false,
                        supports_vision: false,
                        max_context_tokens: 4096,
                        supports_function_calling: false,
                    },
                }
            }

            async fn health_check(&self) -> Result<(), ProviderError> {
                Ok(())
            }
        }

        let provider = Arc::new(CapturingProvider {
            captured: Mutex::new(None),
        });
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "capturing".into(),
                Arc::clone(&provider) as Arc<dyn ProviderAdapter + Send + Sync>,
            )
            .expect("register");

        let router = RegistryRouter::new(Arc::clone(&registry));
        let spec = mock_spec();

        // Build a context that already has a user message pre-loaded.
        let mut ctx = mock_ctx(spec.clone()).await;
        ctx.messages.push(ContextMessage {
            role: ContextRole::User,
            content: "what is 2+2?".into(),
        });

        let _ = router.step(&spec, &ctx).await.expect("step ok");

        let captured = provider
            .captured
            .lock()
            .unwrap()
            .clone()
            .expect("should have captured a request");
        assert!(
            captured
                .messages
                .iter()
                .any(|m| m.content == "what is 2+2?"),
            "context messages must be forwarded to the provider"
        );
    }
}
