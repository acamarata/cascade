# Privacy Policy — Cascade

## What Cascade collects

Cascade collects structured tracing spans from the `cascaded` daemon and the
`cascade` CLI. These spans describe internal operations: daemon startup, IPC
calls, provider health checks, and command timing. They do not contain file
contents, conversation text, API keys, or personally identifiable information.

## Local-only by default

Telemetry is **disabled by default**. No data leaves your machine unless you
explicitly enable it. The default configuration writes nothing; it does not
construct an OTLP exporter.

When you enable telemetry, spans are exported to the OTLP endpoint you
configure (for example, a local Jaeger or Grafana Tempo instance). Cascade
does not operate any remote telemetry collector. If you configure an external
endpoint, you control it.

## What is never collected

- File contents or paths from your projects
- Conversation or prompt text
- API keys or authentication tokens
- Output from AI model calls
- Personally identifiable information

An automated lint check (part of the Cascade test suite) prevents span
attributes named `query`, `content`, `text`, `path`, `key`, or `secret` from
appearing in the telemetry module.

## How to opt in

```toml
# ~/.cascade/config.toml
[telemetry]
enabled  = true
endpoint = "http://localhost:4317"   # your local OTLP collector
```

Or via the CLI:

```sh
cascade telemetry enable
cascade telemetry status
```

## How to opt out

```sh
cascade telemetry disable
```

Or set `enabled = false` in `~/.cascade/config.toml`.

## Where data lives

- Local log files: `~/.cascade/logs/` (structured JSON, mode 0600)
- OTLP export: only to the endpoint you configure; never to a Cascade-operated server

## Changes to this policy

This file is version-controlled in the Cascade repository. Any change to what
is collected or how it is transmitted will appear in the changelog and in this
file.
