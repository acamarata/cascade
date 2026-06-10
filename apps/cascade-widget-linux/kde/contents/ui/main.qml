import QtQuick 2.15
import org.kde.plasma.plasmoid 2.0
import "./CacheModel.qml" as CacheModelComponent

PlasmoidItem {
  id: root

  Plasmoid.icon: "cascade-quota-widget"
  toolTipMainText: "Cascade Quota Widget"
  toolTipSubText: "T2: " + cacheModel.quotaT2 + "% | Inbox: " + cacheModel.inboxUnread

  // Compact and full representations
  // WHY Qt.createComponent and not the deprecated Plasmoid.fullRepresentation:
  // Plasmoid.fullRepresentation was the Plasma 5 API (property alias).
  // In Plasma 6 the correct API is the `fullRepresentation` property on PlasmoidItem.
  // Qt.createComponent resolves the QML file relative to this file's directory.
  compactRepresentation: CompactRepresentation { }
  fullRepresentation: Qt.createComponent("FullRepresentation.qml")

  // Prevent auto-expansion to full representation when panel is wide
  Plasmoid.switchWidth: 200
  Plasmoid.switchHeight: 150

  // Import the CacheModel singleton
  CacheModelComponent.CacheModel {
    id: cacheModel
  }

  // 30-second automatic refresh timer
  Timer {
    id: refreshTimer
    interval: 30000
    running: true
    repeat: true
    onTriggered: cacheModel.loadCache()
  }

  // Initial load on component creation
  Component.onCompleted: {
    cacheModel.loadCache()
  }
}
