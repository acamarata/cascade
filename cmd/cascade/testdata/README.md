# cmd/cascade test fixtures

Fixtures the `cmd/cascade` tests read. Go runs package tests with the package
directory as the working directory, so tests open these as `testdata/<name>`.

## conductor_wiring_transcript.json

The request and reply frames of the conductor wiring handshake, in order.
`TestConductorWiringTranscriptLoads` (`conductor_wiring_transcript_test.go`)
records them by driving the real in-process path: `RegisterConductorExecuteHandler`
on a fresh RPC registry, then `conductor.execute` dispatched through it. No
daemon process, network or credentials are involved.

The file holds two pairs:

1. A well-formed execute request and its reply. The test daemon has no
   security pipeline wired, so the executor refuses it as unavailable.
2. A malformed request (a number where a string belongs) and its reply,
   refused at parameter decode as invalid input.

Each frame has `direction` (`request` or `reply`). A request carries `method`
and `params`; a reply carries `result` or `error` (`code`, `message`).

The test parses the file strictly (unknown fields fail), checks that it
round-trips through the transcript struct byte for byte, and compares it with a
fresh in-process run. Integration tests that need the handshake without a
daemon load the same struct.

Regenerate after an intended change to the handshake:

```sh
CASCADE_TESTKIT_UPDATE_GOLDEN=1 go test -count=1 -run '^TestConductorWiringTranscriptLoads$' ./cmd/cascade/
```

The update is ignored when `CI` is set. Review the diff before committing: a
changed frame shape is a schema change, not a refresh.

## Other fixtures

- `cc-hook-fixtures/`: hook payload fixtures; see its own README.
- `*.golden`, `golden_*.txt`: golden outputs compared by the CLI tests; the
  test that reads each one names how to regenerate it.
