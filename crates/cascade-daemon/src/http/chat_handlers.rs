//! Purpose: POST /api/chat SSE handler — routes streaming chat requests through
//!   the ProviderRegistry instead of a hardcoded Gemini proxy URL.
//!
//! # Selection order (E-P7-07)
//!
//!   1. `provider` field in the request body — explicit user selection.
//!   2. `ProviderRegistry::default_for_task(Chat)` — routing-table default.
//!   3. First healthy cloud provider (health-checked concurrently).
//!   4. First registered local:<id> provider — offline fallback.
//!   5. Typed SSE error event — nothing available.
//!
//! # Outputs (text/event-stream)
//!
//!   - `event: served_by` — which provider backend served the request.
//!   - `event: token`     — streamed text fragment `{"text":"…"}`.
//!   - `event: tool_result` — tool execution result (T-P3-E02-19 seam).
//!   - `event: error`    — provider or routing error (not HTTP 500).
//!   - `data: [DONE]`    — final sentinel (no event type).
//!
//! # Constraints
//!
//!   - Never exposes vault keys or API keys to the browser.
//!   - DashboardState.provider_registry == None → typed error SSE (no panic).
//!   - SPORT: T-P3-E02-18 · MASTER-ENDPOINTS.md § POST /api/chat

use axum::{
    extract::State,
    response::{
        sse::{Event, KeepAlive, Sse},
        IntoResponse,
    },
    routing::post,
    Json, Router,
};
use cascade_core::model_ids::{MODEL_CLAUDE_HAIKU, MODEL_GPT};
use cascade_providers::{CompletionRequest, Message, MessageRole};
use futures_util::StreamExt;
use serde::Deserialize;
use serde_json::{json, Value};
use std::collections::HashSet;
use std::convert::Infallible;
use std::path::{Path, PathBuf};
use std::sync::Arc;
use tokio::io::AsyncReadExt;
use tokio_stream::wrappers::ReceiverStream;

use crate::dashboard::DashboardState;
use cascade_types::model_ids::MODEL_GEMINI_FLASH_LATEST;

// ── Request types ─────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize)]
pub struct ChatMessage {
    pub role: String,
    pub content: String,
}

#[derive(Debug, Deserialize)]
pub struct ChatRequest {
    pub messages: Vec<ChatMessage>,
    #[serde(default)]
    pub tools: Vec<String>,
    pub session_id: Option<String>,
    /// Optional explicit provider id (e.g. "gemini", "anthropic", "local:ollama").
    /// When present and registered, this provider is used directly without
    /// consulting the routing table.
    pub provider: Option<String>,
    /// Optional model override within the selected provider.
    pub model: Option<String>,
    /// Optional chat namespace sent by the app (e.g. `"personal"`,
    /// `"personal:private"`, `"projects:<id>"`, `"meta"`). Previously absent
    /// from this struct, so serde silently DROPPED it and private/personal
    /// chats were middleware-processed like any other request. Personal and
    /// private namespaces now neutralise every content-capturing/GP-touching
    /// middleware flag — see [`effective_middleware_flags`].
    pub namespace: Option<String>,
}

// ── Tool dispatch seam (implemented in T-P3-E02-19) ─────────────────────────

/// Dispatch a tool call by name with its arguments.
/// Delegates to the chat_tools catalog (T-P3-E02-19).  Returns a JSON Value
/// so the SSE handler can embed the result verbatim in a `tool_result` event.
// Tool dispatch seam — wired into chat_handler in T-P3-E02-19.
#[allow(dead_code)]
async fn dispatch_tool(name: &str, args: &Value) -> Value {
    let result = super::chat_tools::dispatch(name, args).await;
    serde_json::to_value(&result)
        .unwrap_or_else(|e| json!({ "error": format!("serialisation error: {e}"), "tool": name }))
}

// ── Router ───────────────────────────────────────────────────────────────────

/// Returns the router for /api/chat.
pub fn router() -> Router<DashboardState> {
    Router::new().route("/", post(chat_handler))
}

// ── Handler ───────────────────────────────────────────────────────────────────

