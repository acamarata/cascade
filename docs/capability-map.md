# Capability map

This page maps every Cascade capability to the code path that serves it and
says how well that path is proven. The table below is checked by a test
harness, so a row cannot claim more than a passing probe backs up.

## What the table is

Each row ties one capability to one entrypoint. A capability is one id from
`internal/build/testdata/capability-ids.txt`, the single public capability
list. An entrypoint is where a caller reaches the capability: a CLI verb, an
RPC method, a daemon subsystem, a plugin, an MCP tool, or a job or scheduler
consumer. The production path is the chain from that entrypoint through the
composition root to the code that does the work. A capability may have several
rows. Every id in the list needs at least one.

The columns are:

- `capability`: the capability id, written exactly as it appears in the list.
- `entrypoint`: the CLI verb, RPC method, subsystem, plugin, tool or consumer.
- `production_path`: how the entrypoint reaches the code, in plain words.
- `classification`: one of the four values below.
- `owner`: who closes the gap. Use `-` on a verified row.
- `evidence`: for a verified row, the probe that proves it; otherwise a short
  note on what was checked or what is missing.

A cell cannot be empty and cannot contain a pipe character.

## Classification

- `verified`: a probe proves the path works. The evidence cell names the
  probe, written `TestCapmap_<Name>`.
- `present-unverified`: the code exists on the path, but no probe proves it.
- `missing`: the path does not exist or is not wired to its entrypoint.
- `policy-refused-as-specified`: the specification says Cascade refuses this on
  purpose, and the refusal is the behavior.

Every row that is not `verified` needs an owner: a planning ticket id of the
form `P<phase>-<EPIC>-<NN>`, or `NEW:P1-<CODE>` when a ticket does not exist
yet.

## What a verified probe asserts

A probe registered for a verified row makes four assertions about the real
binary, built once from the tree under test:

1. Authorization: an authorized caller succeeds, or an unauthorized caller gets
   the typed refusal.
2. Routing: the request reaches the named subsystem, not a stand-in.
3. Side effect: the change shows up in stored state, not only in a reply.
4. Result: the caller gets the documented result or receipt.

## How the harness checks the table

Run it with `go test -tags capmap -count=1 ./internal/integration/capmap/`. The
build tag keeps it out of the default test run.

`TestCapmapTable_Evaluates` runs the probe each verified row names as a
subtest. A probe counts as passed only if its subtest ran to the end, did not
fail and did not skip. The harness then checks the table against the capability
list and that set of passed probes in the same run. It fails on any of these:

- a capability in the list with no row, or a row whose id is not in the list;
- a wrong header, a separator row without six cells, a row with the wrong
  number of columns or without its leading pipe, a second table, or an empty
  cell;
- a classification outside the four values;
- a verified row that names no probe, or names one that was not registered,
  failed or skipped;
- a row that is not verified and has no owner.

The table starts with a header and no rows, so the harness fails until the
rows are filled in.

| capability | entrypoint | production_path | classification | owner | evidence |
| --- | --- | --- | --- | --- | --- |
