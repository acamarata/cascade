/**
 * Purpose: Smoke test for CacheModel — loads the fixture and verifies quotaT2 === 67.
 * Usage:   qml test-cache-model.qml
 *          Exits 0 on pass, 1 on failure.
 * Constraints: Fixture path is hardcoded relative to this file (../../fixtures/cache-v1.json)
 *              so the test is self-contained and runnable from the ui/ directory or CI.
 * SPORT: MASTER-FEATURES.md: linux-widget-kde-cache-reader
 */
import QtQuick 2.15
import QtCore 6.2

Item {
    id: root

    // ---------------------------------------------------------------------------
    // Inline CacheModel — same logic as CacheModel.qml but with a fixture URL
    // so the test does not depend on ~/.cascade/cache.json being present.
    // WHY inline: QML does not support constructor injection; overriding the URL
    // requires a subclass. Inline keeps the test self-contained.
    // ---------------------------------------------------------------------------
    QtObject {
        id: model

        property int quotaT0:      0
        property int quotaT1:      0
        property int quotaT2:      0
        property int quotaT3:      0
        property int inboxUnread:  0
        property int activeAgents: 0
        property int cacheAgeS:    0

        function loadFromFixture(fixtureUrl) {
            var xhr = new XMLHttpRequest();
            xhr.open("GET", fixtureUrl, /*async=*/true);

            xhr.onreadystatechange = function() {
                if (xhr.readyState !== 4) {
                    return;
                }
                if (xhr.status !== 0 && xhr.status !== 200) {
                    console.warn("test-cache-model: failed to read fixture (status",
                                 xhr.status + ")");
                    Qt.exit(1);
                    return;
                }

                try {
                    var data = JSON.parse(xhr.responseText);

                    if (data.quota) {
                        model.quotaT0 = (data.quota.t0 && data.quota.t0.pct_used !== undefined) ? data.quota.t0.pct_used : 0;
                        model.quotaT1 = (data.quota.t1 && data.quota.t1.pct_used !== undefined) ? data.quota.t1.pct_used : 0;
                        model.quotaT2 = (data.quota.t2 && data.quota.t2.pct_used !== undefined) ? data.quota.t2.pct_used : 0;
                        model.quotaT3 = (data.quota.t3 && data.quota.t3.pct_used !== undefined) ? data.quota.t3.pct_used : 0;
                    }
                    if (data.inbox && data.inbox.unread !== undefined) {
                        model.inboxUnread = data.inbox.unread;
                    }
                    if (data.agents && data.agents.active !== undefined) {
                        model.activeAgents = data.agents.active;
                    }

                    // Validate expected values — use Qt.callLater to allow property
                    // bindings to propagate before we read them back.
                    Qt.callLater(function() {
                        var pass = true;

                        if (model.quotaT2 !== 67) {
                            console.error("FAIL: quotaT2 expected 67, got", model.quotaT2);
                            pass = false;
                        }
                        if (model.quotaT0 !== 42) {
                            console.error("FAIL: quotaT0 expected 42, got", model.quotaT0);
                            pass = false;
                        }
                        if (model.quotaT1 !== 15) {
                            console.error("FAIL: quotaT1 expected 15, got", model.quotaT1);
                            pass = false;
                        }
                        if (model.quotaT3 !== 8) {
                            console.error("FAIL: quotaT3 expected 8, got", model.quotaT3);
                            pass = false;
                        }
                        if (model.inboxUnread !== 3) {
                            console.error("FAIL: inboxUnread expected 3, got", model.inboxUnread);
                            pass = false;
                        }
                        if (model.activeAgents !== 2) {
                            console.error("FAIL: activeAgents expected 2, got", model.activeAgents);
                            pass = false;
                        }

                        if (pass) {
                            console.log("PASS: all CacheModel assertions satisfied");
                            Qt.exit(0);
                        } else {
                            Qt.exit(1);
                        }
                    });

                } catch (e) {
                    console.error("test-cache-model: JSON parse error —", e.message);
                    Qt.exit(1);
                }
            };

            xhr.send();
        }
    }

    // ---------------------------------------------------------------------------
    // Entry point — resolve fixture path relative to this QML file, then load.
    // ../../fixtures/cache-v1.json resolves from kde/contents/ui/ to
    // cascade-widget-linux/fixtures/cache-v1.json
    // ---------------------------------------------------------------------------
    Component.onCompleted: {
        var fixtureUrl = Qt.resolvedUrl("../../fixtures/cache-v1.json");
        console.log("test-cache-model: loading fixture from", fixtureUrl);
        model.loadFromFixture(fixtureUrl);
    }
}
