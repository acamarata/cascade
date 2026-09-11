# Crash-reporting policy

Opt-in only, off by default. This mirrors the telemetry disposition (see
`docs/TELEMETRY.md`): whatever capability exists in the future for
capturing and reporting crashes is disabled until a person turns it on,
never enabled by default and never enabled by an environment variable.

## What this release ships

No crash reporter ships in this release. There is no crash-capture code,
no crash-report transport, and no crash-report storage anywhere in this
tree. This page states binding policy for when such a capability is
built, not a description of something already running.

## The policy, for when a crash reporter exists

- Default disposition: off. A fresh install never reports a crash without
  an explicit opt-in.
- Opt-in surface: an explicit config edit, or an installer/wizard prompt
  whose default answer is No, matching the pattern
  `docs/developer/versioning/node-skew.md` and `docs/TELEMETRY.md` both
  use for their own opt-in surfaces.
- No environment variable may force crash reporting on. The same
  never-force-enable rule telemetry's `CASCADE_TELEMETRY` switch follows
  applies here: an environment override may narrow (turn off) but never
  widen (turn on).
- A crash report, when opted in, contains only what is needed to diagnose
  the crash. What exactly that is remains for the ticket that implements
  the feature to define and document; this page does not pre-approve a
  payload shape it cannot yet describe honestly.

Until that ticket lands, "crash reporting" in cascade means: nothing is
captured, nothing is sent, and there is no setting whose value matters
because there is no code reading it.
