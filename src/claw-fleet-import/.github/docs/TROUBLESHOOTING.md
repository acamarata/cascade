# Troubleshooting

## All accounts show "—" or no data

The cache is empty or missing. Run a manual refresh:

```bash
~/bin/claw-fleet-refresh
```

If that errors, run the poller directly to see stderr output:

```bash
~/bin/claw-fleet
```

Common causes: no Claude Code credentials in keychain yet (you haven't opened Claude Code in any `~/.claude-acc*` dir), or all tokens are expired.

## "keychain_failed" in cache

The keychain service entry for a config dir does not exist or Terminal does not have permission to read it.

Steps:
1. Open Claude Code with `CLAUDE_CONFIG_DIR=~/.claude-acc1` at least once. This populates the keychain entry.
2. If you see a permission dialog, allow Keychain access for Terminal (or your shell).
3. On macOS Ventura+, some setups require Full Disk Access for the terminal. Check **System Settings > Privacy & Security > Full Disk Access** and add Terminal.app (or iTerm2).

To verify the credential exists:
```bash
suffix=$(python3 -c "import hashlib; print(hashlib.sha256(b'$HOME/.claude-acc1').hexdigest()[:8])")
security find-generic-password -s "Claude Code-credentials-$suffix" -w
```

## "refresh_failed" in cache

Token refresh ran `claude -p "ok" --max-turns 1` but it failed. Common causes:

- `claude` binary not in PATH for the LaunchAgent. Check that `~/bin` is in the `PATH` env var in the rendered plist at `~/Library/LaunchAgents/io.clawfleet.refresh.plist`.
- The account is on a different machine or the config dir is missing its internal state files.

## Widget not appearing in Übersicht

1. Verify the widget is in the right directory:
   ```bash
   ls "$HOME/Library/Application Support/Übersicht/widgets/claw-fleet.widget/"
   ```
2. In the Übersicht menu-bar icon, click **Refresh All Widgets**.
3. Check that Übersicht has screen recording permission (**System Settings > Privacy > Screen Recording**).
4. If using `--dev` install, verify the symlink points to the correct path:
   ```bash
   readlink "$HOME/Library/Application Support/Übersicht/widgets/claw-fleet.widget"
   ```

## Widget position resets on every reload

The position file may not be writeable. Verify:

```bash
ls -la ~/.config/claw-fleet/widget-position.json
```

If missing, the widget writes it on the next drag-end. Until then it starts at the default position (top: 120, left: 40).

## SwiftBar not picking up the plugin

1. Verify SwiftBar's plugin directory:
   - Open SwiftBar > Preferences > Plugin Folder. Confirm it matches `~/Library/Application Support/SwiftBar/Plugins/`.
2. Verify the file exists and is executable:
   ```bash
   ls -l "$HOME/Library/Application Support/SwiftBar/Plugins/claw-fleet.5s.sh"
   ```
3. Check SwiftBar console for errors: SwiftBar menu > **Open Console**.
4. Ensure `python3` is available at `/usr/bin/env python3`.

## Data is stuck stale (asterisk marker)

The cache is more than 10 minutes old. This usually means:

- The LaunchAgent is not running. Check: `launchctl list | grep clawfleet`
- The poller is hitting API rate limits. The cache preserves last-known data; the asterisk (`*`) tells you when it's stale. Data updates again once the rate limit lifts.
- Token expired and refresh is failing (see "refresh_failed" above).

To restart the LaunchAgent:
```bash
launchctl unload ~/Library/LaunchAgents/io.clawfleet.refresh.plist
launchctl load   ~/Library/LaunchAgents/io.clawfleet.refresh.plist
```

## LaunchAgent logs

```bash
tail -f ~/Library/Logs/claw-fleet-refresh.log
tail -f ~/Library/Logs/claw-fleet-refresh.err
```

---

## G1 stuck in "no_auth" or "auth_expired"

The Gemini probe could not authenticate. Two possible causes:

**API key missing or wrong**: verify `GEMINI_API_KEY_OPENCLAW` is set in `~/.claude/vault.env` and that the key is valid on [aistudio.google.com](https://aistudio.google.com). A key for the wrong project will authenticate but return empty metrics.

**ADC not configured (Mode B)**: if you want Cloud Monitoring data instead of API-key probing, run:

```bash
gcloud auth application-default login
```

After that completes, restart the Gemini LaunchAgent:

```bash
launchctl unload ~/Library/LaunchAgents/io.clawfleet.gemini.plist
launchctl load   ~/Library/LaunchAgents/io.clawfleet.gemini.plist
```

Mode B activates automatically when ADC credentials are present and will surface richer quota metrics from `serviceruntime.googleapis.com`.

---

## O1 shows "auth_expired"

The OpenCode Go session cookie has expired. Cookies typically last 30 days. To refresh:

1. Open Chrome and navigate to `opencode.ai`.
2. Open DevTools → Application → Cookies → `opencode.ai`.
3. Copy the current session cookie value.
4. Update `OPENCODE_GO_AUTH_COOKIE` in `~/.claude/vault.env`.
5. Restart the OpenCode LaunchAgent:

```bash
launchctl unload ~/Library/LaunchAgents/io.clawfleet.opencode.plist
launchctl load   ~/Library/LaunchAgents/io.clawfleet.opencode.plist
```

Note: the cookie scrape is a temporary workaround. [opencode PR #16513](https://github.com/anomalyco/opencode/pull/16513) will replace it with a proper usage API when it ships.

---

## All four windows show 100% for an account

That is a real signal, not a display bug. The provider has confirmed to the probe that the account has hit its cap. Wait for the window to reset — `resets_in` in the cache shows the countdown.

If you want to verify: run the probe directly and check the raw response against what the provider's own dashboard shows.

```bash
~/bin/claw-fleet          # for Claude accounts
~/bin/claw-fleet-codex    # for C1
~/bin/claw-fleet-gemini   # for G1
~/bin/claw-fleet-opencode # for O1
```

---

## sanity ok=false in the cache

The hourly sanity check found an anomaly. Check the `anomalies` array:

```bash
python3 -c "
import json, pathlib
c = json.loads(pathlib.Path('$HOME/.claude/usage-cache.json').read_text())
s = c.get('sanity', {})
print('ok:', s.get('ok'))
for a in s.get('anomalies', []):
    print(' -', a)
"
```

Common anomalies:

- **`resets_at in past`**: the provider's reset timestamp has already passed but utilization is not near zero. This usually means the provider changed its window definition. Check the provider's status page and update the expected window length in the probe config.
- **`utilization out of range`**: a probe returned a value outside 0–100. Likely a response format change on the provider side. Run the probe manually and inspect stderr.

Run `~/bin/claw-fleet-sanity` manually at any time to get a fresh sanity report.
