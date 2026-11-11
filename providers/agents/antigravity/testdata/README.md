# Antigravity protocol fixtures

Protocol revision: 1. Pinned executable version: 1.4.12, defined in
`../protocol.go`. The existing stream capture and its provenance are in
`stream-json/README.md` and `stream-json/agy-1.4.12-print.jsonl`.

Auth source: none: fixture-only. No credential source was verified in this
ticket. Live authenticated legs remain parked; tests use injected lookup
and help capture with fresh HOME and USERPROFILE directories.

Sandbox emitted: no. The existing recorded print command omits `--sandbox`.
There is no capture proving print mode works under that flag, so argv omits
it for none, readonly and all. A future verified sandbox capture must update
this decision and its regression test together. Plan mode is not evidence
of filesystem isolation.

`protocol/1/help-tokens.txt` is the required token contract transcribed from
the ticket's print invocation, not a newly captured live help transcript.
The supplied stream fixture establishes stream-json print output; it does
not establish the current build machine's installed help interface. Each
flag must appear as an option declaration; `stream-json` must appear under
the `--output-format` option, rather than in unrelated prose. Probe tests
derive synthetic help from this contract and remove each token in turn.
Version is reported from an agy/antigravity version heading when available;
runtime version compatibility remains the responsibility of CheckHarness.

Build-machine outcome (2026-11-11): unverified. This ticket's execution
surface permits Go verification commands only, so no installed `agy --help`
capture or live lookup was performed. No PRESENT claim is made from the
synthetic fixture. The integration owner must capture the installed help
and journal the real outcome before closing the live-interface deferral.
If that help lacks `--print` or `stream-json`, escalation is required.

The print invocation contains only --print, --output-format, --mode,
optional --model and --effort, and --print-timeout, followed by the prompt.
None/readonly select plan; all selects accept-edits. Unsupported defaults
and values beginning with a dash refuse. No end-of-options flag is assumed
without a capture proving support, so dash-leading prompts refuse.
ApprovalTimeout supplies the configured print timeout (zero uses 90s);
the builder caps it by the positive remaining envelope lifetime supplied
by the caller using its injected clock. No duration is rounded upward.

Registry integration note: pkg/provider/registry.go defines a read-only
record registry, with no probe registration method or probe struct seam.
These tests exercise the exported production Probe function directly.
Registering a probe against an unspecified registry API would require a
contract clarification and changes outside this ticket's scope.
