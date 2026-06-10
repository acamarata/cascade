/**
 * Cascade Fleet Dashboard — KDE Plasma 6 Configuration Page
 *
 * Purpose: Settings page shown in the applet configuration dialog.
 *          Binds to plasmoid.configuration keys: endpoint, pollInterval.
 *
 * Constraints: Use Kirigami.FormLayout for consistent Plasma style.
 * SPORT: cascade-widget/kde — configuration page QML
 */

import QtQuick 2.15
import QtQuick.Controls 2.15 as QQC2
import org.kde.kirigami 2.20 as Kirigami

Kirigami.FormLayout {
    id: page

    property alias cfg_endpoint: endpointField.text
    property alias cfg_pollInterval: intervalSpin.value

    QQC2.TextField {
        id: endpointField
        Kirigami.FormData.label: "Relay endpoint URL:"
        placeholderText: "http://localhost:3761/status"
        Layout.fillWidth: true
    }

    QQC2.SpinBox {
        id: intervalSpin
        Kirigami.FormData.label: "Poll interval (seconds):"
        from: 5
        to: 300
        stepSize: 5
    }
}
