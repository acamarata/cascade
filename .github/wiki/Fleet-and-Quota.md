# Fleet & Quota

Cascade can track usage across all your AI accounts in one place, locally.

## How it works

A background poller in the `cascaded` daemon refreshes a single store,
`~/.cascade/quota-store.json`, about once a minute. The menu-bar tray reads it
and shows a usage summary. Everything is local — no server, no cloud.

## Sources

| Provider | Status |
|---|---|
| GFP (Gemini Free Pool) | live — reads the key-pool usage |
| Claude (extra accounts) | planned (v1.2, via Claude Code CLI + PTY) |
| Codex / ChatGPT | planned (v1.2, via `codex exec`) |
| Google Pro (Gemini AGY) | planned (v1.2, via `agy -p`) |
| OpenCode-Go | planned (v1.2) |

Sources without a live reader report nothing — Cascade never fabricates usage
numbers.

## Configuration

```toml
# ~/.cascade/config.toml
[fleet]
enabled = true
interval_secs = 60   # 10–3600
```

## What's next (v1.2)

The quota store feeds a routing matrix: before dispatching extra work
(adversarial research, code review, QA), Cascade picks an account that has
headroom — maximizing your free and subscription quotas while keeping your
primary Claude session free for interactive use. Sensitive work (personal data)
is always kept on Claude or a local model.
