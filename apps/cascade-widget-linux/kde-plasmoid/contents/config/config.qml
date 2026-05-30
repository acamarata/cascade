/**
 * Cascade Fleet Dashboard — KDE Plasma 6 Applet Configuration
 *
 * Purpose: Declare configuration pages and the associated QML source files
 *          for the Plasma 6 applet settings dialog.
 *
 * Constraints: Must use Plasma 6 ConfigModel/ConfigCategory API.
 * SPORT: cascade-widget/kde — config declaration
 */

import org.kde.plasma.configuration 2.0

ConfigModel {
    ConfigCategory {
        name: "General"
        icon: "configure"
        source: "ConfigGeneral.qml"
    }
}
