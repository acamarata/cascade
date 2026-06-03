/**
 * Purpose: KDE Plasma full-representation popup for the Cascade Quota Widget.
 *          Shows a 2-column grid with 6 data rows (4 quota tiers + inbox + active agents)
 *          and a terminal-launch button via Plasma5Support.Process.
 * Inputs:  cacheModel — the CacheModel instance declared in the parent PlasmoidItem
 *          (accessible via parent scope because FullRepresentation is a child of PlasmoidItem).
 * Outputs: Visual popup; side-effect: starts a bash terminal on button click.
 * Constraints: Uses Kirigami 2.20 for Plasma 5.27 compatibility (not Kirigami 4.x which
 *              requires Plasma 6). PlasmaComponents3.Button for Plasma visual style.
 *              Terminal launch is EXCLUSIVELY via Plasma5Support.Process — no exec(),
 *              no Qt.openUrlExternally (unreliable for terminal emulators on most distros),
 *              no PlasmaCore.Process (deprecated in Plasma 6).
 * SPORT: MASTER-FEATURES.md: F-WIDGET-LINUX-KDE — linux-widget-kde-popup
 */
import QtQuick 2.15
import QtQuick.Layouts 1.15
import org.kde.kirigami 2.20 as Kirigami
import org.kde.plasma.components 3.0 as PlasmaComponents3
import org.kde.plasma.plasma5support 0.1 as Plasma5Support

Kirigami.Page {
    title: "Cascade Status"

    // WHY explicit sizing: the Plasmoid fullRepresentation popup has no intrinsic
    // size; without a preferred width/height the popup collapses to zero on many
    // Plasma versions. 300×220 is comfortably readable at any DPI without scrolling.
    implicitWidth: Kirigami.Units.gridUnit * 18
    implicitHeight: Kirigami.Units.gridUnit * 14

    ColumnLayout {
        anchors.fill: parent
        anchors.margins: Kirigami.Units.smallSpacing
        spacing: Kirigami.Units.smallSpacing

        // --- 6-row data grid (4 quota tiers + inbox unread + active agents) ---
        GridLayout {
            columns: 2
            Layout.fillWidth: true
            columnSpacing: Kirigami.Units.smallSpacing
            rowSpacing: Kirigami.Units.smallSpacing

            // Row 1 — T0 Observer quota
            PlasmaComponents3.Label {
                text: "T0 (Observer)"
                Layout.fillWidth: true
            }
            PlasmaComponents3.Label {
                // WHY "%" appended here: cacheModel.quotaT0 is an int property;
                // string concatenation is the idiomatic QML approach for display.
                text: cacheModel.quotaT0 + "%"
                horizontalAlignment: Text.AlignRight
            }

            // Row 2 — T1 Planner quota
            PlasmaComponents3.Label {
                text: "T1 (Planner)"
                Layout.fillWidth: true
            }
            PlasmaComponents3.Label {
                text: cacheModel.quotaT1 + "%"
                horizontalAlignment: Text.AlignRight
            }

            // Row 3 — T2 Executor quota
            PlasmaComponents3.Label {
                text: "T2 (Executor)"
                Layout.fillWidth: true
            }
            PlasmaComponents3.Label {
                text: cacheModel.quotaT2 + "%"
                horizontalAlignment: Text.AlignRight
            }

            // Row 4 — T3 Triage quota
            PlasmaComponents3.Label {
                text: "T3 (Triage)"
                Layout.fillWidth: true
            }
            PlasmaComponents3.Label {
                text: cacheModel.quotaT3 + "%"
                horizontalAlignment: Text.AlignRight
            }

            // Row 5 — Inbox unread count
            PlasmaComponents3.Label {
                text: "Inbox unread"
                Layout.fillWidth: true
            }
            PlasmaComponents3.Label {
                text: cacheModel.inboxUnread
                horizontalAlignment: Text.AlignRight
            }

            // Row 6 — Active agent count
            PlasmaComponents3.Label {
                text: "Active agents"
                Layout.fillWidth: true
            }
            PlasmaComponents3.Label {
                text: cacheModel.activeAgents
                horizontalAlignment: Text.AlignRight
            }
        }

        // Spacer to push the button toward the bottom
        Item { Layout.fillHeight: true }

        // --- Terminal launch button ---
        // WHY Plasma5Support.Process instead of Qt.openUrlExternally or exec():
        // Qt.openUrlExternally uses xdg-open which does not reliably map to a
        // terminal emulator on every distro. exec() is not available in QML.
        // PlasmaCore.Process is deprecated in Plasma 6. Plasma5Support.Process
        // is the supported Plasma 5.27+ / Plasma 6 API for subprocess launch.
        Plasma5Support.Process {
            id: termProc
        }

        PlasmaComponents3.Button {
            Layout.fillWidth: true
            text: "Open Terminal"
            icon.name: "utilities-terminal"
            onClicked: {
                // WHY "cascade status; read": the `read` command keeps the terminal
                // window open so the user can inspect the output before it closes.
                termProc.start("bash", ["-c", "cascade status; read"])
            }
        }
    }
}
