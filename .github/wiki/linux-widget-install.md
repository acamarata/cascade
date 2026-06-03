# Linux Widget Installation

## CI Status

| Widget | CI Job | Status |
|---|---|---|
| GNOME Shell Extension | `gnome-extension` | ![CI Linux Widgets](https://github.com/acamarata/cascade/actions/workflows/ci-linux-widgets.yml/badge.svg) |
| KDE Plasmoid | `kde-plasmoid` | ![CI Linux Widgets](https://github.com/acamarata/cascade/actions/workflows/ci-linux-widgets.yml/badge.svg) |

## GNOME Shell Extension

### Requirements

- GNOME 45 or 46
- GJS (ships with GNOME; verify: `gjs --version`)
- Cascade daemon running (`cascade status` should show OK)

### Install from ZIP

```bash
gnome-extensions install cascade-quota-widget@acamarata.zip
gnome-extensions enable cascade-quota-widget@acamarata
```

Note: restart GNOME Shell with Alt+F2, then type `r`, then press Enter on X11. On Wayland, log out and log back in.

### Install from source

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade
bash apps/cascade-widget-linux/gnome/pack.sh
gnome-extensions install apps/cascade-widget-linux/gnome/cascade-quota-widget@acamarata.zip
gnome-extensions enable cascade-quota-widget@acamarata
```

### Update

Repeat install steps. The `gnome-extensions install` command overwrites the existing extension.

### Uninstall

```bash
gnome-extensions disable cascade-quota-widget@acamarata
gnome-extensions uninstall cascade-quota-widget@acamarata
```

### Troubleshooting

- Extension not visible: ensure GNOME Shell version is 45 or 46. Run `gnome-shell --version` to check.
- Icon missing: confirm Cascade is installed with `which cascade`. Check that `~/.cascade/cache.json` exists.
- No status data: run `cascade status` in a terminal to verify the daemon is running.

## KDE Plasmoid

### Requirements

- KDE Plasma 5.27 or 6.0+
- Qt 5.15 / Qt 6.x
- Cascade daemon running (`cascade status` should show OK)

### Install

```bash
plasmapkg2 --install cascade-quota-widget.plasmoid
```

After install, right-click the KDE panel and choose Add Widgets; search "Cascade".

### Install from source

```bash
git clone https://github.com/acamarata/cascade.git
cd cascade
bash apps/cascade-widget-linux/kde/pack.sh
plasmapkg2 --install apps/cascade-widget-linux/kde/cascade-quota-widget.plasmoid
```

### Update

```bash
plasmapkg2 --upgrade cascade-quota-widget.plasmoid
```

### Uninstall

```bash
plasmapkg2 --remove org.acamarata.cascade.quotawidget
```

### Troubleshooting

- Widget not visible in Add Widgets: run `plasmapkg2 --list` to confirm it installed.
- No status data: run `cascade status` in a terminal to verify the daemon is running.
- Blank icon: confirm `contents/icons/` is in the package (`plasmapkg2 --show-structure`).
