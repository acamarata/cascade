# Controller designation

A cascade home is either the controller or a node. It is never both, and
it is never the controller by default. The daemon reads its role from one
of two files in `<data dir>/nodes/`:

| File | Written by | Makes this home |
|---|---|---|
| `controller_designation.json` | `cascade init`, the first daemon start (bootstrap), `nodes.Handover` | the controller |
| `controller_binding.json` | `cascade node bind` | a node |

Both files are same-uid data, not a security boundary. A process running
as the same user that deletes a node binding and writes a designation only
makes that machine refuse (both records present) or run as a controller of
its own, holding a key that no enrolled node has pinned.

## The designation file

- Path: `<data dir>/nodes/controller_designation.json`, mode `0600`, under
  a `0700` directory. Written atomically (temp file, fsync, rename).
- Fields: `installation_id` (`inst_` plus 128 random bits in base32,
  minted at the first write, never derived from the host or a path),
  `controller_node_id` (this machine's node identity), `epoch`,
  `designated_at`, `reason` (`init`, `bootstrap` or `handover`).
- Unknown fields are an integrity error on read. An empty field or an
  epoch below 1 is refused on write.

## Epoch

- The first write is epoch 1. A handover writes exactly the stored epoch
  plus 1 for the same installation id. An equal or lower epoch, a skipped
  epoch or a different installation id is refused and nothing is written.
- Epoch is carried for a later controller lease, which is fenced on
  it. No P1 frame or envelope binds it.
- There is no automatic election and no promotion when a controller is
  lost. Handover is an API only in P1; there is no CLI for it.

## Who writes it

- `cascade init` designates the controller just before it installs the
  daemon service. A replay finds the record for this node and writes
  nothing. A home that holds a binding with an endpoint, or a designation
  for another node, stops init with a conflict error.
- Bootstrap at the first daemon start writes epoch 1, reason `bootstrap`,
  only when there is no designation, no binding file and no
  `nodes/bound_once` tombstone. Anything else is a role conflict and
  nothing is written.
- `cascade node bind` may remove only an untouched bootstrap designation
  (reason `bootstrap`, epoch 1, no device records, no job rows). It writes
  the `bound_once` tombstone first, so bootstrap never runs on that home
  again, even after a crash.

Residual: a node that lost its binding before upgrading to a build that
writes the tombstone has no tombstone, so bootstrap designates it. It
then holds a controller key no other node has pinned, so it controls
only itself.

## Role resolution and remedies

The role resolves to controller only from a readable designation, and to
node only from a complete binding (endpoint and pinned controller key).
Every other state is a role conflict: an integrity error, and
controller-only RPCs refuse. `cascade doctor` reports it as the
`controller_role` check: `ok` for controller or node, `error` for every
conflict kind, with the remedy below. One exception: a home that still
meets the bootstrap preconditions (no designation, no binding file, no
`bound_once` tombstone) reports `warn`, not `ok`, because the first
daemon start will designate it. Doctor never reports it as a controller.

| Conflict kind | Meaning | Remedy |
|---|---|---|
| `none` | neither file exists and `bound_once` exists (warn when no tombstone: bootstrap pending) | run `cascade init` on a controller or `cascade node bind` on a node |
| `both` | a designation and a binding both exist | keep one role: remove the binding and run `cascade init`, or re-run `cascade node bind` |
| `binding-incomplete` | the binding has no endpoint or no controller key | re-run `cascade node bind` on this node |
| `load-error` | a file could not be read or parsed | repair the file, then run `cascade init` or `cascade node bind` |
| `no-source` | the daemon has no designation backend | run `cascade init` on a controller or `cascade node bind` on a node |

## Upgrades

| Starting point | What happens |
|---|---|
| Controller from before designation | bootstrap writes epoch 1 at the first daemon start |
| Node with an old binding (no controller key) | resolves `binding-incomplete`; re-run `cascade node bind` |
| Home migrated with `cascade migrate v1` | bootstrap writes epoch 1 at the first daemon start |