pub async fn chat_handler(
    State(state): State<DashboardState>,
    Json(body): Json<ChatRequest>,
) -> impl IntoResponse {
    // Channel capacity: 256 buffered SSE events is sufficient for burst text.
    let (tx, rx) = tokio::sync::mpsc::channel::<Result<Event, Infallible>>(256);

    tokio::spawn(async move {
        // ── Step 1: resolve the provider registry ─────────────────────────────
        let registry = match state.provider_registry {
            Some(ref r) => Arc::clone(r),
            None => {
                let ev = Event::default()
                    .event("error")
                    .data(json!({ "message": "no provider registry configured" }).to_string());
                let _ = tx.send(Ok(ev)).await;
                let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;
                return;
            }
        };

        // ── Step 2: build provider fallback list ──────────────────────────────
        let preferred = body.provider.as_deref();
        let gp_preferred: Option<String> =
            if gp_preference_allowed(preferred, body.namespace.as_deref()) {
                chat_gp_preference().await.map(str::to_string)
            } else {
                None
            };
        // User preference (project_cascade_chat_design, 2026-07): personal chat
        // MAY use the fleet — Gemini/GPT are good at personal chat, and the user
        // has no local model (no RAM/disk). Only an EXPLICIT vault / `:private`
        // namespace stays trusted-only (Anthropic/local); plain `personal` and
        // `personal:topic:*` route by the normal fleet priority
        // (GF -> GP -> Codex -> A2 -> A1 -> OC-Go). Protected classification
        // still governs middleware neutralisation below, so no content-capturing
        // side channels run for personal chat.
        let requires_trusted = namespace_requires_trusted(body.namespace.as_deref());
        // Future refinement: detect vault-secret content even when the caller
        // did not use a vault/private namespace; today the trusted-only gate is
        // intentionally namespace-based.
        let requested_first = preferred.or(gp_preferred.as_deref());
        let quota = load_quota_saturation().await;
        let candidates =
            provider_fallback_candidates(&registry, requested_first, requires_trusted, &quota);

        if candidates.is_empty() {
            // Fail CLOSED for vault/private namespaces: with only untrusted
            // (Google/OpenAI-family) providers registered, refusing is the
            // promise — never silently fall back to the pool.
            let msg = if requires_trusted {
                "private chat requires a trusted provider (Claude account or local \
                 model) — refusing to send this conversation to an external pool. \
                 Configure an Anthropic provider or start a local model."
            } else {
                "no provider available — configure a provider or start a local model"
            };
            let ev = Event::default()
                .event("error")
                .data(json!({ "message": msg }).to_string());
            let _ = tx.send(Ok(ev)).await;
            let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;
            return;
        }

        // ── Step 3.5: E2-S2 pre-middleware (flag-gated; ALL OFF by default) ──
        // With every [middleware] flag false this block does nothing at all:
        // no GP calls, no file reads, zero added latency on the hot path.
        // Quality guard: every middleware failure falls back to the original
        // request unchanged — savings never degrade correctness.
        // Privacy guard: personal/private namespaces neutralise every flag
        // that captures conversation content or ships it to the GP pool.
        let flags = effective_middleware_flags(state.middleware, body.namespace.as_deref());
        // "Explicit" = the user named a provider and/or model in the request;
        // neither classification nor GP-backed compression may then send the
        // conversation anywhere the user did not point it (a user who pinned
        // local:ollama to keep data local must not have old turns summarised
        // via Google).
        let explicit_choice = body.provider.is_some() || body.model.is_some();

        // 1. Context compression (middleware.compress_context): GP-summarize
        //    old turns past the token threshold, keep the last N verbatim.
        //    Skipped entirely on explicit provider/model choice — the GP
        //    summary call would route content through a provider the user
        //    deliberately did not pick.
        let mut chat_messages: Vec<ChatMessage> = body.messages;
        if flags.compress_context && !explicit_choice {
            chat_messages = compress_chat_messages(chat_messages).await;
        }

        // 2. System-prompt injection: project context remains flag-gated;
        //    personal-family namespaces always get a bounded local summary
        //    from ~/Downloads/.claude so personal topic chat has recall.
        let mut prompt_context = if flags.inject_context {
            load_project_context().await
        } else {
            cascade_core::middleware::ProjectContext::default()
        };
        if let Some(personal_context) = load_personal_context(body.namespace.as_deref()).await {
            append_prompt_context(&mut prompt_context.claude_md_summary, personal_context);
        }
        let system = cascade_core::middleware::build_system_prefix(&prompt_context);

        // 3. Request classification (middleware.classify_requests): one
        //    bounded GP call; may only DOWNGRADE the model, and only when the
        //    user did not explicitly pick a provider/model. GP failure →
        //    None → the normal default model applies. With provider fallback,
        //    classify at most once when any remaining candidate can consume
        //    the downgrade result.
        let mut classified_complexity = None;
        if flags.classify_requests
            && !explicit_choice
            && candidates
                .iter()
                .any(|c| provider_can_downgrade(&c.provider_id))
        {
            let last_user = chat_messages
                .iter()
                .rev()
                .find(|m| m.role == "user")
                .map(|m| m.content.clone());
            if let Some(prompt_text) = last_user {
                let complexity = tokio::task::spawn_blocking(move || {
                    cascade_core::middleware::classify_prompt(&prompt_text)
                })
                .await
                .ok()
                .flatten();
                classified_complexity = complexity;
            }
        }

        // ── Step 4: build provider-neutral messages from the chat body ───────
        let messages: Vec<Message> = chat_messages
            .iter()
            .map(|m| {
                let role = match m.role.as_str() {
                    "assistant" => MessageRole::Assistant,
                    "system" => MessageRole::System,
                    _ => MessageRole::User,
                };
                Message {
                    role,
                    content: m.content.clone(),
                }
            })
            .collect();

        // ── Step 5: open a provider stream, falling through request errors ───
        // E2-S3 post-middleware: with middleware.context_sync on, accumulate
        // the assistant text while streaming so a digest can be synced AFTER
        // the response is delivered. Flag off (default) → no accumulation, no
        // spawn — literally zero overhead on the hot path.
        let sync_context = crate::context_sync::should_sync(&flags);
        let mut assistant_text = String::new();
        let mut failures = Vec::new();
        let mut opened = None;
        for candidate in candidates {
            let model = body
                .model
                .clone()
                .or_else(|| {
                    classified_complexity
                        .and_then(|c| downgraded_model_for(&candidate.provider_id, c))
                })
                .unwrap_or_else(|| default_model_for(&candidate.provider_id));
            let completion_req = CompletionRequest {
                model,
                messages: messages.clone(),
                max_tokens: None,
                temperature: None,
                stream: true,
                system: system.clone(),
            };

            match candidate.adapter.complete_stream(completion_req).await {
                Ok(stream) => {
                    opened = Some((candidate.provider_id, stream));
                    break;
                }
                Err(e) => {
                    failures.push(format!("{}: {e}", candidate.provider_id));
                }
            }
        }

        let Some((provider_id, mut stream)) = opened else {
            let detail = if failures.is_empty() {
                "all providers unavailable".to_string()
            } else {
                failures.join("; ")
            };
            let ev = Event::default()
                .event("error")
                .data(json!({ "message": format!("provider error: {detail}") }).to_string());
            let _ = tx.send(Ok(ev)).await;
            let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;
            return;
        };

        // The UI displays the backend that actually accepted the request.
        let served_ev = Event::default()
            .event("served_by")
            .data(json!({ "provider": provider_id }).to_string());
        let _ = tx.send(Ok(served_ev)).await;

        // Stall-guard: a provider stream can OPEN then hang (e.g. a flaky GP
        // pool key) with no chunk and no error, which would hang the chat
        // forever behind SSE keep-alives. Cap the wait for each chunk; on
        // timeout, emit a typed error and stop rather than hang.
        const CHUNK_STALL_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(20);
        loop {
            let chunk_result = match tokio::time::timeout(CHUNK_STALL_TIMEOUT, stream.next()).await
            {
                Ok(Some(cr)) => cr,
                Ok(None) => break, // stream ended cleanly
                Err(_) => {
                    let ev = Event::default().event("error").data(
                        json!({ "message": format!("stream timeout: {provider_id} stalled (no data in 20s)") })
                            .to_string(),
                    );
                    let _ = tx.send(Ok(ev)).await;
                    break;
                }
            };
            match chunk_result {
                Err(e) => {
                    let ev = Event::default()
                        .event("error")
                        .data(json!({ "message": format!("stream error: {e}") }).to_string());
                    let _ = tx.send(Ok(ev)).await;
                    break;
                }
                Ok(chunk) => {
                    if !chunk.delta.is_empty() {
                        if sync_context {
                            assistant_text.push_str(&chunk.delta);
                        }
                        let ev = Event::default()
                            .event("token")
                            .data(json!({ "text": chunk.delta }).to_string());
                        let _ = tx.send(Ok(ev)).await;
                    }
                    if chunk.finish_reason.is_some() {
                        break;
                    }
                }
            }
        }

        // Final sentinel — always emitted.
        let _ = tx.send(Ok(Event::default().data("[DONE]"))).await;

        // ── Step 6: E2-S3 post-middleware (background, AFTER responding) ─────
        // The digest extraction + JSONL append + rag_watcher nudge all happen
        // on a detached background task; the client already has its [DONE].
        if sync_context {
            crate::context_sync::spawn_context_sync(flags, assistant_text);
        }
    });

    let sse_stream = ReceiverStream::new(rx);
    Sse::new(sse_stream).keep_alive(KeepAlive::default())
}

// ── Helpers ───────────────────────────────────────────────────────────────────

/// The GP proxy's live pool-health endpoint (gemini_proxy `GET /health`).
const GP_HEALTH_URL: &str = "http://127.0.0.1:3761/health";

/// Per-request budget for the GP health probe. Localhost answers in
/// milliseconds; a wedged/absent proxy must not delay chat noticeably.
const GP_HEALTH_TIMEOUT: std::time::Duration = std::time::Duration::from_millis(500);

/// GP-first chat preference via the shared selection module (E1-S6).
///
/// Probes the GP proxy's LIVE `/health` endpoint (:3761) — the only source
/// that reflects in-memory 429-cooldown state — and asks
/// `selection::preferred_chat_provider` whether the pool is healthy enough to
/// prefer. Returns `None` on any probe failure (proxy down, timeout, garbage
/// body) or when the pool has no routable slot — the caller then falls back
/// to the unchanged `pick_for_chat` chain.
///
/// WHY not providers.json: a routing table rebuilt from disk always has
/// `enabled=true, re_enable_at=None` slots, so it can report "healthy" while
/// every live key sits in 429 cooldown. Health must come from the live table.
async fn chat_gp_preference() -> Option<&'static str> {
    let gp = fetch_gp_health(GP_HEALTH_URL).await?;
    cascade_core::selection::preferred_chat_provider(&gp)
}

/// `true` when the GP-first chat preference may be consulted for this
/// request (pure): only when the user named no explicit provider AND the
/// namespace does not require trusted-only routing.
fn gp_preference_allowed(explicit_provider: Option<&str>, namespace: Option<&str>) -> bool {
    explicit_provider.is_none() && !namespace_requires_trusted(namespace)
}

/// Async probe of the GP proxy health endpoint. `None` on any failure —
/// health is never guessed upward.
async fn fetch_gp_health(url: &str) -> Option<cascade_core::selection::GpHealthSnapshot> {
    let client = reqwest::Client::builder()
        .timeout(GP_HEALTH_TIMEOUT)
        .build()
        .ok()?;
    let resp = client.get(url).send().await.ok()?;
    if !resp.status().is_success() {
        return None;
    }
    let body = resp.text().await.ok()?;
    Some(cascade_core::routing::gfp_http::parse_gp_health(&body))
}

