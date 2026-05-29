# Troubleshooting

## No data / cache is empty or missing

Run the poller manually to see what it finds and any errors:

```bash
~/bin/claw-dash
```

Then inspect the cache:

```bash
cat ~/.claw-dash/cache.json | python3 -m json.tool | head -60
```

Look at `projects[].error` fields. Common causes:

- No `.claude/` directory found in any detected path. Either create `~/.claw-dash/config.json` with explicit project paths, or verify that `~/Sites/` and `~/Downloads/` contain projects with `.claude/` subdirectories.
- The `.claude/` directory exists but `tasks/`, `memory/`, and `inbox/` are all empty. Claw Dash reads those subdirectories. If none of them have files, the cache entry for that project will be empty but not an error.
- Permissions: verify that `~/bin/claw-dash` is executable (`ls -l ~/bin/claw-dash`).

## Server not running / can't open the dashboard

Check whether the LaunchAgent is loaded:

```bash
launchctl list | grep clawdash
```

Both `io.clawdash.refresh` and `io.clawdash.server` should appear. If `io.clawdash.server` is missing:

```bash
launchctl load ~/Library/LaunchAgents/io.clawdash.server.plist
```

Check the server log for startup errors:

```bash
tail -40 ~/Library/Logs/claw-dash-server.log
tail -20 ~/Library/Logs/claw-dash-server.err
```

Verify the port file exists and contains a valid port number:

```bash
cat ~/.claw-dash/port
```

If the file is missing, the server has not started successfully yet. Check the log above.

## Port conflict

If port 3077 is in use by another process, the server selects the next available port and writes it to `~/.claw-dash/port`. The widget reads this file, so the "Open Dashboard" button still works.

To set a fixed port that avoids your conflict, add it to `~/.claw-dash/config.json`:

```json
{ "port": 4000 }
```

Then restart the server:

```bash
launchctl unload ~/Library/LaunchAgents/io.clawdash.server.plist
launchctl load   ~/Library/LaunchAgents/io.clawdash.server.plist
```

## Widget not appearing in Übersicht

1. Verify the widget directory exists:

   ```bash
   ls "$HOME/Library/Application Support/Übersicht/widgets/claw-dash.widget/"
   ```

2. In the Übersicht menu-bar icon, click **Refresh All Widgets**.

3. Check that Übersicht has screen recording permission: **System Settings > Privacy & Security > Screen Recording**.

4. If using `--dev` install, verify the symlink is correct:

   ```bash
   readlink "$HOME/Library/Application Support/Übersicht/widgets/claw-dash.widget"
   ```

   The path should point to `src/widget/claw-dash.widget/` inside your cloned repo.

## Projects not auto-detected

Auto-detection scans `~/Sites/*/` and `~/Downloads/` for `.claude/` subdirectories. If your projects are elsewhere, create `~/.claw-dash/config.json` with explicit paths:

```json
{
  "projects": [
    { "path": "~/my-projects/projecta", "label": "Project A", "enabled": true }
  ]
}
```

Once the file exists, auto-detection is disabled entirely. List every project you want to see.

## Widget shows stale data

The widget reads from the cache, which the poller updates every 60 seconds. If data looks old, check whether the refresh LaunchAgent is running:

```bash
launchctl list | grep clawdash
```

The `io.clawdash.refresh` entry should be present. If it is missing:

```bash
launchctl load ~/Library/LaunchAgents/io.clawdash.refresh.plist
```

To trigger an immediate refresh without waiting for the 60-second interval:

```bash
~/bin/claw-dash
```

Check the refresh log for errors:

```bash
tail -20 ~/Library/Logs/claw-dash-refresh.log
tail -20 ~/Library/Logs/claw-dash-refresh.err
```

## LaunchAgent logs

```bash
tail -f ~/Library/Logs/claw-dash-refresh.log
tail -f ~/Library/Logs/claw-dash-refresh.err
tail -f ~/Library/Logs/claw-dash-server.log
tail -f ~/Library/Logs/claw-dash-server.err
```
