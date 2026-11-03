# antigravity stream-json fixtures

`agy-1.4.12-print.jsonl` is one real headless run of the antigravity CLI,
captured once on 2026-11-03 with agy 1.4.12 (darwin):

    agy -p "Reply with exactly the word ok and nothing else. Do not use any tools." \
        --output-format stream-json --mode plan --print-timeout 90s

Edits after capture: the `init.cwd` value (a scratch directory) was
rewritten to `/work`. Nothing else was changed. The file carries no
credential, account name or token.

Event shape the decoder pins (protocol revision 1):

| event         | payload key   | fields read                                              |
|---------------|---------------|----------------------------------------------------------|
| `init`        | `init`        | top-level `conversation_id`                              |
| `step_update` | `step_update` | `conversation_id`, `step_index`, `state`, `step_type`, `text_delta` |
| `result`      | `result`      | `conversation_id`, `status`, `response`                  |

Any other `event` value is protocol drift and refuses with
`provider.ErrHarnessIncompatible`. Output that does not open with `init`
(text or json output format) refuses with `ErrInterfaceAbsent`.
