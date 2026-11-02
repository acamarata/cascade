# Evidence ledger reads

`jobs.evidence.list` and its mirror `plugin.pbd.evidence` take exactly
`{"ticket":"job-id"}`. Both verbs and the `jobs.evidence` SSE topic require
owner-only access. Principal tokens and requests without the owner marker
are refused. There is no evidence write verb.

The reply is `{"ticket":"job-id","rows":[]}` for an unknown ticket.
Otherwise rows appear in append sequence. Storage failures are returned as
errors rather than empty listings.

Each row contains `job_id`, `seq`, `kind`, `producer_capability`, `attempt_id`,
`checkpoint_id`, `tree_hash`, `attestor_identity`, `outcome`, `result_hash`,
`idempotency_key`, `artifact_ref`, `started_at`, `ended_at`, `created_at`,
`data_class`, and `redacted`. Timestamps use UTC RFC3339.

The class uses the stored vocabulary `public`, `internal`, `confidential`,
or `secret`. Append joins the record's sensitivity with its job's sensitivity;
callers can only raise it. The side table stores the resulting class outside
the evidence hash chain. Legacy rows inherit their job's class. Unknown
classes and unreadable jobs resolve to `secret`.

The fixed read ceiling is `internal`. Confidential and secret rows retain
their position and metadata, with `redacted: true`; `artifact_ref`,
`result_hash`, `attestor_identity`, and `idempotency_key` are omitted.
Public and internal rows pass through with `redacted: false`.
Requests cannot select or raise the ceiling.

Subscribe with `GET /events?topic=jobs.evidence&ticket=job-id`. Exactly one
non-empty `ticket` is required; missing, empty, or repeated values receive
HTTP 400. Each connection starts at the current bus head and receives only
subsequent changes for that exact ticket. Events above the ceiling remain
visible with `redacted: true`.

After an append commits and its audit effect is confirmed, the shared writer
publishes `jobs.evidence.changed` on namespace `jobs.evidence`, carrying
`job_id`, `seq`, `kind`, `outcome`, and `data_class`. The SSE data adds
`redacted`. Idempotent retries and refused writes publish nothing. Event
publication failures do not undo a committed row.

Daemon registration orders are fleet 6 for the verbs and event-topics 31 for
the topic; event-topics 30 belongs to the CI watch topic.
