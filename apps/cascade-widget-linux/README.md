# cascade-widget-linux

Linux desktop widgets for the Cascade fleet dashboard. Polls `localhost:3761/status` and displays per-tier AI utilization bars.

## GNOME Shell Extension

Requires GNOME Shell 45–47 and GJS with `gi://Gio` HTTP support.

**Install:**
```bash
UUID="cascade-widget@acamarata.github.io"
DEST="$HOME/.local/share/gnome-shell/extensions/$UUID"
mkdir -p "$DEST"
cp -r gnome-extension/* "$DEST/"
gnome-extensions enable "$UUID"
```

**GSettings schema** — compile before enabling:
```bash
glib-compile-schemas "$DEST/schemas/"
```

The schema file (`org.gnome.shell.extensions.cascade-widget.gschema.xml`) must declare keys `endpoint` (string) and `poll-interval` (int) under path `/org/gnome/shell/extensions/cascade-widget/`.

## KDE Plasma 6 Plasmoid

Requires Plasma 6 / Qt 6.

**Install:**
```bash
kpackagetool6 --install kde-plasmoid/
```

Or drag the `kde-plasmoid/` folder into System Settings → Widgets → Install from file.

**Configuration:** right-click the widget → Configure Cascade Fleet Dashboard.

## Relay API contract

Both widgets expect the relay at `GET /status` to return:

```json
{
  "tiers": [
    { "tier": "T0", "pct": 12, "resets_at": null },
    { "tier": "T1", "pct": 65, "resets_at": "2026-05-30T18:00:00Z" },
    { "tier": "T2", "pct": 80, "resets_at": null },
    { "tier": "T3", "pct": 4,  "resets_at": null }
  ],
  "last_updated": "2026-05-30T14:22:00Z"
}
```

Color thresholds: ≥90% = critical (red), ≥70% = warning (yellow), below = normal.
