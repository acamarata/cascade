# Model Access & Routing

Cascade keeps your primary Claude session free for chatting while routing extra
work (drafting, research, code review, QA) across every account and tool you
have, automatically, by live quota.

## Lanes

`cascade dispatch` can delegate to local CLIs as workers — each is detected on
your PATH and never fabricates output if absent:

| Lane | Tool |
|---|---|
| Extra Claude accounts | `claude -p` (acc2, acc3…) |
| Codex / ChatGPT | `codex exec` |
| Google Pro (Gemini) | `agy -p` |
| OpenCode-Go | `opencode run` |
| GFP (free Gemini Flash pool) | provider pool |
| Local LLM | on-device model |

## The routing matrix

`cascade dispatch --route <class>` picks a lane by task class and live quota
headroom (from `quota-store.json`):

| Task class | Routed to |
|---|---|
| Interactive chat / final gate | primary Claude (reserved) |
| Bulk execution | extra Claude accounts (drain first) → Codex → OpenCode-Go |
| Cheap / classify / taxonomy | GFP free Flash (maximized) → local |
| Adversarial review (CR/QA/research) | a different model family than the author |
| **Sensitive (PII/VA/health/personal)** | **Claude or local only — never external, never synced** |

The primary Claude account stays reserved for interactive use; additional
accounts are used first; the free GFP pool is maximized for cheap work and
review. Paid-API overage is off by default. Multi-account sub-pooling is a
private, opt-in, feature-gated capability.
