# Account Auto-Detection

Claw Fleet discovers accounts without any configuration. Here is how it works.

## Default Discovery

On each run, `claw-fleet` globs `~/.claude-acc*` (sorted alphabetically), excluding paths matching `*.bak*` or `*.bak.*`. The result is a list like:

```
~/.claude-acc1
~/.claude-acc2
~/.claude-acc3
~/.claude-acc4
```

The primary config dir (`~/.claude`) is **not** included by default when `~/.claude-acc*` dirs exist. This is intentional: most users who have set up multiple accounts treat `~/.claude` as the same account as `acc1`. To include it, pass `--include-primary` or set `"include_primary": true` in the config file.

If no `~/.claude-acc*` dirs exist, `~/.claude` is included automatically.

## Keychain Service Name

For each detected config dir, the keychain service name follows Claude Code's own convention:

```
Claude Code-credentials-<sha256(absolute_path)[:8]>
```

For example, `~/.claude-acc1` resolves to `/Users/you/.claude-acc1`, and the keychain service would be something like `Claude Code-credentials-a1b2c3d4`.

This is a Claude Code internal detail. It means credentials appear in Keychain Access under the same entries Claude Code itself creates — no extra keychain items.

## Email Extraction

There is no explicit email field inside the keychain JSON payload (`{"claudeAiOauth": {...}}`). Claw Fleet extracts the email by:

1. Decoding the base64 JWT access token payload.
2. Looking for an `email` claim or a `sub` claim that contains `@`.
3. Falling back to the config dir basename (`claude-acc1`, `claude-acc2`, etc.) if no email is found.

If you want a custom label, use the config file (see below).

## Config File Override

Create `~/.config/claw-fleet/config.json` to switch from auto-detection to an explicit list:

```json
{
  "accounts": [
    { "dir": "~/.claude-acc1", "label": "Main",    "enabled": true  },
    { "dir": "~/.claude-acc2", "label": "Work",    "enabled": true  },
    { "dir": "~/.claude-acc3", "label": "Archive", "enabled": false, "do_not_query": true }
  ],
  "include_primary": false,
  "poll_interval_seconds": 300
}
```

When this file exists, `claw-fleet` uses only the listed accounts and ignores the glob. Setting `"enabled": false` or `"do_not_query": true` emits a stub entry in the cache so consumers still see the account, but no API call is made.

## Opting Out Individual Dirs

In auto-detection mode (no config file), you cannot opt out individual dirs other than by removing them or adding `.bak` to the directory name (e.g. `~/.claude-acc3.bak`). The recommended approach for more control is to create the config file with a curated list.

## The `poll_interval_seconds` Field

This field is read by the config file but is informational for the LaunchAgent. The LaunchAgent cadence is set in the rendered plist at install time (`StartInterval` in seconds). To change the cadence after install, edit the plist and reload:

```bash
plutil -replace StartInterval -integer 180 \
  ~/Library/LaunchAgents/io.clawfleet.refresh.plist
launchctl unload ~/Library/LaunchAgents/io.clawfleet.refresh.plist
launchctl load   ~/Library/LaunchAgents/io.clawfleet.refresh.plist
```
