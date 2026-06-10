import QtQuick 2.15
import org.kde.kirigami 2.20 as Kirigami

Item {
  id: compactRoot
  width: 64
  height: 22

  Row {
    spacing: 4
    anchors.fill: parent
    anchors.leftMargin: 2
    anchors.rightMargin: 2

    Image {
      source: Qt.resolvedUrl("../icons/cascade-quota-widget-22.svg")
      width: 22
      height: 22
    }

    Text {
      text: "T2:" + cacheModel.quotaT2 + "%"
      color: Kirigami.Theme.textColor
      font.pixelSize: 13
      verticalAlignment: Text.AlignVCenter
    }
  }
}
