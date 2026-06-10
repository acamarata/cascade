# Telemetry Consent

Cascade collects no telemetry by default. This page describes the optional telemetry, what it contains, how to opt in or out, and where data goes if you consent.

---

## Default: off

On a fresh install, telemetry is disabled. The onboarding wizard asks once whether you want to help improve Cascade by sharing crash reports and usage counts. If you skip the wizard or decline, no data is ever sent.

---

## What optional telemetry includes

If you opt in, Cascade sends anonymized data only:

| Data type | Example | Why |
|---|---|---|
| Crash report (panic) | Stack trace, Rust crate + line | Find and fix crashes |
| Command invocation count | `cascade search` called 12 times | Understand which features are used |
| Cascade version | `0.1.0` | Prioritize support for older versions |
| OS type | `macOS`, `Linux`, `Windows` | Platform-specific bug reports |
| Index size tier | `<100 docs`, `100–1000 docs`, `>1000 docs` | Scale planning |

**What it never includes:**
- File contents or instruction text
- Search queries or results
- Repo names, file paths, or usernames
- API keys or tokens
- Any personally identifiable information

---

## How to opt in

During the onboarding wizard, choose "Yes" on the telemetry prompt.

Or enable manually:

```bash
cascade config set telemetry.enabled true
```

---

## How to opt out (or verify you are opted out)

```bash
cascade config get telemetry.enabled
# should print: false
```

To disable if currently enabled:

```bash
cascade config set telemetry.enabled false
cascade daemon restart
```

---

## Where data goes

Crash reports use the Sentry SDK, sent to a Sentry project owned by the `acamarata` account. Usage counts are sent to a self-hosted endpoint. No data is shared with third parties, sold, or used for advertising.

---

## Verification

To confirm no outbound connections are made when telemetry is disabled, you can monitor network traffic with `lsof` or `Little Snitch`:

```bash
# Check what the daemon connects to
lsof -n -i -p $(pgrep cascade-daemon)
```

With telemetry off, the only outbound connections should be from explicit commands (`cascade update check`, LLM proxy, plugin network calls).

---

See also: [Privacy](Privacy.md) · [Security](Security.md)
