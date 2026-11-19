#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
TARGET_USER="${SUDO_USER:-$USER}"
USER_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
USER_GROUP="$(id -gn "$TARGET_USER")"
PLASMOID_ID="me.muriloscolari.tuyalamp"
ICON_SRC="$ROOT/plasmoid/$PLASMOID_ID/contents/icons/tuya-lamp.svg"

if ! command -v go >/dev/null 2>&1; then
    echo "Erro: Go não encontrado no PATH." >&2
    exit 1
fi

echo "==> Compilando tuya-lampd (Go)..."
if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ]; then
    sudo -u "$TARGET_USER" bash -c "cd '$ROOT' && go build -o '$ROOT/tuya-lampd' ./cmd/tuya-lampd"
else
    (cd "$ROOT" && go build -o "$ROOT/tuya-lampd" ./cmd/tuya-lampd)
fi

if [ "$(id -u)" -ne 0 ]; then
    echo "==> Solicitando sudo para instalar o binário e o serviço systemd..."
    exec sudo -E "$0" "$@"
fi

echo "==> Instalando binário em /usr/local/bin/tuya-lampd..."
systemctl stop tuya-lampd.service >/dev/null 2>&1 || true
pkill -f '/tuya-lampd serve' >/dev/null 2>&1 || true
install -m 0755 -o "$TARGET_USER" -g "$USER_GROUP" "$ROOT/tuya-lampd" /usr/local/bin/tuya-lampd
setcap cap_net_raw+ep /usr/local/bin/tuya-lampd >/dev/null 2>&1 || true
ln -sf /usr/local/bin/tuya-lampd /usr/local/bin/tuya-lamp

echo "==> Instalando ícone e atalho de aplicativo..."
install -d -m 0755 /usr/share/icons/hicolor/scalable/apps
install -m 0644 "$ICON_SRC" /usr/share/icons/hicolor/scalable/apps/tuya-lamp.svg
sudo -u "$TARGET_USER" mkdir -p "$USER_HOME/.local/share/icons/hicolor/scalable/apps" "$USER_HOME/.local/share/applications"
sudo -u "$TARGET_USER" cp "$ICON_SRC" "$USER_HOME/.local/share/icons/hicolor/scalable/apps/tuya-lamp.svg"
cat > "$USER_HOME/.local/share/applications/tuya-lamp.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Tuya Lamp (Zinnia Crux CR90)
Comment=Controle local Bluetooth para lâmpada Tuya Beacon
Icon=tuya-lamp
Exec=xdg-open http://127.0.0.1:18765
Terminal=false
Categories=Utility;Settings;HardwareSettings;
EOF
chown "$TARGET_USER:$USER_GROUP" "$USER_HOME/.local/share/applications/tuya-lamp.desktop"

echo "==> Instalando serviço systemd tuya-lampd.service..."
sed -e "s|@USER@|$TARGET_USER|g" -e "s|@GROUP@|$USER_GROUP|g" \
    "$ROOT/systemd/tuya-lampd.service" > /etc/systemd/system/tuya-lampd.service

systemctl daemon-reload
systemctl enable tuya-lampd.service
systemctl reset-failed tuya-lampd.service >/dev/null 2>&1 || true
systemctl restart tuya-lampd.service

if command -v kpackagetool6 >/dev/null 2>&1; then
    echo "==> Instalando widget Plasmoid do KDE Plasma 6 ($PLASMOID_ID)..."
    PLASMOID_SRC="$ROOT/plasmoid/$PLASMOID_ID"
    if sudo -u "$TARGET_USER" kpackagetool6 -t Plasma/Applet -s "$PLASMOID_ID" >/dev/null 2>&1; then
        sudo -u "$TARGET_USER" kpackagetool6 -t Plasma/Applet -u "$PLASMOID_SRC"
    else
        sudo -u "$TARGET_USER" kpackagetool6 -t Plasma/Applet -i "$PLASMOID_SRC"
    fi
    sudo -u "$TARGET_USER" kbuildsycoca6 >/dev/null 2>&1 || true
else
    echo "Aviso: kpackagetool6 não encontrado; pulando instalação do Plasmoid."
fi

sleep 0.5
if curl -sf http://127.0.0.1:18765/api/status >/dev/null 2>&1; then
    echo "==> Serviço tuya-lampd online e respondendo em http://127.0.0.1:18765!"
else
    echo "Aviso: verifique 'systemctl status tuya-lampd'."
fi

echo ""
echo "Instalação concluída!"
echo "  - Backend + Web UI: http://127.0.0.1:18765"
echo "  - CLI:              tuya-lamp on | off | bright 50 | temp 80 | color purple"
echo "  - Serviço:          systemctl status tuya-lampd"
echo "  - Plasmoid KDE:     Clique direito no painel -> Adicionar Widgets -> 'Tuya Lamp'"
