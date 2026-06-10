/**
 * Cascade Fleet Dashboard — GNOME Extension Preferences
 *
 * Purpose: GTK4 preferences window for configuring the relay endpoint URL
 *          and poll interval displayed in GNOME Extensions app.
 *
 * Inputs: GSettings schema org.gnome.shell.extensions.cascade-widget
 * Outputs: Adw.PreferencesWindow with two fields (endpoint, interval)
 * Constraints: GTK4 + libadwaita only; no GJS-private APIs.
 * SPORT: cascade-widget/gnome — preferences UI
 */

import Adw from 'gi://Adw';
import Gtk from 'gi://Gtk';
import { ExtensionPreferences } from 'resource:///org/gnome/Shell/Extensions/js/extensions/prefs.js';

export default class CascadePreferences extends ExtensionPreferences {
  /**
   * Build and return the preferences widget.
   * @returns {Adw.PreferencesPage}
   */
  getPreferencesWidget() {
    const settings = this.getSettings();

    const page = new Adw.PreferencesPage({
      title: 'Cascade Fleet Dashboard',
      icon_name: 'network-server-symbolic',
    });

    // --- Connection group ---
    const connGroup = new Adw.PreferencesGroup({ title: 'Connection' });
    page.add(connGroup);

    const endpointRow = new Adw.EntryRow({
      title: 'Relay endpoint URL',
    });
    endpointRow.text = settings.get_string('endpoint');
    endpointRow.connect('changed', () => {
      settings.set_string('endpoint', endpointRow.text);
    });
    connGroup.add(endpointRow);

    // --- Display group ---
    const displayGroup = new Adw.PreferencesGroup({ title: 'Display' });
    page.add(displayGroup);

    const intervalRow = new Adw.SpinRow({
      title: 'Poll interval (seconds)',
      adjustment: new Gtk.Adjustment({
        lower: 5,
        upper: 300,
        step_increment: 5,
        value: settings.get_int('poll-interval'),
      }),
    });
    intervalRow.connect('changed', () => {
      settings.set_int('poll-interval', intervalRow.value);
    });
    displayGroup.add(intervalRow);

    return page;
  }
}
