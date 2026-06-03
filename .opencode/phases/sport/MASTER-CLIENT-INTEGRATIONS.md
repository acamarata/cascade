# MASTER-CLIENT-INTEGRATIONS.md — Cascade AI Provider Integrations

**Purpose:** Registry of every AI provider adapter in cascade-providers.
**Status legend:** ✅ Done · 🟡 Partial · 🔲 Planned · 🚧 In Progress · 🔒 Blocked · 🚫 Deferred
**Last updated:** 2026-05-31
**Source:** Cascade P3/E-05 plan

| Provider | Adapter path | Description | Status | Phase | Creating tickets |
|---|---|---|---|---|---|
| Anthropic (Claude) | crates/cascade-providers/src/adapters/anthropic.rs | Claude API: completions, streaming, tool use | 🔲 Planned | P3 | T-P3-E05-* |
| OpenAI | crates/cascade-providers/src/adapters/openai.rs | OpenAI API: GPT completions, streaming, function calling | 🔲 Planned | P3 | T-P3-E05-* |
| Gemini | crates/cascade-providers/src/adapters/gemini.rs | Google Gemini API: completions, multimodal | 🔲 Planned | P3 | T-P3-E05-* |
| OpenRouter | crates/cascade-providers/src/adapters/openrouter.rs | OpenRouter meta-API: model routing, cost tracking | 🔲 Planned | P3 | T-P3-E05-* |
| DeepSeek | crates/cascade-providers/src/adapters/deepseek.rs | DeepSeek API: completions | 🔲 Planned | P3 | T-P3-E05-* |
| Ollama | crates/cascade-providers/src/adapters/ollama.rs | Ollama local API: chat, completions, model management | 🔲 Planned | P3 | T-P3-E05-* |
| Local LLM | crates/cascade-local-llm/src/lib.rs | llama.cpp / candle: local model inference | 🔲 Planned | P3/P4 | T-P3-E05-*, T-P4-E02-* |
