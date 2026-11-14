# Jobs RPC methods

Methods over the job evidence ledger. Access classes are listed in the
generated RPC method reference. Both methods below are served by
the same handler and are registered only when the daemon has an evidence
ledger.

### `jobs.evidence.list`

Lists the evidence rows recorded for one ticket.

Params (one object, unknown fields refused): `ticket`, required and non-empty;
anything else is `invalid-input`. A ticket with no evidence returns an empty
list, not an error.

Result: `{"ticket": "...", "rows": [...]}`. Each row carries `job_id`, `seq`,
`kind`, `producer_capability`, `attempt_id`, `checkpoint_id`, `tree_hash`,
`outcome`, `data_class`, `redacted`, the timestamps `started_at`, `ended_at`
and `created_at` (RFC 3339 UTC) and, when present, `attestor_identity`,
`result_hash`, `idempotency_key` and `artifact_ref`. Rows above the evidence
read ceiling are redacted before they are returned and carry `redacted: true`.

### `plugin.pbd.evidence`

The same handler under the name the phase-based-development plugin calls:
identical params, result and access class.
