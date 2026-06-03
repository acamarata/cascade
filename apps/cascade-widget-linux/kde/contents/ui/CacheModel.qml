/**
 * Purpose: QML singleton that reads ~/.cascade/cache.json and exposes quota/inbox/agent
 *          data as typed properties for Plasma widget bindings.
 * Inputs:  None (reads from the local filesystem via XHR file:// URL on loadCache() call)
 * Outputs: 7 int properties: quotaT0, quotaT1, quotaT2, quotaT3, inboxUnread,
 *          activeAgents, cacheAgeS
 * Constraints: file:// scheme is allowed in QML for local reads (no CORS restriction).
 *              XHR status === 0 (not 200) signals success for file:// requests.
 *              All properties remain 0 and a console.warn is logged on any error.
 * SPORT: MASTER-FEATURES.md: linux-widget-kde-cache-reader
 */
import QtQuick 2.15
import QtCore 6.2

QtObject {
    // ---------------------------------------------------------------------------
    // Public properties — declare as typed QML properties so bindings work
    // correctly when values are updated after an async load.
    // ---------------------------------------------------------------------------
    property int quotaT0:      0
    property int quotaT1:      0
    property int quotaT2:      0
    property int quotaT3:      0
    property int inboxUnread:  0
    property int activeAgents: 0
    property int cacheAgeS:    0

    // ---------------------------------------------------------------------------
    // loadCache() — read ~/.cascade/cache.json and populate properties.
    //
    // WHY file:// + XHR: Qt.openUrlExternally and Qt.resolvedUrl only resolve
    // relative/resource paths; for an absolute home-dir path we build the
    // file:// URL at runtime using StandardPaths.standardLocations() so the
    // code is not hardcoded to any specific username or home directory.
    // ---------------------------------------------------------------------------
    function loadCache() {
        var homeLocations = StandardPaths.standardLocations(StandardPaths.HomeLocation);
        if (!homeLocations || homeLocations.length === 0) {
            console.warn("CacheModel: unable to resolve HomeLocation via StandardPaths");
            return;
        }

        // StandardPaths returns file:// URLs on Qt 6; strip the scheme if present
        // so we can safely re-add it as a plain file:// URL below.
        var home = homeLocations[0].toString();
        if (home.indexOf("file://") === 0) {
            home = home.slice(7);
        }

        var cachePath = home + "/.cascade/cache.json";
        var fileUrl   = "file://" + cachePath;

        var xhr = new XMLHttpRequest();
        xhr.open("GET", fileUrl, /*async=*/true);

        xhr.onreadystatechange = function() {
            if (xhr.readyState !== 4) {
                return;
            }

            // For file:// requests Qt/V8 returns status 0 on success.
            if (xhr.status !== 0 && xhr.status !== 200) {
                console.warn("CacheModel: failed to read", cachePath,
                             "(status", xhr.status + ")");
                return;
            }

            try {
                var data = JSON.parse(xhr.responseText);

                // Quota tier usage — nested under data.quota.{t0..t3}.pct_used
                if (data.quota) {
                    quotaT0 = (data.quota.t0 && data.quota.t0.pct_used !== undefined)
                              ? data.quota.t0.pct_used : 0;
                    quotaT1 = (data.quota.t1 && data.quota.t1.pct_used !== undefined)
                              ? data.quota.t1.pct_used : 0;
                    quotaT2 = (data.quota.t2 && data.quota.t2.pct_used !== undefined)
                              ? data.quota.t2.pct_used : 0;
                    quotaT3 = (data.quota.t3 && data.quota.t3.pct_used !== undefined)
                              ? data.quota.t3.pct_used : 0;
                }

                // Inbox unread count
                if (data.inbox && data.inbox.unread !== undefined) {
                    inboxUnread = data.inbox.unread;
                }

                // Active agent count
                if (data.agents && data.agents.active !== undefined) {
                    activeAgents = data.agents.active;
                }

                // Cache age in seconds — derived from updated_at if present
                if (data.updated_at) {
                    var updatedMs = Date.parse(data.updated_at);
                    if (!isNaN(updatedMs)) {
                        cacheAgeS = Math.round((Date.now() - updatedMs) / 1000);
                    }
                }

            } catch (e) {
                console.warn("CacheModel: JSON parse error —", e.message);
                // Leave all properties at their current (0) defaults.
            }
        };

        xhr.send();
    }
}
