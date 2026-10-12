# Migration rollback guide

Rollback is a routing change back to the unchanged v1 installation, not a
reverse import. v1 remains available and authoritative until a surface passes
the go/no-go list in the [side-by-side protocol](side-by-side-protocol.md).
Never overwrite v1 data with a v2 export. The commands below are examples for
an operator to execute deliberately; this document does not run them.

## Trigger conditions by severity

| severity | trigger |
|---|---|
| Critical | A restore or import would expose the wrong domain, lose source data, or leave the ledger claiming success for incomplete state. Stop v2 writes and preserve both copies. |
| High | Any required proof fails: doctor exit 0, migration ledger rows done, exact recall parity, tripwire clean, or sweep zero UNRESOLVED. Do not continue cutover. |
| Medium | A surface's cutover proof or rollback owner is missing, or its v1 source cannot be confirmed intact. Keep that surface on v1. |
| Low | The evidence record or operator handoff is incomplete while all state remains unchanged. Defer cutover until the record is complete. |

## Revert blocks by surface

Every block first creates a v2 backup, then stops the v2 daemon. Stopping the
daemon stops every v2 surface at once; re-enable only the surfaces that passed
their proof. If `cascade backup create` fails, stop there: do not run
`cascade daemon stop` until a backup exists or the operator records why not.
Keep the v1
installation and its source untouched; resume service from that existing v1
installation using the site's established routing. Do not delete or overwrite
either installation during rollback.

### config

For imported config, return the config writer to the intact v1 source. Do not
copy the v2 config over it.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### memory

Return memory reads and writes to the v1 memory collection. The importer copied
data into v2; rollback does not rewrite the v1 collection.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### vault

Return secret reads and writes to the existing v1 vault source. Keep the v1
custody backend available; never export v2 secret values into shell arguments.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### accounts

Use the v1 account registry and credentials again. v2 account imports require
their own re-authentication; the import does not modify v1 credentials.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### context

No v1 context data is imported. Stop the v2 context surface and leave v1
serving it.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### fleet

No v1 fleet data is imported. Stop the v2 fleet surface and leave v1 serving
it.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### conversation

No v1 conversation data is imported. Stop the v2 conversation surface and
leave v1 serving it.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### plugins

No v1 plugin state is imported. Stop the v2 plugin surface and leave v1
serving it; retain the v2 plugin records for investigation.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### nodes

No v1 node data is imported. Stop the v2 node surface and leave v1 serving it;
do not revoke a signing key as part of this rollback.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### sync

No v1 sync data is imported. Stop the v2 sync surface and leave v1 serving it;
do not resolve a conflict during rollback.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

### backup

No v1 backup data is imported. Stop the v2 backup surface and retain both
backup sets; do not restore a v2 snapshot into v1.

```console
$ cascade backup create --yes
$ cascade daemon stop
```

## v1 restore verification

1. Confirm the original v1 data paths and the existing v1 backup remain
   available. Do not restore over either copy.
2. Start the existing v1 installation using its established service procedure.
3. Run its v1 health command and confirm it reports healthy before routing
   traffic back to it.
4. Keep v2 stopped until its failed proof has been understood and a new
   cutover decision is recorded.

Run the v1 health command with the v1 binary by its own path or install name,
not the v2 `cascade` on PATH. `cascade doctor` below is the v1 command name
and would otherwise resolve to v2.

```console
# v1
$ cascade doctor
```

## Post-rollback health commands

Use the installed v2 binary to verify its daemon is stopped and its backup is
still listed. Use the v1 command above to verify the restored writer.

```console
$ cascade daemon status
$ cascade backup list
```

Record the outputs, the surface, and the operator in the change evidence. A
rollback is complete only when v1 is serving, both source copies remain
intact, and the v2 service is stopped.
