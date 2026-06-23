# Cascade Fleet — Übersicht desktop widget

A live desktop widget showing every linked account and its quota, read from
`~/.cascade/accounts.json` and `~/.cascade/quota-store.json` (maintained by the
cascade daemon's fleet poller). This is the Cascade-native replacement for the
old Claw-Fleet desktop display.

## Install

1. Install Übersicht: `brew install --cask ubersicht`
2. Copy the widget: `cp -R cascade-fleet.widget ~/Library/Application\ Support/Übersicht/widgets/`
3. Launch Übersicht (grant Screen Recording permission on first run so it can draw on the desktop).

The widget refreshes every 30s and lists each account's family, access method,
CLI availability, GFP key count, and the reserved primary (T0) account.
