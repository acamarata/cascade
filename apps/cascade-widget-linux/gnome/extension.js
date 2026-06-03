const {GObject, St, Clutter, GLib, Gio} = imports.gi;
const ByteArray = imports.byteArray;
const Main = imports.ui.main;
const PanelMenu = imports.ui.panelMenu;
const Me = imports.misc.extensionUtils.getCurrentExtension();

// Module-level state
let _cachePath = '';
let _refreshTimer = null;
let _state = null;

// Module-level panel widget refs — nulled in disable()
let _indicator = null;
let _statusLabel = null;

/**
 * Purpose: Handle panel click — launch cascade status in terminal.
 * Inputs:  none (closure over module-level state)
 * Outputs: spawns terminal + cascade via GLib.spawn_async; returns Clutter.EVENT_STOP
 * Constraints: GLib.find_program_in_path used for both terminal and cascade detection.
 *              Terminal detection tries xterm, gnome-terminal, konsole, xfce4-terminal in order.
 *              cascade path: GLib.find_program_in_path('cascade') or fallback /usr/local/bin/cascade.
 * SPORT: MASTER-FEATURES.md: linux-widget-gnome-click-handler
 */
function _onPanelClick() {
  // 1. Find terminal: iterate ['xterm','gnome-terminal','konsole','xfce4-terminal'] in order.
  const terminals = ['xterm', 'gnome-terminal', 'konsole', 'xfce4-terminal'];
  let termPath = null;
  let termName = null;

  for (const term of terminals) {
    const path = GLib.find_program_in_path(term);
    if (path) {
      termPath = path;
      termName = term;
      break;
    }
  }

  if (!termPath) {
    log('[cascade-widget] _onPanelClick: no terminal found (xterm, gnome-terminal, konsole, xfce4-terminal all missing)');
    return Clutter.EVENT_STOP;
  }

  // 2. Find cascade: GLib.find_program_in_path('cascade') or fallback.
  const cascadePath = GLib.find_program_in_path('cascade') || '/usr/local/bin/cascade';

  // 3. Build argv based on terminal type.
  let argv;
  if (termName === 'xterm') {
    argv = [termPath, '-e', cascadePath, 'status'];
  } else if (termName === 'gnome-terminal') {
    argv = [termPath, '--', cascadePath, 'status'];
  } else if (termName === 'konsole') {
    argv = [termPath, '-e', cascadePath + ' status'];
  } else if (termName === 'xfce4-terminal') {
    argv = [termPath, '-e', cascadePath + ' status'];
  }

  // 4. Spawn the process using GLib.spawn_async (not GLib.spawn_command_line_async for security).
  try {
    GLib.spawn_async(
      null, // working directory (null = inherit)
      argv, // argv
      null, // envp (null = inherit parent env)
      GLib.SpawnFlags.SEARCH_PATH, // flags
      null, // child_setup callback (no setup needed)
      null  // user_data for callback
    );
  } catch (err) {
    log(`[cascade-widget] _onPanelClick: error spawning ${termName}: ${err}`);
  }

  // Return Clutter.EVENT_STOP to prevent event propagation to the panel.
  return Clutter.EVENT_STOP;
}

/**
 * Purpose: Read and parse a cascade cache.json file from disk.
 * Inputs:  filePath {string} — absolute path to the cache JSON file.
 * Outputs: {quota_pcts:{t0,t1,t2,t3}, inbox_unread, active_agents, cache_age_s}
 *          Returns all-zeros object on missing file or parse error so callers
 *          never need to guard against throws.
 * Constraints: Uses GLib.file_get_contents (GNOME Shell GJS environment only).
 *              cache_age_s is computed relative to Date.now() at call time.
 * SPORT: MASTER-FEATURES.md: linux-widget-gnome-cache-reader
 */
export function parseCache(filePath) {
  // Default returned when file is absent or JSON is malformed.
  const defaults = {
    quota_pcts: {t0: 0, t1: 0, t2: 0, t3: 0},
    inbox_unread: 0,
    active_agents: 0,
    cache_age_s: 0,
  };

  try {
    // GLib.file_get_contents returns [ok, bytes] where bytes is a GLib.Bytes.
    const [ok, bytes] = GLib.file_get_contents(filePath);
    if (!ok) {
      // File does not exist or could not be read; return defaults silently.
      return defaults;
    }

    // Convert the raw bytes to a UTF-8 string before JSON parsing.
    const text = ByteArray.toString(bytes);
    const d = JSON.parse(text);
    const q = d.quota;

    return {
      quota_pcts: {
        t0: q.t0.pct_used,
        t1: q.t1.pct_used,
        t2: q.t2.pct_used,
        t3: q.t3.pct_used,
      },
      inbox_unread: d.inbox.unread,
      active_agents: d.agents.active,
      // Age in whole seconds between the cache's updated_at timestamp and now.
      cache_age_s: Math.round((Date.now() - Date.parse(d.updated_at)) / 1000),
    };
  } catch (_err) {
    // Malformed JSON or unexpected schema; warn via log() and return defaults.
    log(`[cascade-widget] parseCache: error reading ${filePath}: ${_err}`);
    return defaults;
  }
}

