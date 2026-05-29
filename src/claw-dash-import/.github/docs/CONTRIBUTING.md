# Contributing

Bug reports and pull requests are welcome.

## Dev install

Clone the repo and run the installer in dev mode so edits in the repo take effect immediately:

```bash
git clone https://github.com/acamarata/claw-dash.git
cd claw-dash
./install.sh --dev
```

`--dev` uses symlinks instead of copies. Changes to files in the repo directory are reflected live without reinstalling.

## Test the poller

Run the poller manually to verify it can read your `.claude/` directories and write the cache:

```bash
~/bin/claw-dash
cat ~/.claw-dash/cache.json | python3 -m json.tool | head -60
```

The output shows every project found and whether each section (tasks, memory, inbox) parsed successfully.

## Reload the widget

After editing `src/widget/claw-dash.widget/index.jsx`, click **Refresh All Widgets** in the Übersicht menu-bar icon, or wait for the 5s auto-refresh.

## Restart the server

After editing `src/bin/claw-dash-server` or any file under `src/web/`:

```bash
launchctl unload ~/Library/LaunchAgents/io.clawdash.server.plist
launchctl load   ~/Library/LaunchAgents/io.clawdash.server.plist
```

In dev mode the server script is a symlink, so the next request picks up any changes automatically. A restart is only needed if you changed the server startup logic.

## Lint

Shell scripts are checked with shellcheck. Run locally:

```bash
brew install shellcheck
shellcheck src/bin/claw-dash src/bin/claw-dash-server install.sh uninstall.sh
```

The same check runs on every push and PR via GitHub Actions (`.github/workflows/shellcheck.yml`).

## Issues

Open an issue with:

- macOS version
- Output of `~/bin/claw-dash` (with any personal paths redacted if preferred)
- Output of `launchctl list | grep clawdash`
- Contents of `~/.claw-dash/cache.json` (the `projects[].error` fields are most useful)
- Contents of `~/.claw-dash/port` (if the server issue)
- Relevant lines from `~/Library/Logs/claw-dash-server.log`
