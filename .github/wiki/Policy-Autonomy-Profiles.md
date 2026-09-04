# Autonomy profiles

An autonomy profile decides how much Cascade may do without asking. It is the
top of the authorization stack: the per-risk-level default verdict, applied
only when nothing more specific matched.

The one rule to keep in mind: **a profile can restrict, never widen.** No
profile name, no config overlay, and no combination of the two can let through
something a lower layer refused.

## The verdict table

Every profile holds five slots, one per risk level. A slot carries a verdict —
`allow`, `ask` or `deny` — and an auto-advance flag that says whether an
autonomous loop may continue past that level without a human turn.

`never-auto` is not a verdict. It is the auto-advance flag being off on a slot
that still asks.

## Built-in profiles

| Level | What it is | `balanced` (default) | `strict` |
|---|---|---|---|
| L0 | read-only | allow, auto-advance | allow, auto-advance |
| L1 | safe local dev (tests, lint, build) | allow, auto-advance | ask |
| L2 | workspace mutation | ask | ask |
| L3 | external side effect (push, PR, network, messages) | ask, never auto | deny |
| L4 | destructive or privileged | deny | deny |

`balanced` is the risk ladder from the spec, verbatim, and it is the most
permissive table that exists. There is no full-autonomy profile: nothing you
can name in config allows L2 or above without asking.

`custom` starts from `balanced` and expects overlays.

Two ceilings sit above every profile and cannot be raised by any of them:

- **L4 is always deny.** A destructive or privileged action needs same-turn
  authorization, which a standing profile cannot give.
- **Auto-advance is L0/L1 only, and only on an allow verdict.**

## Config

```toml
[policy]
autonomy_profile        = "balanced"   # balanced | strict | custom
ask                     = ["L1"]       # tightening overlays, by level
deny                    = ["L2", "L3"]
approval_batch_window_s = 10
approval_batch_cap      = 20
```

The `allow`, `ask` and `deny` lists move individual levels. A level may appear
in at most one list.

**Overlays only tighten.** `allow = ["L2"]` under `balanced` is refused,
because L2 already asks — the config surface cannot be used to widen the table
it names.

The section is hot-reloadable. A valid reload swaps the running profile
atomically and is visible to the very next decision; there is no cache.

## What happens when something is wrong

Every one of these resolves to the most restrictive behaviour, never the most
permissive:

| Situation | Result |
|---|---|
| No profile loaded yet | every level denies |
| Profile name misspelled or unknown | config refused, running profile kept |
| `autonomy_profile = ""` | config refused |
| Misspelled key under `[policy]` | config refused |
| A level name that is not L0–L4 | config refused |
| A level named by two overlay lists | config refused |
| An overlay that would widen a slot | config refused |
| Approval numerics out of range | config refused |

A refused reload emits `config.reload.rejected` and leaves the running profile
and approval numerics exactly as they were. A successful load emits
`policy.autonomy.loaded`.

## Where it sits

The evaluation order is: deny-list, elevation, standing grants, capability
defaults, **autonomy profile**, fail-closed fallback. First match wins, so the
profile is consulted only for actions no layer above it settled. An
unregistered capability denies before any profile is read, and a capability
whose own class sits higher than the classified action raises the level rather
than lowering it.
