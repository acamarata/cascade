# Agent Session JSON-RPC Test Fixtures

## Fixture Format

The `agent-session.json` file contains a JSON array of provenance-stamped
JSON-RPC request/response pairs. Each element has three top-level keys:

- **provenance**: metadata about how and when the exchange was captured.
  - `capturedAt`: ISO 8601 timestamp of the recording.
  - `source`: identifier of the component that produced the trace.
  - `schema`: the contract schema the exchange conforms to (e.g. `contract:agent-identity`).
- **request**: a standard JSON-RPC 2.0 request object with `jsonrpc`, `id`,
  `method`, and `params` fields.
- **response**: a standard JSON-RPC 2.0 response object with `jsonrpc`, `id`,
  and `result` or `error` fields.

### Recorded Methods

The fixture includes exchanges for the following RPC methods:

- `agent.get`: retrieves a single agent by its minted ID (`agt_` prefix).
- `agent.list`: returns all known agents and their current states.

## How It Was Captured

These fixtures were recorded from a running daemon instance using a loopback
listener. The capture process stamps each exchange with provenance metadata
so that downstream consumers can verify the schema version and origin. No
real credentials, keychain entries, or operator configuration are included.

## Usage for Replay Testing

Builder agents and acceptance tests should load `agent-session.json` and
replay each request against a mock or test RPC registry. For every record:

1. Parse the `request` object and dispatch it through the test handler.
2. Compare the actual response against the recorded `response` object.
3. Verify that the `provenance` block is present and carries the expected
   `schema` value matching `contract:agent-identity`.

This replay approach drives acceptance criterion [6] and ensures that any
change to the agent identity contract is caught early. Tests must run
offline, use only loopback listeners, and never touch the real keychain,
secret service, or operator configuration.