// ── Provider fallback helpers ────────────────────────────────────────────────

type ProviderArc = Arc<dyn cascade_providers::ProviderAdapter + Send + Sync>;

struct ProviderCandidate {
    provider_id: String,
    adapter: ProviderArc,
}

fn provider_fallback_candidates(
    registry: &cascade_providers::ProviderRegistry,
    requested_first: Option<&str>,
    requires_trusted: bool,
    quota: &QuotaSaturation,
) -> Vec<ProviderCandidate> {
    let mut out = Vec::new();
    let mut seen = HashSet::new();
    let mut registered: Vec<String> = registry.list().into_iter().map(|p| p.id).collect();
    registered.sort();

    if let Some(id) = requested_first {
        let quota_keys = quota_keys_for_provider_id(id);
        push_provider_candidate(
            registry,
            id,
            quota_keys,
            requires_trusted,
            true,
            quota,
            &mut seen,
            &mut out,
        );
    }

    push_group(
        registry,
        &registered,
        &["gp-pool"],
        &["gfp", "gp-pool"],
        |id| id == "gp-pool",
        requires_trusted,
        quota,
        &mut seen,
        &mut out,
    );
    push_group(
        registry,
        &registered,
        &["gemini"],
        &["gemini", "google-agy"],
        |id| {
            let id = id.to_ascii_lowercase();
            id == "gemini" || id.starts_with("gemini-") || id.starts_with("google-agy")
        },
        requires_trusted,
        quota,
        &mut seen,
        &mut out,
    );
    push_group(
        registry,
        &registered,
        &["openai", "codex", "openai-codex"],
        &["codex", "openai-codex", "openai"],
        |id| {
            let id = id.to_ascii_lowercase();
            id == "openai"
                || id == "codex"
                || id == "openai-codex"
                || id.starts_with("openai-")
                || id.starts_with("codex-")
        },
        requires_trusted,
        quota,
        &mut seen,
        &mut out,
    );
    push_group(
        registry,
        &registered,
        &["claude2", "acc2", "claude-acc2"],
        &["claude2", "acc2"],
        |id| {
            let id = id.to_ascii_lowercase();
            id == "claude2" || id == "acc2" || id.contains("claude2") || id.contains("acc2")
        },
        requires_trusted,
        quota,
        &mut seen,
        &mut out,
    );
    push_group(
        registry,
        &registered,
        &["claude", "anthropic", "acc1", "claude-acc1"],
        &["claude", "anthropic", "acc1"],
        |id| {
            let id = id.to_ascii_lowercase();
            id == "claude"
                || id == "anthropic"
                || id == "acc1"
                || id.starts_with("claude-")
                || id.starts_with("anthropic-")
                || id.starts_with("cc-")
        },
        requires_trusted,
        quota,
        &mut seen,
        &mut out,
    );
    push_group(
        registry,
        &registered,
        &["opencode", "oc-go"],
        &["opencode", "oc-go"],
        |id| {
            let id = id.to_ascii_lowercase();
            id == "opencode"
                || id == "oc-go"
                || id.starts_with("opencode-")
                || id.starts_with("oc-")
        },
        requires_trusted,
        quota,
        &mut seen,
        &mut out,
    );
    push_group(
        registry,
        &registered,
        &["local", "ollama"],
        &[],
        |id| {
            let id = id.to_ascii_lowercase();
            id.starts_with("local") || id == "ollama" || id.starts_with("ollama-")
        },
        requires_trusted,
        quota,
        &mut seen,
        &mut out,
    );

    out
}

fn quota_keys_for_provider_id(id: &str) -> &'static [&'static str] {
    let id = id.to_ascii_lowercase();
    if id == "gp-pool" {
        &["gfp", "gp-pool"]
    } else if id == "gemini" || id.starts_with("gemini-") {
        &["gemini", "google-agy"]
    } else if id == "openai"
        || id == "codex"
        || id.starts_with("openai-")
        || id.starts_with("codex-")
    {
        &["codex", "openai-codex", "openai"]
    } else if id == "opencode"
        || id == "oc-go"
        || id.starts_with("opencode-")
        || id.starts_with("oc-")
    {
        &["opencode", "oc-go"]
    } else {
        &[]
    }
}

