# Telemetry

What this release ships for telemetry, and what it does not.

## Disposition

P1 ships two things: this document, and the `[telemetry]` config section
in `config.toml`. Nothing else. There is no telemetry endpoint, no
transport, no collector, no sampling, and no emission code anywhere in
this tree. Telemetry endpoint and transport are explicitly deferred to a
later release. Until that lands, enabling `telemetry.enabled` changes
nothing observable: no config format the shipped binary sends anything,
because the code path that would send it does not exist.

## Shipped default

Telemetry is off by default. A fresh install, a fresh `config.toml`, and
every profile resolve `telemetry.enabled = false` with zero user action.

```toml
[telemetry]
enabled = false   # opt-in only; shipped default off
```

The only supported opt-in paths are:

- an explicit edit of `telemetry.enabled = true` in `config.toml`, or
- the init wizard's telemetry prompt (step 7), whose default answer is
  No.

There is no third path. Environment variables cannot enable telemetry;
see below.

## Hard-disable

`CASCADE_TELEMETRY=0` forces `telemetry.enabled = false` regardless of
whatever `config.toml` says. This is an unconditional override: it wins
over an explicit `enabled = true` in the file.

`CASCADE_TELEMETRY` has no enable-side effect. No value of it turns
telemetry on. Setting it to `1`, `true`, or anything other than the
disable value changes nothing; the only defined behavior is disabling.

## Never force-enable

The generic per-section environment override
(`CASCADE_<SECTION>__<KEY>`, here `CASCADE_TELEMETRY__ENABLED`) may only
narrow telemetry from on to off. It can never widen telemetry from off to
on. A `CASCADE_TELEMETRY__ENABLED=true` set in an environment where
`config.toml` has telemetry off is refused: the resolved value stays
`false`.

The rule, stated once: no environment input, of any shape, can be the
reason telemetry ends up enabled. Enabling is always a config-file edit
or a wizard answer that a person made on purpose.

## Config reference

| Key | Type | Default | Reload class |
|---|---|---|---|
| `telemetry.enabled` | bool | `false` | hot |

The section is hot-reloadable: a daemon picks up a `telemetry.enabled`
edit on the next config reload, under the same whole-file
validate-before-apply rule every other hot section uses. An invalid value
(anything other than a boolean) is rejected with a typed config error
naming `telemetry.enabled`; the file is not silently coerced or ignored.

## What is not here

- No telemetry endpoint or transport. Deferred.
- No collector, sampler, or metrics emitter for telemetry specifically.
  (In-product operational metrics such as `cascade top`'s live resource
  view are a separate, unrelated surface and are not telemetry.)
- No CLI verb. There is no `cascade telemetry ...` command. The only user
  surface over this key is the general-purpose `cascade config
  get/set/list`.
- No egress registration. The egress inventory names a telemetry class
  for future use, but nothing transits under it in this release: nothing
  is collected, and nothing leaves the machine.

## Crash reporting

Crash reporting, when it exists, follows the same posture: opt-in only,
off by default. No crash reporter ships in this release; this section
states policy for if and when one is built, not a shipped capability.
