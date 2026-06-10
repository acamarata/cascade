# Cascade Backups

The Cascade daemon automatically backs up each tier's `.cascade/` directory on every successful regeneration. Snapshots are timestamped and rotated to keep your disk usage bounded.

## Configuration

Backup is configured in your `cascade.toml`:

```toml
[backup]
enabled = true
backup_root = "~/.cascade/backups"
sync_interval_secs = 60
max_versions = 7
```

| Setting | Type | Default | Notes |
|---------|------|---------|-------|
| `enabled` | bool | `true` | Set to `false` to disable all backups |
| `backup_root` | string | `~/.cascade/backups` | Root directory for all backups (~ is expanded to `$HOME`) |
| `sync_interval_secs` | u64 | `60` | Throttle per-tier — only backs up once per N seconds, even if regeneration happens more frequently; set to `0` for no throttle |
| `max_versions` | u64 | `7` | Number of most recent snapshots to keep per tier; older snapshots are automatically deleted |

## Snapshot Layout

Each tier stores snapshots in its own directory:

```
~/.cascade/backups/
├── GCI/
│   ├── snapshot-2026-06-02-093015/
│   ├── snapshot-2026-06-02-094015/
│   ├── snapshot-2026-06-02-095015/
│   └── ...
├── ASI/
│   └── snapshot-2026-06-02-093600/
└── PPI/
    └── snapshot-2026-06-02-094200/
```

Snapshot directory names are ISO format: `snapshot-YYYY-MM-DD-HHMMSS`. This ensures lexicographic sort equals chronological order — useful for tools that list and compare snapshots.

## Rotation Policy

After each successful backup:

1. **Count** existing `snapshot-*` directories
2. **Sort** lexicographically (oldest first)
3. **Keep** the most recent `max_versions` snapshots
4. **Delete** any older snapshots

Example: If `max_versions = 7` and you have 10 snapshots, the 3 oldest are deleted, leaving exactly 7.

**Race conditions:** If multiple backup processes run concurrently (unlikely, but possible during config reload), rotation may briefly exceed `max_versions`. A warning is logged but backup continues. Consistency is restored on the next rotation.

## Listing Snapshots

Use the CLI to list snapshots for any tier:

```bash
cascade backup list GCI
cascade backup list PPI --backup-root /mnt/external-drive/cascade-backups
```

Output:

```
Found 7 snapshots for tier 'GCI' (newest last):
  snapshot-2026-06-02-093015
  snapshot-2026-06-02-094015
  snapshot-2026-06-02-095015
  snapshot-2026-06-02-100015
  snapshot-2026-06-02-101015
  snapshot-2026-06-02-102015
  snapshot-2026-06-02-103015
```

## Restoring from a Snapshot

To restore a tier from a snapshot:

1. **Stop** the cascade daemon: `cascade daemon stop`
2. **Identify** the snapshot you want to restore by date or by listing: `cascade backup list GCI`
3. **Copy** the snapshot back to its original location (full restore — data will be overwritten):

```bash
# Restore GCI from the snapshot-2026-06-02-103015 backup
rm -rf ~/.cascade  # Backup first!
cp -r ~/.cascade/backups/GCI/snapshot-2026-06-02-103015 ~/.cascade
```

Or selectively restore only certain files:

```bash
# Restore only memory/ subdirectory from the backup
cp -r ~/.cascade/backups/GCI/snapshot-2026-06-02-103015/memory/* ~/.cascade/memory/
```

4. **Restart** the daemon: `cascade daemon start`

## What's Included in a Backup

Each snapshot contains a complete copy of the `.cascade/` directory tree, except for:

- `logs/` — daemon log files (can grow large; not backed up)
- `temp/` — temporary files and session state (recreated on startup)
- `*.pid`, `*.lock` — process lock files (specific to the daemon instance)

All other files are included:
- `CLAUDE.md` — instructions
- `memory/` — decisions, lessons, patterns
- `tasks/` — task history
- `phases/` — active and archived phase state
- `docs/` — reference docs and master lists
- `inbox/` — PCI messages
- `config.toml` — configuration

## Verifying Backup Integrity

To check that a backup contains expected files:

```bash
# List contents of a snapshot
ls -la ~/.cascade/backups/GCI/snapshot-2026-06-02-103015/

# Verify key subdirectories exist
test -d ~/.cascade/backups/GCI/snapshot-2026-06-02-103015/memory && echo "memory/ ✓"
test -d ~/.cascade/backups/GCI/snapshot-2026-06-02-103015/tasks && echo "tasks/ ✓"
```

A full backup verification command is planned for a future release.

## Backup Frequency and Storage

- **Frequency:** Backups are triggered by successful CASCADE regeneration events. If your cascade regenerates every 10 seconds, backups may occur frequently (limited by `sync_interval_secs` throttle per tier).
- **Storage:** At `max_versions = 7`, typical disk usage is `7 × size_of_.cascade_directory` per tier. The `.cascade/` directory is usually 1–10 MB (depending on history size), so expect 10–70 MB per tier.
- **Automatic cleanup:** Old snapshots are deleted automatically during rotation. No manual cleanup is needed.

## Troubleshooting

### No backups found for tier 'GCI'

**Cause:** The backup directory doesn't exist yet (daemon hasn't created it) or the tier name is incorrect.

**Fix:**
- Check that the daemon is running: `cascade daemon status`
- Verify the tier name: `cascade resolve` shows active tiers
- Ensure the first cascade regeneration has completed (backups start after successful regen)

### Backup directory is growing too fast

**Cause:** `sync_interval_secs` is set too low or to `0` (no throttle).

**Fix:**
- Increase `sync_interval_secs` in `cascade.toml` to throttle backups. Example: `sync_interval_secs = 300` (only back up once every 5 minutes)
- Reload the daemon: `cascade daemon reload`

### Restore failed — disk is full

**Cause:** The backup snapshot is larger than available disk space.

**Fix:**
- Free up disk space before restoring
- Restore selectively (specific subdirectories only) rather than the full backup
- Consider storing backups on external media with more capacity

## Future Features

Planned for P3:
- `cascade backup verify` — check snapshot integrity and report missing files
- `cascade backup restore` — interactive restore from a snapshot
- Incremental backups (only changed files)
- Remote backup sync (cloud storage support)