/**
 * Purpose: Refresh the cached state by reading cache.json and updating the panel.
 * Inputs:  none (uses module-level _cachePath)
 * Outputs: updates _state; calls _updatePanel()
 * Constraints: Timer callback; must return GLib.SOURCE_CONTINUE to continue polling.
 */
function _refreshData() {
  _state = parseCache(_cachePath);
  _updatePanel();
}

/**
 * Purpose: Update the panel indicator label with current quota and inbox state.
 * Inputs:  uses module-level _state and _statusLabel
 * Outputs: sets _statusLabel.text (never creates new widgets — updates in place)
 * Constraints: _statusLabel must already exist (created in enable()).
 *              Text format: "T2:{t2}% 📬{unread}" fits under ~20 chars in panel.
 * SPORT: MASTER-FEATURES.md: linux-widget-gnome-panel-indicator
 */
function _updatePanel() {
  // Guard: indicator may not exist yet if called before enable() sets it up.
  if (_statusLabel === null) {
    return;
  }

  if (_state === null) {
    // No data available yet; show placeholder.
    _statusLabel.text = '--';
    return;
  }

  // Compact format that fits in the GNOME top bar: "T2:67% 📬3"
  _statusLabel.text = `T2:${_state.quota_pcts.t2}% \u{1F4EC}${_state.inbox_unread}`;
}

export function init() {
  // Initialization hook — no setup needed before enable().
}

/**
 * Purpose: Construct the panel indicator and start the refresh timer.
 * Inputs:  none
 * Outputs: Creates _indicator (PanelMenu.Button) with icon + label, registered
 *          in Main.panel right box. Starts 30 s polling timer.
 * Constraints: Must be paired with disable() to avoid memory leaks.
 */
export function enable() {
  // Set the cache path using GLib.get_home_dir() to avoid hardcoded /home paths.
  _cachePath = GLib.get_home_dir() + '/.cascade/cache.json';

  // ── Build the indicator ──────────────────────────────────────────────────
  // PanelMenu.Button(menuAlignment, nameText, dontCreateMenu)
  _indicator = new PanelMenu.Button(0.0, 'CascadeQuotaWidget', false);

  // Horizontal box that holds icon + label side by side.
  const hbox = new St.BoxLayout({style_class: 'panel-status-menu-box'});

  // SVG icon — file will be provided by T-P2-E05-05; path must be correct now.
  const iconPath = Me.dir.get_path() + '/icons/cascade-16.svg';
  const icon = new St.Icon({
    gicon: Gio.icon_new_for_string(iconPath),
    style_class: 'system-status-icon',
  });

  // Status label — created once here; _updatePanel() mutates .text, never recreates.
  _statusLabel = new St.Label({
    text: '--',
    y_align: Clutter.ActorAlign.CENTER,
  });

  hbox.add_child(icon);
  hbox.add_child(_statusLabel);
  _indicator.add_child(hbox);

  // Register in the right side of the top panel (position 1 = after system menu).
  Main.panel.addToStatusArea('cascade-quota-widget', _indicator, 1, 'right');

  // Connect button-press-event to _onPanelClick; return EVENT_STOP to prevent propagation.
  _indicator.connect('button-press-event', () => {
    _onPanelClick();
    return Clutter.EVENT_STOP;
  });

  // Initial read so the label populates immediately on enable.
  _refreshData();

  // 30-second repeating poll; GLib.SOURCE_CONTINUE keeps the timer alive.
  _refreshTimer = GLib.timeout_add_seconds(GLib.PRIORITY_DEFAULT, 30, () => {
    _refreshData();
    return GLib.SOURCE_CONTINUE;
  });
}

/**
 * Purpose: Tear down the panel indicator and stop the refresh timer.
 * Inputs:  none
 * Outputs: Removes indicator from panel, destroys all St widgets, nulls refs.
 * Constraints: Must null _indicator and _statusLabel to satisfy the GC
 *              and allow the extension to be re-enabled cleanly.
 */
export function disable() {
  // Stop the polling timer first to avoid any callbacks firing during teardown.
  if (_refreshTimer !== null) {
    GLib.Source.remove(_refreshTimer);
    _refreshTimer = null;
  }

  // Remove the indicator from the panel and destroy its widget tree.
  if (_indicator !== null) {
    _indicator.destroy();
    _indicator = null;
  }

  // Null the label ref (already destroyed above as a child of _indicator).
  _statusLabel = null;

  // Clear the cached state.
  _state = null;
}
