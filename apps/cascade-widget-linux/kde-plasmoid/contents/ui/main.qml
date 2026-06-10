/**
 * Cascade Fleet Dashboard — KDE Plasma 6 Applet
 *
 * Purpose: System tray / panel widget that polls the Cascade relay endpoint
 *          (default: http://localhost:3761/status) and renders per-tier
 *          utilization bars in a Plasma popup.
 *
 * Inputs:  plasmoid.configuration.endpoint (string)
 *          plasmoid.configuration.pollInterval (int, seconds)
 * Outputs: Panel icon with color-coded utilization; popup with tier rows.
 * Constraints: Plasma 6 / Qt 6 / QML only. No external JS dependencies.
 * SPORT: cascade-widget/kde — main QML applet
 */

import QtQuick 2.15
import QtQuick.Layouts 1.15
import QtQuick.Controls 2.15 as QQC2
import org.kde.plasma.plasmoid 2.0
import org.kde.plasma.components 3.0 as PlasmaComponents3
import org.kde.plasma.extras 2.0 as PlasmaExtras
import org.kde.kirigami 2.20 as Kirigami

PlasmoidItem {
    id: root

    // ------------------------------------------------------------------ state

    /** @type {Array<{tier: string, pct: number, resets_at: string|null}>} */
    property var tiers: []
    property string lastUpdated: ""
    property bool fetchError: false

    // Highest utilization across all tiers — drives icon/label color.
    property int maxPct: {
        let m = 0;
        for (const t of tiers) { if (t.pct > m) m = t.pct; }
        return m;
    }

    // ------------------------------------------------------------------ config

    readonly property string endpoint:
        plasmoid.configuration.endpoint || "http://localhost:3761/status"
    readonly property int pollIntervalMs:
        (plasmoid.configuration.pollInterval || 30) * 1000

    // ------------------------------------------------------------------ panel representation

    compactRepresentation: Item {
        PlasmaComponents3.Label {
            anchors.centerIn: parent
            text: root.fetchError ? "CASCADE ✗" : "CASCADE"
            color: root.maxPct >= 90
                ? Kirigami.Theme.negativeTextColor
                : root.maxPct >= 70
                    ? Kirigami.Theme.neutralTextColor
                    : Kirigami.Theme.textColor
            font.pointSize: 8
            font.bold: true
        }
    }

    // ------------------------------------------------------------------ full popup representation

    fullRepresentation: Item {
        implicitWidth: Kirigami.Units.gridUnit * 18
        implicitHeight: column.implicitHeight + Kirigami.Units.gridUnit

        ColumnLayout {
            id: column
            anchors { top: parent.top; left: parent.left; right: parent.right; margins: Kirigami.Units.smallSpacing }
            spacing: Kirigami.Units.smallSpacing

            PlasmaExtras.Heading {
                level: 4
                text: "Cascade Fleet Dashboard"
                Layout.fillWidth: true
            }

            Kirigami.Separator { Layout.fillWidth: true }

            // Tier rows
            Repeater {
                model: root.tiers

                delegate: RowLayout {
                    Layout.fillWidth: true
                    spacing: Kirigami.Units.smallSpacing

                    // Tier label (T0 / T1 / T2 / T3)
                    PlasmaComponents3.Label {
                        text: modelData.tier
                        font.bold: true
                        font.pointSize: 9
                        Layout.minimumWidth: Kirigami.Units.gridUnit * 2
                    }

                    // Progress bar
                    QQC2.ProgressBar {
                        value: modelData.pct / 100
                        Layout.fillWidth: true
                        // Color via palette — Plasma theming handles it.
                    }

                    // Percentage
                    PlasmaComponents3.Label {
                        text: modelData.pct + "%"
                        font.pointSize: 9
                        Layout.minimumWidth: Kirigami.Units.gridUnit * 2
                        horizontalAlignment: Text.AlignRight
                        color: modelData.pct >= 90
                            ? Kirigami.Theme.negativeTextColor
                            : modelData.pct >= 70
                                ? Kirigami.Theme.neutralTextColor
                                : Kirigami.Theme.textColor
                    }

                    // Reset countdown (shown when resets_at is set)
                    PlasmaComponents3.Label {
                        visible: !!modelData.resets_at
                        text: modelData.resets_at ? "↺ " + formatCountdown(modelData.resets_at) : ""
                        font.pointSize: 8
                        opacity: 0.6
                    }
                }
            }

            Kirigami.Separator { Layout.fillWidth: true }

            // Footer
            RowLayout {
                Layout.fillWidth: true

                PlasmaComponents3.Label {
                    text: root.fetchError
                        ? "Error — relay unreachable"
                        : "Updated: " + (root.lastUpdated || "—")
                    font.pointSize: 8
                    opacity: 0.6
                    Layout.fillWidth: true
                }

                PlasmaComponents3.ToolButton {
                    icon.name: "view-refresh"
                    onClicked: fetchStatus()
                }
            }
        }
    }

    // ------------------------------------------------------------------ helpers

    /**
     * Format an ISO timestamp as "Xm Ys" countdown from now.
     * @param {string} isoTs
     * @returns {string}
     */
    function formatCountdown(isoTs) {
        const diff = Math.max(0, Math.round((new Date(isoTs) - Date.now()) / 1000));
        const m = Math.floor(diff / 60);
        const s = diff % 60;
        return m > 0 ? `${m}m ${s}s` : `${s}s`;
    }

    /**
     * Fetch /status JSON from the Cascade relay and update tiers.
     * Falls back to fetchError=true on any network or parse error.
     */
    function fetchStatus() {
        const xhr = new XMLHttpRequest();
        xhr.open("GET", root.endpoint, true);
        xhr.timeout = 5000;
        xhr.onreadystatechange = function () {
            if (xhr.readyState !== XMLHttpRequest.DONE) return;
            if (xhr.status === 200) {
                try {
                    const data = JSON.parse(xhr.responseText);
                    root.tiers = data.tiers || [];
                    root.lastUpdated = data.last_updated
                        ? new Date(data.last_updated).toLocaleTimeString()
                        : "";
                    root.fetchError = false;
                } catch (_) {
                    root.fetchError = true;
                }
            } else {
                root.fetchError = true;
            }
        };
        xhr.send();
    }

    // ------------------------------------------------------------------ timers

    Timer {
        id: pollTimer
        interval: root.pollIntervalMs
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: root.fetchStatus()
    }

    // Re-start timer when interval config changes.
    onPollIntervalMsChanged: {
        pollTimer.restart();
    }

    // ------------------------------------------------------------------ plasmoid metadata

    Plasmoid.title: "Cascade Fleet Dashboard"
    Plasmoid.icon: "network-server"
    Plasmoid.toolTipMainText: "Cascade Fleet Dashboard"
    Plasmoid.toolTipSubText: root.fetchError
        ? "Relay unreachable"
        : "Max utilization: " + root.maxPct + "%"
}