#[allow(clippy::too_many_arguments)]
fn push_group<F>(
    registry: &cascade_providers::ProviderRegistry,
    registered: &[String],
    exact_ids: &[&str],
    quota_keys: &[&str],
    matches_group: F,
    requires_trusted: bool,
    quota: &QuotaSaturation,
    seen: &mut HashSet<String>,
    out: &mut Vec<ProviderCandidate>,
) where
    F: Fn(&str) -> bool,
{
    for id in exact_ids {
        push_provider_candidate(
            registry,
            id,
            quota_keys,
            requires_trusted,
            false,
            quota,
            seen,
            out,
        );
    }
    for id in registered {
        if matches_group(id) {
            push_provider_candidate(
                registry,
                id,
                quota_keys,
                requires_trusted,
                false,
                quota,
                seen,
                out,
            );
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn push_provider_candidate(
    registry: &cascade_providers::ProviderRegistry,
    id: &str,
    quota_keys: &[&str],
    requires_trusted: bool,
    bypass_trust_filter: bool,
    quota: &QuotaSaturation,
    seen: &mut HashSet<String>,
    out: &mut Vec<ProviderCandidate>,
) {
    if seen.contains(id) || quota.is_saturated(id, quota_keys) {
        return;
    }
    if requires_trusted
        && !bypass_trust_filter
        && !cascade_core::sensitivity::registry_provider_is_trusted_for_sensitive(id)
    {
        return;
    }
    if let Some(adapter) = registry.get(id) {
        seen.insert(id.to_string());
        out.push(ProviderCandidate {
            provider_id: id.to_string(),
            adapter,
        });
    }
}

#[derive(Debug, Default)]
struct QuotaSaturation {
    saturated_ids: HashSet<String>,
}

impl QuotaSaturation {
    fn is_saturated(&self, id: &str, extra_keys: &[&str]) -> bool {
        self.saturated_ids.contains(&id.to_ascii_lowercase())
            || extra_keys
                .iter()
                .any(|key| self.saturated_ids.contains(&key.to_ascii_lowercase()))
    }

    fn insert(&mut self, id: &str) {
        let id = id.trim().to_ascii_lowercase();
        if !id.is_empty() {
            self.saturated_ids.insert(id);
        }
    }
}

async fn load_quota_saturation() -> QuotaSaturation {
    let Some(home) = std::env::var_os("HOME").map(PathBuf::from) else {
        return QuotaSaturation::default();
    };
    let path = home.join(".cascade").join("accounts").join("quota.json");
    let Ok(raw) = tokio::fs::read_to_string(path).await else {
        return QuotaSaturation::default();
    };
    let Ok(value) = serde_json::from_str::<Value>(&raw) else {
        return QuotaSaturation::default();
    };
    quota_saturation_from_value(&value)
}

fn quota_saturation_from_value(value: &Value) -> QuotaSaturation {
    let mut out = QuotaSaturation::default();
    if let Some(accounts) = value.get("accounts").and_then(Value::as_array) {
        for account in accounts {
            if !quota_entry_saturated(account) {
                continue;
            }
            let account_id = account
                .get("account")
                .or_else(|| account.get("account_id"))
                .and_then(Value::as_str);
            let provider = account.get("provider").and_then(Value::as_str);
            if let Some(id) = account_id {
                out.insert(id);
            } else if let Some(id) = provider {
                out.insert(id);
            }
        }
    }
    out
}

fn quota_entry_saturated(value: &Value) -> bool {
    if value
        .get("status")
        .and_then(Value::as_str)
        .map(|s| {
            matches!(
                s.to_ascii_lowercase().as_str(),
                "saturated" | "exhausted" | "quota_exhausted" | "rate_limited" | "rate-limited"
            )
        })
        .unwrap_or(false)
    {
        return true;
    }

    let mut pcts = Vec::new();
    collect_quota_pcts(value, &mut pcts);
    cascade_core::selection::pcts_saturated(pcts) || has_exhausted_limit(value)
}

fn collect_quota_pcts(value: &Value, out: &mut Vec<f64>) {
    match value {
        Value::Object(map) => {
            for (key, child) in map {
                let key = key.to_ascii_lowercase();
                if matches!(
                    key.as_str(),
                    "utilization" | "pct_used" | "percent_used" | "percentage_used" | "used_pct"
                ) {
                    if let Some(n) = child.as_f64() {
                        out.push(n);
                    }
                }
                collect_quota_pcts(child, out);
            }
        }
        Value::Array(items) => {
            for child in items {
                collect_quota_pcts(child, out);
            }
        }
        _ => {}
    }
}

fn has_exhausted_limit(value: &Value) -> bool {
    match value {
        Value::Object(map) => {
            let used = map.get("used").and_then(Value::as_u64);
            let limit = map.get("limit").and_then(Value::as_u64);
            if let (Some(used), Some(limit)) = (used, limit) {
                if limit > 0 && used >= limit {
                    return true;
                }
            }
            map.values().any(has_exhausted_limit)
        }
        Value::Array(items) => items.iter().any(has_exhausted_limit),
        _ => false,
    }
}

// ── Personal context helpers ─────────────────────────────────────────────────

const PERSONAL_CONTEXT_MAX_CHARS: usize = 6_000;
const PERSONAL_INDEX_MAX_TOPICS: usize = 40;
const PERSONAL_SNIPPET_MAX_FILES: usize = 8;
const PERSONAL_SNIPPET_MAX_CHARS: usize = 600;
const PERSONAL_TOPIC_MAX_FILES: usize = 10;
const PERSONAL_TOPIC_FILE_MAX_CHARS: usize = 1_000;
const PERSONAL_READ_MAX_BYTES: u64 = 16_384;

async fn load_personal_context(namespace: Option<&str>) -> Option<String> {
    if !is_personal_namespace(namespace) {
        return None;
    }
    let base = personal_claude_dir()?;
    let mut out = String::new();

    append_personal_thread_index(&mut out, &base.join("threads")).await;
    append_personal_dir_snippets(&mut out, "Memory", &base.join("memory")).await;
    append_personal_dir_snippets(&mut out, "Summaries", &base.join("summaries")).await;

    if let Some(topic_id) = personal_topic_id(namespace) {
        if let Some(safe_id) = safe_topic_id(topic_id) {
            append_personal_topic(&mut out, &base.join("threads").join(&safe_id), &safe_id).await;
        }
    }

    let trimmed = out.trim();
    if trimmed.is_empty() {
        None
    } else {
        Some(format!(
            "Personal context from ~/Downloads/.claude (bounded local excerpts):\n{}",
            truncate_chars(trimmed, PERSONAL_CONTEXT_MAX_CHARS)
        ))
    }
}

fn personal_claude_dir() -> Option<PathBuf> {
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .map(|home| home.join("Downloads").join(".claude"))
}

fn is_personal_namespace(namespace: Option<&str>) -> bool {
    namespace
        .map(|ns| ns.trim().to_ascii_lowercase().starts_with("personal"))
        .unwrap_or(false)
}

fn personal_topic_id(namespace: Option<&str>) -> Option<&str> {
    let ns = namespace?.trim();
    if ns
        .get(.."personal:topic:".len())
        .is_some_and(|prefix| prefix.eq_ignore_ascii_case("personal:topic:"))
    {
        Some(&ns["personal:topic:".len()..])
    } else {
        None
    }
}

fn safe_topic_id(id: &str) -> Option<String> {
    let id = id.trim();
    if id.is_empty()
        || id == "."
        || id == ".."
        || id.contains('/')
        || id.contains('\\')
        || id.contains('\0')
    {
        None
    } else {
        Some(id.to_string())
    }
}

async fn append_personal_thread_index(out: &mut String, threads_dir: &Path) {
    let Some(children) = sorted_personal_children(threads_dir).await else {
        return;
    };
    let mut lines = Vec::new();
    for path in children {
        if lines.len() >= PERSONAL_INDEX_MAX_TOPICS {
            break;
        }
        let Ok(meta) = tokio::fs::metadata(&path).await else {
            continue;
        };
        if !meta.is_dir() {
            continue;
        }
        let Some(id) = path.file_name().and_then(|n| n.to_str()) else {
            continue;
        };
        let title = personal_topic_title(&path, id).await;
        lines.push(format!("- {title} (id: {id})"));
    }
    if !lines.is_empty() {
        out.push_str("Topics:\n");
        out.push_str(&lines.join("\n"));
        out.push_str("\n\n");
    }
}

async fn append_personal_dir_snippets(out: &mut String, label: &str, dir: &Path) {
    let Some(children) = sorted_personal_children(dir).await else {
        return;
    };
    let mut snippets = Vec::new();
    for path in children {
        if snippets.len() >= PERSONAL_SNIPPET_MAX_FILES {
            break;
        }
        let Ok(meta) = tokio::fs::metadata(&path).await else {
            continue;
        };
        if !meta.is_file() {
            continue;
        }
        if let Some(snippet) = read_personal_text_prefix(&path, PERSONAL_SNIPPET_MAX_CHARS).await {
            let name = path
                .file_name()
                .and_then(|n| n.to_str())
                .unwrap_or("unknown");
            snippets.push(format!("- {name}: {}", one_line(&snippet)));
        }
    }
    if !snippets.is_empty() {
        out.push_str(label);
        out.push_str(":\n");
        out.push_str(&snippets.join("\n"));
        out.push_str("\n\n");
    }
}

async fn append_personal_topic(out: &mut String, topic_dir: &Path, topic_id: &str) {
    let Some(children) = sorted_personal_children(topic_dir).await else {
        return;
    };
    let mut snippets = Vec::new();
    for path in children {
        if snippets.len() >= PERSONAL_TOPIC_MAX_FILES {
            break;
        }
        let Ok(meta) = tokio::fs::metadata(&path).await else {
            continue;
        };
        if !meta.is_file() {
            continue;
        }
        if let Some(snippet) = read_personal_text_prefix(&path, PERSONAL_TOPIC_FILE_MAX_CHARS).await
        {
            let name = path
                .file_name()
                .and_then(|n| n.to_str())
                .unwrap_or("unknown");
            snippets.push(format!("- {name}: {}", one_line(&snippet)));
        }
    }
    if !snippets.is_empty() {
        out.push_str("Selected topic ");
        out.push_str(topic_id);
        out.push_str(":\n");
        out.push_str(&snippets.join("\n"));
        out.push_str("\n\n");
    }
}

async fn personal_topic_title(topic_dir: &Path, id: &str) -> String {
    let Some(children) = sorted_personal_children(topic_dir).await else {
        return prettify_personal_id(id);
    };
    for path in children {
        let Ok(meta) = tokio::fs::metadata(&path).await else {
            continue;
        };
        if !meta.is_file() {
            continue;
        }
        if let Some(snippet) = read_personal_text_prefix(&path, PERSONAL_SNIPPET_MAX_CHARS).await {
            if let Some(title) = title_from_personal_text(&snippet) {
                return title;
            }
        }
    }
    prettify_personal_id(id)
}

async fn sorted_personal_children(dir: &Path) -> Option<Vec<PathBuf>> {
    let mut rd = tokio::fs::read_dir(dir).await.ok()?;
    let mut out = Vec::new();
    while let Ok(Some(entry)) = rd.next_entry().await {
        out.push(entry.path());
    }
    out.sort_by_key(|p| {
        p.file_name()
            .map(|n| n.to_string_lossy().to_ascii_lowercase())
            .unwrap_or_default()
    });
    Some(out)
}

async fn read_personal_text_prefix(path: &Path, max_chars: usize) -> Option<String> {
    let file = tokio::fs::File::open(path).await.ok()?;
    let mut limited = file.take(PERSONAL_READ_MAX_BYTES);
    let mut bytes = Vec::new();
    limited.read_to_end(&mut bytes).await.ok()?;
    let text: String = String::from_utf8_lossy(&bytes)
        .chars()
        .take(max_chars)
        .collect();
    let text = text.trim();
    if text.is_empty() {
        None
    } else {
        Some(text.to_string())
    }
}

fn title_from_personal_text(text: &str) -> Option<String> {
    for line in text.lines().take(12) {
        let line = line.trim();
        if line.is_empty() || line == "---" {
            continue;
        }
        let lower = line.to_ascii_lowercase();
        let candidate = if lower.starts_with("title:") {
            line.split_once(':')
                .map(|(_, rest)| rest.trim())
                .unwrap_or(line)
        } else if line.starts_with('#') {
            line.trim_start_matches('#').trim()
        } else {
            line
        };
        let title = truncate_chars(candidate.trim_matches('"').trim_matches('\''), 96);
        if !title.is_empty() {
            return Some(title);
        }
    }
    None
}

fn prettify_personal_id(id: &str) -> String {
    let words: Vec<String> = id
        .split(['-', '_'])
        .filter(|w| !w.is_empty())
        .map(|word| {
            let mut chars = word.chars();
            match chars.next() {
                Some(first) => {
                    let mut out = first.to_uppercase().collect::<String>();
                    out.push_str(chars.as_str());
                    out
                }
                None => String::new(),
            }
        })
        .collect();
    if words.is_empty() {
        id.to_string()
    } else {
        words.join(" ")
    }
}

fn one_line(text: &str) -> String {
    text.split_whitespace().collect::<Vec<_>>().join(" ")
}

fn truncate_chars(text: &str, max_chars: usize) -> String {
    text.chars().take(max_chars).collect()
}

fn append_prompt_context(slot: &mut Option<String>, addition: String) {
    match slot {
        Some(existing) if !existing.trim().is_empty() => {
            existing.push_str("\n\n");
            existing.push_str(&addition);
        }
        _ => *slot = Some(addition),
    }
}

// ── E2-S2 pre-middleware helpers ──────────────────────────────────────────────

/// Max characters of GCI CASCADE.md fed into the injected context summary.
///
/// Keeps the injected system prefix small AND byte-stable: given unchanged
/// file content the truncation is deterministic, so the provider's prompt
/// cache prefix keeps hitting across requests.
const INJECT_SUMMARY_MAX_CHARS: usize = 2_000;

/// Run context compression off the async runtime (the GP transport is a
/// blocking curl subprocess). On ANY failure — including a panicked or
/// cancelled blocking task — the ORIGINAL messages are returned unchanged.
async fn compress_chat_messages(messages: Vec<ChatMessage>) -> Vec<ChatMessage> {
    use cascade_core::middleware::{compress_context_via_gp, ChatTurn};
    let turns: Vec<ChatTurn> = messages
        .iter()
        .map(|m| ChatTurn::new(m.role.clone(), m.content.clone()))
        .collect();
    match tokio::task::spawn_blocking(move || compress_context_via_gp(&turns)).await {
        Ok(compressed) => compressed
            .into_iter()
            .map(|t| ChatMessage {
                role: t.role,
                content: t.content,
            })
            .collect(),
        Err(_) => messages,
    }
}

/// Gather the project context strings for system-prompt injection.
///
/// Currently sources `claude_md_summary` from the global GCI file
/// (`~/.cascade/CASCADE.md`, truncated); `active_task` and `stack_notes`
/// stay `None` until the task-store / stack-detection surfaces are wired in
/// (E2-S3+). Missing file or missing home dir → empty context → the pure
/// merge returns `None` and the request is dispatched untouched.
async fn load_project_context() -> cascade_core::middleware::ProjectContext {
    let mut ctx = cascade_core::middleware::ProjectContext::default();
    if let Some(home) = dirs::home_dir() {
        let path = home.join(".cascade").join("CASCADE.md");
        if let Ok(text) = tokio::fs::read_to_string(&path).await {
            let truncated: String = text.chars().take(INJECT_SUMMARY_MAX_CHARS).collect();
            if !truncated.trim().is_empty() {
                ctx.claude_md_summary = Some(truncated);
            }
        }
    }
    ctx
}

/// Pure model-downgrade map for the chat path (E2-S2 classification).
///
/// Only ever DOWNGRADES: a Trivial-classified request on the Gemini family
/// steps down to flash-lite. Every other (provider, complexity) pair returns
/// `None` — the chat defaults are already the cheap tier for the remaining
/// providers, and classification must never upgrade the model.
fn downgraded_model_for(
    provider_id: &str,
    c: cascade_core::middleware::RequestComplexity,
) -> Option<String> {
    use cascade_core::middleware::RequestComplexity::Trivial;
    match (provider_id, c) {
        ("gemini" | "gp-pool", Trivial) => Some("gemini-flash-lite-latest".into()),
        _ => None,
    }
}

/// `true` when classification could possibly change the outcome for this
/// provider (pure). Must stay in lock-step with [`downgraded_model_for`]:
/// only the Gemini family has a downgrade step, so for every other provider
/// the bounded GP classify call would produce a result that is thrown away.
fn provider_can_downgrade(provider_id: &str) -> bool {
    matches!(provider_id, "gemini" | "gp-pool")
}

/// `true` when a request namespace marks personal or private chat (pure).
///
/// Thin alias for the canonical classifier in
/// [`cascade_core::sensitivity::is_protected_namespace`] — shared with the
/// :3762 anthropic-compat proxy and mirrored by `useChat.ts` in the app.
fn is_protected_namespace(namespace: Option<&str>) -> bool {
    cascade_core::sensitivity::is_protected_namespace(namespace)
}

fn namespace_requires_trusted(namespace: Option<&str>) -> bool {
    namespace
        .map(|ns| ns.contains("vault") || ns.ends_with(":private") || ns == "private")
        .unwrap_or(false)
}

/// Neutralise content-capturing / GP-touching middleware for protected
/// namespaces (pure).
///
/// For `personal` / `personal:private` chats:
/// - `context_sync` OFF — digests feed the GLOBAL RAG index, which is exactly
///   the "Cascade memory" the private-mode UI promises to skip, and the
///   personal namespace's `memory.personalChatSync` consent gate does not
///   cover that side channel.
/// - `compress_context` / `classify_requests` OFF — both embed conversation
///   content verbatim in GP-pool (Google) calls.
/// - `inject_context` stays as configured — a pure local template merge;
///   nothing is captured and nothing extra leaves the machine.
fn effective_middleware_flags(
    flags: crate::config::MiddlewareConfig,
    namespace: Option<&str>,
) -> crate::config::MiddlewareConfig {
    if is_protected_namespace(namespace) {
        crate::config::MiddlewareConfig {
            inject_context: flags.inject_context,
            ..Default::default()
        }
    } else {
        flags
    }
}

/// Return a sensible default model string for the given provider id.
///
/// This is a best-effort hint used when no model is specified in an incoming
/// chat request; the provider adapter may override it.
fn default_model_for(provider_id: &str) -> String {
    match provider_id {
        // "gp-pool" is the reserved pool-backed adapter id (GP_CHAT_PROVIDER_ID);
        // the :3761 pool serves free Flash only.
        "gemini" | "gp-pool" => MODEL_GEMINI_FLASH_LATEST.into(),
        "anthropic" => MODEL_CLAUDE_HAIKU.into(),
        "openai" => MODEL_GPT.into(),
        _ if provider_id.starts_with("local") => "default".into(),
        _ => "default".into(),
    }
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;
    use axum::{
        body::{to_bytes, Body},
        http::{Request, StatusCode},
    };
    use cascade_providers::{
        AuthMethod, CompletionResponse, ModelInfo, NoopProvider, ProviderAdapter,
        ProviderCapabilities, ProviderError, ProviderInfo, ProviderRegistry, StreamChunk,
        TokenUsage,
    };
    use futures_util::stream::Stream;
    use std::pin::Pin;
    use tower::ServiceExt;

    // ── Mock adapters ─────────────────────────────────────────────────────────

    /// A mock adapter that streams a fixed set of chunks then finishes.
    struct StreamingMockProvider {
        id: &'static str,
        chunks: Vec<&'static str>,
        /// If true, health_check returns Ok; otherwise Err.
        healthy: bool,
    }

    impl StreamingMockProvider {
        fn new(id: &'static str, chunks: Vec<&'static str>, healthy: bool) -> Self {
            Self {
                id,
                chunks,
                healthy,
            }
        }
    }

    #[async_trait]
    impl ProviderAdapter for StreamingMockProvider {
        async fn complete(
            &self,
            _req: CompletionRequest,
        ) -> Result<CompletionResponse, ProviderError> {
            Ok(CompletionResponse {
                content: self.chunks.join(""),
                model: self.id.into(),
                usage: TokenUsage::default(),
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
            let chunks: Vec<Result<StreamChunk, ProviderError>> = self
                .chunks
                .iter()
                .enumerate()
                .map(|(i, &text)| {
                    Ok(StreamChunk {
                        delta: text.to_string(),
                        finish_reason: if i == self.chunks.len() - 1 {
                            Some("stop".into())
                        } else {
                            None
                        },
                    })
                })
                .collect();
            // SAFETY: `futures_util::stream::iter` returns a `Send` stream.
            let s = futures_util::stream::iter(chunks);
            Ok(Box::pin(s))
        }

        async fn available_models(&self) -> Result<Vec<ModelInfo>, ProviderError> {
            Ok(vec![ModelInfo {
                id: self.id.into(),
                name: self.id.into(),
                context_window: 4096,
                supports_streaming: true,
            }])
        }

        async fn health_check(&self) -> Result<(), ProviderError> {
            if self.healthy {
                Ok(())
            } else {
                Err(ProviderError::NetworkError("mock unhealthy".into()))
            }
        }

        fn provider_info(&self) -> ProviderInfo {
            ProviderInfo {
                id: self.id.into(),
                name: format!("Mock({})", self.id),
                auth_method: AuthMethod::None,
                base_url: String::new(),
                capabilities: ProviderCapabilities {
                    supports_streaming: true,
                    supports_vision: false,
                    max_context_tokens: 4096,
                    supports_function_calling: false,
                },
            }
        }
    }

    // ── State builders ────────────────────────────────────────────────────────

    fn state_no_registry() -> DashboardState {
        DashboardState {
            token: Arc::new("test-token".into()),
            provider_registry: None,
            routing_ring: None,
            middleware: Default::default(),
        }
    }

    fn state_with_registry(registry: Arc<ProviderRegistry>) -> DashboardState {
        DashboardState {
            token: Arc::new("test-token".into()),
            provider_registry: Some(registry),
            routing_ring: None,
            middleware: Default::default(),
        }
    }

    fn make_app(state: DashboardState) -> axum::Router {
        router().with_state(state)
    }

    fn chat_body(provider: Option<&str>) -> String {
        let mut obj = serde_json::json!({
            "messages": [{ "role": "user", "content": "hello" }]
        });
        if let Some(p) = provider {
            obj["provider"] = serde_json::Value::String(p.into());
        }
        obj.to_string()
    }

    async fn collect_sse(body: axum::body::Body) -> String {
        let bytes = to_bytes(body, 65536).await.unwrap();
        String::from_utf8_lossy(&bytes).to_string()
    }

    // ── Test 1: no registry → typed error event ───────────────────────────────

    #[tokio::test]
    async fn no_registry_yields_error_event() {
        let app = make_app(state_no_registry());
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;
        assert!(body.contains("event: error"), "body: {body}");
        assert!(body.contains("no provider registry"), "body: {body}");
        assert!(body.contains("[DONE]"), "body: {body}");
    }

    // ── Test 2: no provider available → typed error event ─────────────────────

    #[tokio::test]
    async fn no_provider_available_yields_error_event() {
        let registry = Arc::new(ProviderRegistry::new());
        // Register a noop that never matches (not in routing table)
        // by using an id not in the default Chat priority list.
        registry
            .register("noop-x".into(), Arc::new(NoopProvider))
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;
        assert!(body.contains("event: error"), "body: {body}");
        assert!(body.contains("no provider available"), "body: {body}");
    }

    // ── Protected-namespace provider gating (privacy firewall) ────────────────

    fn chat_body_ns(namespace: &str, provider: Option<&str>) -> String {
        let mut obj = serde_json::json!({
            "messages": [{ "role": "user", "content": "hello" }],
            "namespace": namespace,
        });
        if let Some(p) = provider {
            obj["provider"] = serde_json::Value::String(p.into());
        }
        obj.to_string()
    }

    async fn post_chat(app: axum::Router, body: String) -> String {
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(body))
            .unwrap();
        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        collect_sse(resp.into_body()).await
    }

    /// DIFFERENTIAL: with gp-pool (healthy cloud) + a local adapter
    /// registered, an UNPROTECTED chat is served by gp-pool via the health
    /// scan, while a PROTECTED chat must skip it and land on local.
    #[tokio::test]
    async fn protected_namespace_skips_gp_pool_lands_on_local() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "gp-pool".into(),
                Arc::new(StreamingMockProvider::new("gp-pool", vec!["leak"], true)),
            )
            .unwrap();
        registry
            .register(
                "local:ollama".into(),
                Arc::new(StreamingMockProvider::new(
                    "local:ollama",
                    vec!["safe"],
                    true,
                )),
            )
            .unwrap();

        // Control: unprotected namespace → gp-pool (healthy cloud scan).
        let body = post_chat(
            make_app(state_with_registry(Arc::clone(&registry))),
            chat_body_ns("projects:cascade", None),
        )
        .await;
        assert!(body.contains("\"gp-pool\""), "control body: {body}");

        // Protected namespace → gp-pool is filtered out, local serves.
        let body = post_chat(
            make_app(state_with_registry(registry)),
            chat_body_ns("personal:private", None),
        )
        .await;
        assert!(
            !body.contains("\"gp-pool\""),
            "private chat reached gp-pool: {body}"
        );
        assert!(body.contains("\"local:ollama\""), "body: {body}");
        assert!(body.contains("safe"), "body: {body}");
    }

    /// Fail-closed: only untrusted providers registered → typed error, and
    /// the conversation is never dispatched.
    #[tokio::test]
    async fn protected_namespace_fails_closed_when_only_untrusted() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "gp-pool".into(),
                Arc::new(StreamingMockProvider::new("gp-pool", vec!["leak"], true)),
            )
            .unwrap();

        let body = post_chat(
            make_app(state_with_registry(registry)),
            chat_body_ns("personal:private", None),
        )
        .await;
        assert!(body.contains("event: error"), "body: {body}");
        assert!(
            body.contains("private chat requires a trusted provider"),
            "body: {body}"
        );
        assert!(!body.contains("event: served_by"), "body: {body}");
        assert!(!body.contains("leak"), "body: {body}");
        assert!(body.contains("[DONE]"), "body: {body}");
    }

    /// An EXPLICIT provider named in the request wins even for a protected
    /// namespace — deliberate user choice outranks the policy by design.
    #[tokio::test]
    async fn protected_namespace_explicit_provider_still_wins() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "gp-pool".into(),
                Arc::new(StreamingMockProvider::new("gp-pool", vec!["pinned"], true)),
            )
            .unwrap();

        let body = post_chat(
            make_app(state_with_registry(registry)),
            chat_body_ns("personal:private", Some("gp-pool")),
        )
        .await;
        assert!(body.contains("event: served_by"), "body: {body}");
        assert!(body.contains("\"gp-pool\""), "body: {body}");
        assert!(body.contains("pinned"), "body: {body}");
    }

    // ── Test 3: routing-table default → streams tokens ────────────────────────

    #[tokio::test]
    async fn routing_table_default_streams_tokens() {
        let registry = Arc::new(ProviderRegistry::new());
        // "openai" is the first in the Chat priority list.
        registry
            .register(
                "openai".into(),
                Arc::new(StreamingMockProvider::new(
                    "openai",
                    vec!["hello", " world"],
                    true,
                )),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;

        // served_by event
        assert!(body.contains("event: served_by"), "body: {body}");
        assert!(body.contains("\"openai\""), "body: {body}");
        // token events
        assert!(body.contains("event: token"), "body: {body}");
        assert!(body.contains("hello"), "body: {body}");
        assert!(body.contains("[DONE]"), "body: {body}");
    }

    // ── Test 4: user-selected provider wins over default ──────────────────────

    #[tokio::test]
    async fn user_selected_provider_wins_over_routing_default() {
        let registry = Arc::new(ProviderRegistry::new());
        // "openai" is routing default; register "anthropic" as explicit choice.
        registry
            .register(
                "openai".into(),
                Arc::new(StreamingMockProvider::new(
                    "openai",
                    vec!["from-openai"],
                    true,
                )),
            )
            .unwrap();
        registry
            .register(
                "anthropic".into(),
                Arc::new(StreamingMockProvider::new(
                    "anthropic",
                    vec!["from-anthropic"],
                    true,
                )),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        // Explicitly request "anthropic"
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(Some("anthropic"))))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        let body = collect_sse(resp.into_body()).await;

        assert!(
            body.contains("\"anthropic\""),
            "served_by must say anthropic: {body}"
        );
        assert!(
            body.contains("from-anthropic"),
            "token must come from anthropic: {body}"
        );
        assert!(!body.contains("from-openai"), "must not use openai: {body}");
    }

    // ── Test 5: local fallback when no cloud provider is healthy ──────────────

    #[tokio::test]
    async fn local_fallback_when_no_cloud_healthy() {
        let registry = Arc::new(ProviderRegistry::new());
        // Cloud provider is unhealthy and not in the routing table.
        registry
            .register(
                "cloud-broken".into(),
                Arc::new(StreamingMockProvider::new("cloud-broken", vec![], false)),
            )
            .unwrap();
        // Local provider — id starts with "local".
        registry
            .register(
                "local:ollama".into(),
                Arc::new(StreamingMockProvider::new(
                    "local:ollama",
                    vec!["local-response"],
                    false, // local models don't need health_check
                )),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let body = collect_sse(resp.into_body()).await;

        // Local provider served the request
        assert!(body.contains("\"local:ollama\""), "body: {body}");
        assert!(body.contains("local-response"), "body: {body}");
    }

    // ── Test 6: served_by provider id is reported ─────────────────────────────

    #[tokio::test]
    async fn served_by_is_reported_in_sse() {
        let registry = Arc::new(ProviderRegistry::new());
        registry
            .register(
                "gemini".into(),
                Arc::new(StreamingMockProvider::new("gemini", vec!["tok"], true)),
            )
            .unwrap();

        let app = make_app(state_with_registry(registry));
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(Some("gemini"))))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        let body = collect_sse(resp.into_body()).await;

        // The served_by event appears before token events.
        let served_pos = body.find("event: served_by").unwrap_or(usize::MAX);
        let token_pos = body.find("event: token").unwrap_or(0);
        assert!(
            served_pos < token_pos,
            "served_by must precede first token: {body}"
        );
        assert!(body.contains("\"gemini\""), "body: {body}");
    }

    // ── Test 7: SSE response always has text/event-stream content type ─────────

    #[tokio::test]
    async fn response_is_sse_content_type() {
        let app = make_app(state_no_registry());
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from(chat_body(None)))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert_eq!(resp.status(), StatusCode::OK);
        let ct = resp
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .unwrap_or("");
        assert!(ct.starts_with("text/event-stream"), "got: {ct}");
    }

    // ── Test 8: invalid JSON → 400 ────────────────────────────────────────────

    #[tokio::test]
    async fn invalid_json_returns_4xx() {
        let app = make_app(state_no_registry());
        let req = Request::builder()
            .method("POST")
            .uri("/")
            .header("Content-Type", "application/json")
            .body(Body::from("not json"))
            .unwrap();

        let resp = app.oneshot(req).await.unwrap();
        assert!(
            resp.status() == StatusCode::BAD_REQUEST
                || resp.status() == StatusCode::UNPROCESSABLE_ENTITY,
            "expected 400 or 422, got {}",
            resp.status()
        );
    }

    // ── Test 9: pick_for_chat routing order verified directly ─────────────────

    #[tokio::test]
    async fn pick_for_chat_prefers_explicit_over_routing_default() {
        let registry = ProviderRegistry::new();
        registry
            .register(
                "openai".into(), // routing default for Chat
                Arc::new(StreamingMockProvider::new("openai", vec![], true)),
            )
            .unwrap();
        registry
            .register(
                "mymodel".into(),
                Arc::new(StreamingMockProvider::new("mymodel", vec![], true)),
            )
            .unwrap();

        let (_, id) = registry
            .pick_for_chat(Some("mymodel"))
            .await
            .expect("should pick mymodel");
        assert_eq!(id, "mymodel");
    }

    #[tokio::test]
    async fn pick_for_chat_routing_default_when_no_preferred() {
        let registry = ProviderRegistry::new();
        // Only "anthropic" registered — priority 2 in Chat routing.
        registry
            .register(
                "anthropic".into(),
                Arc::new(StreamingMockProvider::new("anthropic", vec![], true)),
            )
            .unwrap();

        let (_, id) = registry
            .pick_for_chat(None)
            .await
            .expect("should pick anthropic");
        assert_eq!(id, "anthropic");
    }

    #[tokio::test]
    async fn pick_for_chat_local_fallback_when_no_cloud() {
        let registry = ProviderRegistry::new();
        // No cloud providers in routing list; one local.
        registry
            .register(
                "local:test".into(),
                Arc::new(StreamingMockProvider::new("local:test", vec![], false)),
            )
            .unwrap();

        let (_, id) = registry
            .pick_for_chat(None)
            .await
            .expect("should pick local fallback");
        assert_eq!(id, "local:test");
    }

    #[tokio::test]
    async fn pick_for_chat_none_when_nothing_registered() {
        let registry = ProviderRegistry::new();
        let result = registry.pick_for_chat(None).await;
        assert!(result.is_none(), "empty registry must return None");
    }

    // ── GP health probe (E1-S6 review fixes) ──────────────────────────────────

    /// The probe against a live endpoint must read healthy_slots and enable
    /// the preference; against a "pool fully in cooldown" body it must not.
    #[tokio::test]
    async fn fetch_gp_health_reads_live_endpoint() {
        use axum::routing::get;

        let app = axum::Router::new().route(
            "/health",
            get(|| async {
                axum::Json(serde_json::json!({
                    "status": "ok", "healthy_slots": 3, "total_slots": 6
                }))
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        let gp = fetch_gp_health(&format!("http://{addr}/health"))
            .await
            .expect("live endpoint must yield a snapshot");
        assert_eq!(gp.healthy_slots, 3);
        assert_eq!(
            cascade_core::selection::preferred_chat_provider(&gp),
            Some(cascade_core::selection::GP_CHAT_PROVIDER_ID)
        );
    }

    /// Regression: a reachable proxy whose pool is FULLY rate-limited
    /// (healthy_slots = 0) must NOT enable the GP preference — the old
    /// providers.json-derived snapshot could never see this state.
    #[tokio::test]
    async fn fetch_gp_health_cooldown_pool_disables_preference() {
        use axum::routing::get;

        let app = axum::Router::new().route(
            "/health",
            get(|| async {
                axum::Json(serde_json::json!({
                    "status": "ok", "healthy_slots": 0, "total_slots": 28
                }))
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });

        let gp = fetch_gp_health(&format!("http://{addr}/health"))
            .await
            .expect("reachable endpoint yields a snapshot");
        assert!(!gp.is_healthy());
        assert_eq!(cascade_core::selection::preferred_chat_provider(&gp), None);
    }

    /// Dead port (proxy not running) → None → the pick_for_chat chain runs
    /// exactly as before the GP preference existed.
    #[tokio::test]
    async fn fetch_gp_health_dead_port_is_none() {
        let port = {
            let l = std::net::TcpListener::bind("127.0.0.1:0").unwrap();
            l.local_addr().unwrap().port()
        };
        let gp = fetch_gp_health(&format!("http://127.0.0.1:{port}/health")).await;
        assert!(gp.is_none());
    }

    /// The default model for the reserved pool adapter id must be Flash.
    #[test]
    fn default_model_for_gp_pool_is_flash() {
        assert_eq!(default_model_for("gp-pool"), MODEL_GEMINI_FLASH_LATEST);
    }

    // ── E2-S2 pre-middleware ──────────────────────────────────────────────────

    /// The middleware flags on a default DashboardState are all OFF — the
    /// hot-path guard: no compression, no injection, no classification.
    /// (The streaming tests above run with this default state, so their
    /// unchanged token output IS the flags-off passthrough proof.)
    #[test]
    fn middleware_flags_default_off() {
        let state = state_no_registry();
        assert!(!state.middleware.compress_context);
        assert!(!state.middleware.inject_context);
        assert!(!state.middleware.classify_requests);
        // E2-S3 post-middleware: default off → no accumulation, no spawn.
        assert!(!state.middleware.context_sync);
        assert!(!crate::context_sync::should_sync(&state.middleware));
    }

    /// The wire field the app sends for personal/private chats must survive
    /// deserialization — a missing struct field means serde silently drops it
    /// and the privacy gating never engages (the original bug).
    #[test]
    fn chat_request_namespace_is_deserialized() {
        let req: ChatRequest = serde_json::from_str(
            r#"{"messages":[{"role":"user","content":"hi"}],"namespace":"personal:private"}"#,
        )
        .expect("valid body");
        assert_eq!(req.namespace.as_deref(), Some("personal:private"));
        // Absent namespace stays None (backward compatible).
        let req: ChatRequest =
            serde_json::from_str(r#"{"messages":[{"role":"user","content":"hi"}]}"#)
                .expect("valid body");
        assert_eq!(req.namespace, None);
    }

    /// Personal and private namespaces are protected; project/meta/absent are
    /// not.
    #[test]
    fn protected_namespace_classification() {
        assert!(is_protected_namespace(Some("personal:private")));
        assert!(is_protected_namespace(Some("personal")));
        assert!(is_protected_namespace(Some("private")));
        assert!(is_protected_namespace(Some("Personal:Private"))); // case-insensitive
        assert!(!is_protected_namespace(Some("projects:cascade")));
        assert!(!is_protected_namespace(Some("meta")));
        assert!(!is_protected_namespace(None));
    }

    /// The GP-first preference is consulted when no explicit provider is set
    /// and the namespace does not require trusted-only routing. Plain personal
    /// chat may use the fleet; vault/private chat must not be steered there.
    #[test]
    fn gp_preference_gated_by_namespace_and_explicit_provider() {
        // Default path: no provider, unprotected namespace → GP may be preferred.
        assert!(gp_preference_allowed(None, None));
        assert!(gp_preference_allowed(None, Some("projects:cascade")));
        assert!(gp_preference_allowed(None, Some("meta")));
        assert!(gp_preference_allowed(None, Some("personal")));
        assert!(gp_preference_allowed(
            None,
            Some("personal:topic:road-trip")
        ));

        // Trusted-only namespaces: never steered to the pool.
        for ns in ["personal:private", "private", "vault"] {
            assert!(!gp_preference_allowed(None, Some(ns)), "{ns}: must skip GP");
        }

        // Explicit provider always wins, protected or not.
        assert!(!gp_preference_allowed(Some("anthropic"), None));
        assert!(!gp_preference_allowed(
            Some("local:ollama"),
            Some("personal")
        ));
    }

    /// Protected namespaces neutralise every content-capturing / GP-touching
    /// flag (context_sync, compress_context, classify_requests) while the
    /// pure-local inject_context passes through; unprotected namespaces keep
    /// the configured flags byte-for-byte.
    #[test]
    fn protected_namespace_neutralises_capturing_middleware() {
        let all_on = crate::config::MiddlewareConfig {
            compress_context: true,
            inject_context: true,
            classify_requests: true,
            context_sync: true,
        };

        for ns in ["personal:private", "personal"] {
            let eff = effective_middleware_flags(all_on, Some(ns));
            assert!(!eff.context_sync, "{ns}: no digest capture");
            assert!(!eff.compress_context, "{ns}: no GP compression");
            assert!(!eff.classify_requests, "{ns}: no GP classification");
            assert!(eff.inject_context, "{ns}: local-only injection allowed");
            // The handler's sync guard sees the effective flags — private
            // chats never accumulate or spawn a context-sync task.
            assert!(!crate::context_sync::should_sync(&eff));
        }

        for ns in [Some("projects:cascade"), Some("meta"), None] {
            let eff = effective_middleware_flags(all_on, ns);
            assert!(eff.context_sync && eff.compress_context && eff.classify_requests);
        }
    }

    /// Classification is only worth a GP call when the picked provider has a
    /// downgrade step — must stay in lock-step with `downgraded_model_for`.
    #[test]
    fn provider_can_downgrade_matches_downgrade_map() {
        use cascade_core::middleware::RequestComplexity::Trivial;
        for p in ["gemini", "gp-pool"] {
            assert!(provider_can_downgrade(p));
            assert!(downgraded_model_for(p, Trivial).is_some());
        }
        for p in ["anthropic", "openai", "local:ollama", "noop-x"] {
            assert!(!provider_can_downgrade(p), "{p}");
            assert!(downgraded_model_for(p, Trivial).is_none(), "{p}");
        }
    }

    /// Classification may only DOWNGRADE: Trivial on the Gemini family steps
    /// down to flash-lite; everything else keeps the caller's choice.
    #[test]
    fn downgrade_model_map_is_downgrade_only() {
        use cascade_core::middleware::RequestComplexity::{Complex, Medium, Trivial};
        assert_eq!(
            downgraded_model_for("gp-pool", Trivial).as_deref(),
            Some("gemini-flash-lite-latest")
        );
        assert_eq!(
            downgraded_model_for("gemini", Trivial).as_deref(),
            Some("gemini-flash-lite-latest")
        );
        // Never upgrade, never touch other providers.
        assert_eq!(downgraded_model_for("gp-pool", Medium), None);
        assert_eq!(downgraded_model_for("gp-pool", Complex), None);
        assert_eq!(downgraded_model_for("anthropic", Trivial), None);
        assert_eq!(downgraded_model_for("openai", Trivial), None);
        assert_eq!(downgraded_model_for("local:ollama", Trivial), None);
    }
}
