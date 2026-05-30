/**
 * Cascade Fleet Dashboard — GNOME Shell Extension
 *
 * Purpose: Panel indicator that polls the Cascade relay (localhost:3761/status)
 *          and displays per-tier AI utilization bars in a popup menu.
 *
 * Constraints: Must run inside the GNOME Shell process (GJS). No Node.js APIs.
 *              Uses GLib.timeout_add_seconds for polling; cancels on disable().
 *
 * SPORT: cascade/apps/cascade-widget-linux — GNOME extension entry point
 */

import GLib from 'gi://GLib';
import Gio from 'gi://Gio';
import St from 'gi://St';
import Clutter from 'gi://Clutter';
import * as Main from 'resource:///org/gnome/shell/ui/main.js';
import * as PanelMenu from 'resource:///org/gnome/shell/ui/panelMenu.js';
import * as PopupMenu from 'resource:///org/gnome/shell/ui/popupMenu.js';
import { Extension } from 'resource:///org/gnome/shell/extensions/extension.js';

/** Cascade relay status endpoint. Configurable via prefs. */
const DEFAULT_ENDPOINT = 'http://localhost:3761/status';
/** Poll interval in seconds. */
const POLL_INTERVAL_S = 30;

/** @typedef {{ tier: string, pct: number, resets_at: string | null }} TierStatus */
/** @typedef {{ tiers: TierStatus[], last_updated: string }} CascadeStatus */

class CascadeIndicator extends PanelMenu.Button {
  /**
   * Purpose: Panel button + popup that shows live tier utilization.
   * Inputs: extension — the parent Extension instance for settings access.
   * Outputs: GNOME panel widget; side-effect: periodic HTTP polling.
   * Constraints: Must call destroy() to clear the poll timer.
   * SPORT: cascade-widget/gnome — CascadeIndicator class
   */
  constructor(extension) {
    super(0.0, 'Cascade Fleet Dashboard', false);

    this._extension = extension;
    this._settings = extension.getSettings();
    this._pollSource = null;
    this._tiers = [];

    // Panel label
    this._label = new St.Label({
      text: 'CASCADE',
      y_align: Clutter.ActorAlign.CENTER,
      style_class: 'cascade-panel-label',
    });
    this.add_child(this._label);

    // Build popup
    this._buildMenu();

    // Start polling
    this._startPolling();
  }

  /** Build the initial popup menu skeleton. */
  _buildMenu() {
    const titleItem = new PopupMenu.PopupMenuItem('Cascade Fleet Dashboard', {
      reactive: false,
      style_class: 'cascade-popup-title',
    });
    this.menu.addMenuItem(titleItem);
    this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());

    this._tierSection = new PopupMenu.PopupMenuSection();
    this.menu.addMenuItem(this._tierSection);

    this.menu.addMenuItem(new PopupMenu.PopupSeparatorMenuItem());

    this._footerItem = new PopupMenu.PopupMenuItem('Last updated: —', {
      reactive: false,
      style_class: 'cascade-footer',
    });
    this.menu.addMenuItem(this._footerItem);

    const refreshItem = new PopupMenu.PopupMenuItem('Refresh now');
    refreshItem.connect('activate', () => this._fetchStatus());
    this.menu.addMenuItem(refreshItem);
  }

  /** Start the periodic poll timer. */
  _startPolling() {
    this._fetchStatus();
    this._pollSource = GLib.timeout_add_seconds(
      GLib.PRIORITY_DEFAULT,
      POLL_INTERVAL_S,
      () => {
        this._fetchStatus();
        return GLib.SOURCE_CONTINUE;
      },
    );
  }

  /** Fetch status JSON from the Cascade relay. */
  _fetchStatus() {
    const endpoint =
      this._settings?.get_string('endpoint') ?? DEFAULT_ENDPOINT;

    const msg = Gio.Message.new('GET', endpoint);
    const session = new Gio.Session();
    session.send_async(msg, null, (_sess, res) => {
      try {
        const reply = session.send_finish(res);
        const bytes = reply.read_bytes(65536, null);
        const json = new TextDecoder().decode(bytes.get_data());
        /** @type {CascadeStatus} */
        const status = JSON.parse(json);
        this._applyStatus(status);
      } catch (e) {
        this._label.text = 'CASCADE ✗';
        this._label.style_class = 'cascade-panel-label critical';
      }
    });
  }

  /**
   * Apply parsed status to the UI.
   * @param {CascadeStatus} status
   */
  _applyStatus(status) {
    this._tierSection.removeAll();

    let maxPct = 0;
    for (const tier of status.tiers ?? []) {
      maxPct = Math.max(maxPct, tier.pct);
      const row = new PopupMenu.PopupBaseMenuItem({ reactive: false });
      const box = new St.BoxLayout({
        style_class: 'cascade-tier-row',
        x_expand: true,
      });

      const lbl = new St.Label({
        text: tier.tier,
        style_class: 'cascade-tier-label',
      });

      const barBg = new St.Widget({
        style_class: 'cascade-bar-bg',
        x_expand: true,
      });
      const fillClass =
        tier.pct >= 90
          ? 'cascade-bar-fill critical'
          : tier.pct >= 70
            ? 'cascade-bar-fill warning'
            : 'cascade-bar-fill';
      const barFill = new St.Widget({
        style_class: fillClass,
        width: Math.round((tier.pct / 100) * 160),
      });
      barBg.add_child(barFill);

      const pctLbl = new St.Label({
        text: `${tier.pct}%`,
        style_class: 'cascade-pct-label',
      });

      box.add_child(lbl);
      box.add_child(barBg);
      box.add_child(pctLbl);
      row.add_child(box);
      this._tierSection.addMenuItem(row);
    }

    // Update panel label color
    if (maxPct >= 90) {
      this._label.style_class = 'cascade-panel-label critical';
    } else if (maxPct >= 70) {
      this._label.style_class = 'cascade-panel-label warning';
    } else {
      this._label.style_class = 'cascade-panel-label';
    }

    const ts = status.last_updated
      ? new Date(status.last_updated).toLocaleTimeString()
      : '—';
    this._footerItem.label.text = `Last updated: ${ts}`;
  }

  /** Clean up on extension disable. */
  destroy() {
    if (this._pollSource) {
      GLib.source_remove(this._pollSource);
      this._pollSource = null;
    }
    super.destroy();
  }
}

export default class CascadeExtension extends Extension {
  /**
   * Purpose: GNOME Shell extension lifecycle — enable/disable hooks.
   * SPORT: cascade-widget/gnome — extension lifecycle
   */

  enable() {
    this._indicator = new CascadeIndicator(this);
    Main.panel.addToStatusArea(this.uuid, this._indicator);
  }

  disable() {
    this._indicator?.destroy();
    this._indicator = null;
  }
}
