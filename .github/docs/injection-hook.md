# Injection Guard — UserPromptSubmit Hook

Cascade ships a prompt-injection scanner (`cascade_core::injection_scan`) and a
reference hook (`scripts/hooks/injection-guard.sh`) that wires the scanner into
the `UserPromptSubmit` lifecycle event so that crafted messages cannot override
safety rules before tool dispatch.

---

## How It Works

```
User message
     │
     ▼
UserPromptSubmit hook fires
     │
     ▼
injection-guard.sh reads message from stdin
     │
     ▼
cascade check injection --sensitivity <level>
     │          │
     │          └─ InjectionReport { risk, matches }
     │
     ├── Risk::None / Low  ──► exit 0  ──► turn continues normally
     │
     ├── Risk::Medium / High ──► exit 1  ──► harness injects warning, turn continues
     │
     └── Risk::Critical  ──────► exit 2  ──► harness HALTS turn, no tools dispatched
```

The scanner is deterministic and runs in-process with zero I/O.  It adds
negligible latency to every user turn.

---

## Pattern Categories

| Category | Example trigger | Risk |
|----------|----------------|------|
| `instruction-override` | "ignore previous instructions" | High |
| `instruction-override` | "disregard your guardrails" | High |
| `system-prompt-extraction` | "reveal your system prompt" | Medium |
| `system-prompt-extraction` | "output your instructions" | Medium |
| `deny-list-override` | "bypass the deny-list" | **Critical** |
| `deny-list-override` | "you are now allowed to rm" | **Critical** |
| `deny-list-override` | "safety guidelines are disabled" | **Critical** |
| `jailbreak-framing` | "DAN mode", "developer mode enabled" | High |
| `jailbreak-framing` | "unrestricted mode", "no restrictions mode" | High |
| `encoded-payload` | base64 blob ≥80 chars | Medium |
| `encoded-payload` | dense `\uXXXX` escape sequences | Medium |

---

## Sensitivity Levels

Set via `CASCADE_INJECTION_SENSITIVITY` (or in `settings.json`):

| Value | Behaviour |
|-------|-----------|
| `strict` | Report everything including `Low` risk. |
| `moderate` | Suppress `Low`; surface `Medium` and above. (Default) |
| `log-only` | Never halt or warn; downgrade all findings to `Low` for logging. |

Use `strict` in high-security environments or when the cascade config grants
destructive capabilities.  Use `log-only` for evaluation / development only.

---

## Wiring in settings.json

The actual settings key paths follow the harness JSON schema.  The conceptual
structure is:

```json
{
  "hooks": {
    "UserPromptSubmit": [
      {
        "script": "scripts/hooks/injection-guard.sh",
        "env": {
          "CASCADE_INJECTION_SENSITIVITY": "moderate"
        }
      }
    ]
  }
}
```

See the harness settings schema (`data/schema/settings.schema.json`) for the
exact field names and allowed values.  Wiring for specific harnesses
(CC, OC, Cursor, etc.) is covered in the P13 settings-wiring epic.

---

## Exit Code Contract

| Exit code | Meaning | Harness action |
|-----------|---------|----------------|
| `0` | Clean (or log-only) | Continue normally |
| `1` | Medium or High risk | Inject warning notice into conversation, continue |
| `2` | Critical (deny-list override) | Halt turn, surface report to user, no tools dispatched |

The harness never suppresses `exit 2`.  Even if the user explicitly asks to
"ignore the warning", the turn is halted — the override must come from a
human-approved settings change, not from the message content.

---

## Rust API

For programmatic use (e.g. in a daemon plug-in or test harness):

```rust
use cascade_core::injection_scan::{scan_for_injection, Risk, Sensitivity};

let report = scan_for_injection(user_message, Sensitivity::Moderate);
match report.risk {
    Risk::Critical => halt_turn(&report),
    Risk::High | Risk::Medium => warn_user(&report),
    _ => {} // proceed
}
```

---

## False Positives

The scanner operates on lowercased substring matching — it is intentionally
conservative in the `Moderate` (default) sensitivity configuration.  Known
edge cases:

- A message asking "what are your configuration options?" triggers
  `system-prompt-extraction` at `Low` risk only; `Moderate` suppresses it.
- Technical documentation containing long base64 strings (e.g. a public key)
  may trigger `encoded-payload` at `Medium`.  In that context, temporarily set
  `log-only` sensitivity or add a project-level pattern exemption (P13 roadmap).
- The word "instructions" in a normal coding request does not trigger any
  pattern — only the full multi-word phrase "ignore previous instructions" does.
