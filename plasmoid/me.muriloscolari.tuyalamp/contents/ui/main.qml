import QtQuick
import QtQuick.Layouts
import org.kde.plasma.plasmoid
import org.kde.plasma.core as PlasmaCore
import org.kde.plasma.components as PlasmaComponents
import org.kde.kirigami as Kirigami

PlasmoidItem {
    id: root

    readonly property string apiBase: "http://127.0.0.1:18765"
    readonly property url bulbIconUrl: Qt.resolvedUrl("../icons/tuya-lamp.svg")

    property bool daemonOnline: false
    property bool configured: false
    property string lampName: "Zinnia Crux CR90"
    property bool lampOn: false
    property string lampMode: "white"
    property int lampBright: 100
    property int lampTemp: 50
    property int lampHue: 270
    property int lampSat: 100
    property bool openrgbEnabled: true
    property bool openrgbConnected: false
    property bool openrgbStreaming: false
    property string openrgbListenAddr: ""
    property double dragLockUntil: 0

    preferredRepresentation: compactRepresentation
    switchWidth: Kirigami.Units.gridUnit * 16
    switchHeight: Kirigami.Units.gridUnit * 18

    function currentPreviewColor() {
        if (!lampOn) {
            return Kirigami.Theme.disabledTextColor
        }
        if (lampMode === "color") {
            return Qt.hsla(lampHue / 360.0, lampSat / 100.0, 0.55, 1.0)
        }
        var t = lampTemp / 100.0
        return Qt.rgba(1.0 - t * 0.15, 0.78 + t * 0.18, 0.50 + t * 0.50, 1.0)
    }

    function applyStatus(data) {
        daemonOnline = true
        configured = !!data.configured
        if (data.name) lampName = data.name
        if (data.state) {
            lampOn = !!data.state.on
            lampMode = data.state.mode || "white"
            if (Date.now() > dragLockUntil) {
                lampBright = data.state.bright || 100
                lampTemp = data.state.temp !== undefined ? data.state.temp : 50
                lampHue = data.state.hue !== undefined ? data.state.hue : 270
                lampSat = data.state.sat !== undefined ? data.state.sat : 100
            }
        }
        if (data.openrgb) {
            if (data.openrgb.config) {
                openrgbEnabled = !!data.openrgb.config.enabled
            }
            openrgbConnected = !!data.openrgb.connected
            openrgbStreaming = !!data.openrgb.streaming
            openrgbListenAddr = data.openrgb.listen_addr || ""
        }
    }

    function fetchStatus() {
        var xhr = new XMLHttpRequest()
        xhr.open("GET", apiBase + "/api/status", true)
        xhr.onreadystatechange = function() {
            if (xhr.readyState === XMLHttpRequest.DONE) {
                if (xhr.status === 200) {
                    try {
                        applyStatus(JSON.parse(xhr.responseText))
                    } catch (e) {}
                } else {
                    daemonOnline = false
                }
            }
        }
        xhr.send()
    }

    function sendCommand(payload) {
        var xhr = new XMLHttpRequest()
        xhr.open("POST", apiBase + "/api/command", true)
        xhr.setRequestHeader("Content-Type", "application/json")
        xhr.onreadystatechange = function() {
            if (xhr.readyState === XMLHttpRequest.DONE && xhr.status === 200) {
                try {
                    applyStatus(JSON.parse(xhr.responseText))
                } catch (e) {}
            }
        }
        xhr.send(JSON.stringify(payload))
    }

    function sendOpenRGB(payload) {
        var xhr = new XMLHttpRequest()
        xhr.open("POST", apiBase + "/api/openrgb", true)
        xhr.setRequestHeader("Content-Type", "application/json")
        xhr.onreadystatechange = function() {
            if (xhr.readyState === XMLHttpRequest.DONE && xhr.status === 200) {
                try {
                    applyStatus(JSON.parse(xhr.responseText))
                } catch (e) {}
            }
        }
        xhr.send(JSON.stringify(payload))
    }

    Timer {
        interval: root.expanded ? 1500 : 3500
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: root.fetchStatus()
    }

    onExpandedChanged: {
        if (expanded) fetchStatus()
    }

    toolTipMainText: lampName
    toolTipSubText: !daemonOnline ? "Backend offline (tuya-lampd)" :
                    (!configured ? "Não configurada" :
                    (lampOn ? ("Ligada • Brilho " + lampBright + "%" + (openrgbStreaming ? " • OpenRGB Sync" : "")) : "Desligada"))

    compactRepresentation: MouseArea {
        id: compactRoot
        implicitWidth: Kirigami.Units.iconSizes.medium
        implicitHeight: Kirigami.Units.iconSizes.medium
        Layout.minimumWidth: Kirigami.Units.iconSizes.smallMedium
        Layout.minimumHeight: Kirigami.Units.iconSizes.smallMedium
        Layout.preferredWidth: Kirigami.Units.iconSizes.medium
        Layout.preferredHeight: Kirigami.Units.iconSizes.medium

        acceptedButtons: Qt.LeftButton | Qt.MiddleButton
        hoverEnabled: true

        onClicked: (mouse) => {
            if (mouse.button === Qt.MiddleButton) {
                root.sendCommand({ action: "toggle" })
            } else {
                root.expanded = !root.expanded
            }
        }

        Kirigami.Icon {
            anchors.centerIn: parent
            width: Math.min(parent.width, parent.height) * 0.86
            height: width
            source: root.bulbIconUrl
            opacity: root.daemonOnline ? (root.lampOn ? 1.0 : 0.45) : 0.3
        }

        Rectangle {
            width: Math.max(7, Math.min(parent.width, parent.height) * 0.28)
            height: width
            radius: width / 2
            anchors.right: parent.right
            anchors.bottom: parent.bottom
            anchors.margins: 1
            color: root.daemonOnline ? root.currentPreviewColor() : Kirigami.Theme.negativeTextColor
            border.width: 1
            border.color: Kirigami.Theme.backgroundColor
        }
    }

    fullRepresentation: Item {
        Layout.preferredWidth: Kirigami.Units.gridUnit * 19
        Layout.preferredHeight: mainCol.implicitHeight + Kirigami.Units.largeSpacing * 2
        Layout.minimumWidth: Kirigami.Units.gridUnit * 16

        ColumnLayout {
            id: mainCol
            anchors.fill: parent
            anchors.margins: Kirigami.Units.largeSpacing
            spacing: Kirigami.Units.smallSpacing * 1.5

            // Header
            RowLayout {
                Layout.fillWidth: true
                spacing: Kirigami.Units.smallSpacing

                Rectangle {
                    width: 28
                    height: 28
                    radius: 14
                    color: root.daemonOnline ? root.currentPreviewColor() : Kirigami.Theme.negativeTextColor
                    border.width: 1
                    border.color: Qt.rgba(1, 1, 1, 0.25)

                    Kirigami.Icon {
                        anchors.centerIn: parent
                        width: 18
                        height: 18
                        source: root.bulbIconUrl
                    }
                }

                ColumnLayout {
                    Layout.fillWidth: true
                    spacing: 0
                    PlasmaComponents.Label {
                        text: root.lampName
                        font.bold: true
                        elide: Text.ElideRight
                        Layout.fillWidth: true
                    }
                    PlasmaComponents.Label {
                        text: !root.daemonOnline ? "Serviço tuya-lampd offline" :
                              (!root.configured ? "Abra o painel web para parear" :
                              (root.lampOn ? (root.lampMode === "color" ? "Cor " + root.lampHue + "°" : "Branco " + root.lampTemp + "%") : "Desligada"))
                        font.pointSize: Kirigami.Theme.smallFont.pointSize
                        opacity: 0.7
                    }
                }

                PlasmaComponents.Switch {
                    checked: root.lampOn
                    enabled: root.daemonOnline && root.configured
                    onToggled: root.sendCommand({ action: "toggle" })
                }
            }

            Kirigami.Separator { Layout.fillWidth: true }

            // Mode Tabs
            RowLayout {
                Layout.fillWidth: true
                enabled: root.daemonOnline && root.configured
                spacing: Kirigami.Units.smallSpacing

                PlasmaComponents.Button {
                    Layout.fillWidth: true
                    text: "Branco"
                    checked: root.lampMode === "white"
                    checkable: true
                    onClicked: root.sendCommand({ action: "white" })
                }
                PlasmaComponents.Button {
                    Layout.fillWidth: true
                    text: "Cor (RGB)"
                    checked: root.lampMode === "color"
                    checkable: true
                    onClicked: root.sendCommand({ action: "color" })
                }
            }

            // Brightness
            ColumnLayout {
                Layout.fillWidth: true
                enabled: root.daemonOnline && root.configured
                spacing: 2

                RowLayout {
                    Layout.fillWidth: true
                    PlasmaComponents.Label { text: "Brilho"; Layout.fillWidth: true }
                    PlasmaComponents.Label { text: Math.round(brightSlider.value) + "%"; opacity: 0.75 }
                }
                PlasmaComponents.Slider {
                    id: brightSlider
                    Layout.fillWidth: true
                    from: 1
                    to: 100
                    stepSize: 1
                    value: root.lampBright
                    onMoved: {
                        root.dragLockUntil = Date.now() + 900
                    }
                    onPressedChanged: {
                        if (!pressed) {
                            root.dragLockUntil = Date.now() + 400
                            root.sendCommand({ action: "bright", bright: Math.round(value) })
                        }
                    }
                }
            }

            // White mode controls
            ColumnLayout {
                Layout.fillWidth: true
                visible: root.lampMode === "white"
                enabled: root.daemonOnline && root.configured
                spacing: Kirigami.Units.smallSpacing

                RowLayout {
                    Layout.fillWidth: true
                    PlasmaComponents.Label { text: "Temperatura"; Layout.fillWidth: true }
                    PlasmaComponents.Label {
                        text: tempSlider.value < 25 ? "Quente" : (tempSlider.value > 75 ? "Frio" : "Neutro")
                        opacity: 0.75
                    }
                }
                PlasmaComponents.Slider {
                    id: tempSlider
                    Layout.fillWidth: true
                    from: 0
                    to: 100
                    stepSize: 1
                    value: root.lampTemp
                    onMoved: root.dragLockUntil = Date.now() + 900
                    onPressedChanged: {
                        if (!pressed) {
                            root.dragLockUntil = Date.now() + 400
                            root.sendCommand({ action: "temp", temp: Math.round(value) })
                        }
                    }
                }
                RowLayout {
                    Layout.fillWidth: true
                    spacing: 4
                    PlasmaComponents.Button {
                        Layout.fillWidth: true
                        text: "Relaxar"
                        onClicked: root.sendCommand({ action: "set", mode: "white", bright: 40, temp: 15 })
                    }
                    PlasmaComponents.Button {
                        Layout.fillWidth: true
                        text: "Leitura"
                        onClicked: root.sendCommand({ action: "set", mode: "white", bright: 85, temp: 55 })
                    }
                    PlasmaComponents.Button {
                        Layout.fillWidth: true
                        text: "Foco"
                        onClicked: root.sendCommand({ action: "set", mode: "white", bright: 100, temp: 100 })
                    }
                }
            }

            // Color mode controls
            ColumnLayout {
                Layout.fillWidth: true
                visible: root.lampMode === "color"
                enabled: root.daemonOnline && root.configured
                spacing: Kirigami.Units.smallSpacing

                RowLayout {
                    Layout.fillWidth: true
                    PlasmaComponents.Label { text: "Matiz (Hue)"; Layout.fillWidth: true }
                    PlasmaComponents.Label { text: Math.round(hueSlider.value) + "°"; opacity: 0.75 }
                }
                PlasmaComponents.Slider {
                    id: hueSlider
                    Layout.fillWidth: true
                    from: 0
                    to: 359
                    stepSize: 1
                    value: root.lampHue
                    onMoved: root.dragLockUntil = Date.now() + 900
                    onPressedChanged: {
                        if (!pressed) {
                            root.dragLockUntil = Date.now() + 400
                            root.sendCommand({ action: "color", hue: Math.round(value) })
                        }
                    }
                }

                GridLayout {
                    Layout.fillWidth: true
                    columns: 8
                    columnSpacing: 6
                    Repeater {
                        model: [0, 30, 60, 120, 180, 240, 270, 330]
                        delegate: Rectangle {
                            Layout.fillWidth: true
                            implicitHeight: 22
                            radius: 11
                            color: Qt.hsla(modelData / 360.0, 1.0, 0.5, 1.0)
                            border.width: root.lampHue === modelData ? 2 : 1
                            border.color: root.lampHue === modelData ? Kirigami.Theme.textColor : Qt.rgba(1,1,1,0.25)
                            MouseArea {
                                anchors.fill: parent
                                cursorShape: Qt.PointingHandCursor
                                onClicked: root.sendCommand({ action: "color", hue: modelData, sat: 100 })
                            }
                        }
                    }
                }
            }

            Kirigami.Separator { Layout.fillWidth: true }

            RowLayout {
                Layout.fillWidth: true
                enabled: root.daemonOnline && root.configured
                spacing: Kirigami.Units.smallSpacing

                Kirigami.Icon {
                    source: "color-management"
                    Layout.preferredWidth: 18
                    Layout.preferredHeight: 18
                    opacity: root.openrgbConnected ? 1.0 : 0.6
                }

                ColumnLayout {
                    Layout.fillWidth: true
                    spacing: 0
                    PlasmaComponents.Label {
                        text: "OpenRGB Sync"
                        font.bold: true
                        Layout.fillWidth: true
                    }
                    PlasmaComponents.Label {
                        text: root.openrgbStreaming ? "Sincronizando (DDP)" :
                              (root.openrgbConnected ? (root.openrgbEnabled ? "OpenRGB conectado" : "OpenRGB conectado (pausado)") :
                              (root.openrgbListenAddr !== "" ? ("Aguardando (" + root.openrgbListenAddr + ")") : "DDP offline"))
                        font.pointSize: Kirigami.Theme.smallFont.pointSize
                        opacity: 0.7
                        elide: Text.ElideRight
                        Layout.fillWidth: true
                    }
                }

                PlasmaComponents.Switch {
                    checked: root.openrgbEnabled
                    onToggled: root.sendOpenRGB({ enabled: !root.openrgbEnabled })
                }
            }

            Kirigami.Separator { Layout.fillWidth: true }

            RowLayout {
                Layout.fillWidth: true
                PlasmaComponents.Label {
                    text: "Clique do meio no ícone: Ligar/Desligar"
                    font.pointSize: Kirigami.Theme.smallFont.pointSize
                    opacity: 0.6
                    Layout.fillWidth: true
                }
                PlasmaComponents.Button {
                    text: "Abrir Web UI"
                    icon.name: "internet-web-browser"
                    onClicked: Qt.openUrlExternally(root.apiBase)
                }
            }
        }
    }
}
