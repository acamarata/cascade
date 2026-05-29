# Contributing

Bug reports and pull requests are welcome.

## Dev install

Clone the repo and run the installer in dev mode so edits in the repo take effect immediately:

```bash
git clone https://github.com/acamarata/claw-fleet.git
cd claw-fleet
./install.sh --dev
```

`--dev` uses symlinks instead of copies. Editing a source file is reflected live.

## Reload the widget

After editing `src/widgets/ubersicht/claw-fleet.widget/index.jsx`, click **Refresh All Widgets** in the Übersicht menu-bar icon, or wait for the 5s auto-refresh.

## Test the poller

```bash
~/bin/claw-fleet
cat ~/.claude/usage-cache.json | python3 -m json.tool | head -40
```

## Lint

Shell scripts are checked with shellcheck. Run locally:

```bash
brew install shellcheck
shellcheck src/bin/claw-fleet src/bin/claw-fleet-refresh install.sh uninstall.sh
```

The same check runs on every push and PR via GitHub Actions (`.github/workflows/shellcheck.yml`).

## Issues

Open an issue with:
- macOS version
- Output of `~/bin/claw-fleet` (with any tokens redacted)
- Output of `launchctl list | grep clawfleet`
- Contents of `~/.claude/usage-cache.json` (redact email addresses if you prefer)
