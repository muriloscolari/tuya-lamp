#!/usr/bin/env bash
set -euo pipefail

TARGET_USER="${SUDO_USER:-$USER}"
USER_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
PLASMOID_ID="me.muriloscolari.tuyalamp"
PURGE=false

for arg in "$@"; do
    case "$arg" in
        --purge) PURGE=true ;;
        -h|--help)
            echo "Uso: ./uninstall.sh [--purge]"
            echo "  --purge   Remove também as configurações e chaves em ~/.config/tuya-lamp"
            exit 0
            ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "==> Solicitando sudo para remover serviço e binários..."
    exec sudo -E "$0" "$@"
fi

echo "==> Parando e desabilitando serviço tuya-lampd..."
systemctl disable --now tuya-lampd.service >/dev/null 2>&1 || true
systemctl reset-failed tuya-lampd.service >/dev/null 2>&1 || true
rm -f /etc/systemd/system/tuya-lampd.service
systemctl daemon-reload

echo "==> Removendo binários, ícones e atalho..."
rm -f /usr/local/bin/tuya-lampd /usr/local/bin/tuya-lamp
rm -f /usr/share/icons/hicolor/scalable/apps/tuya-lamp.svg
rm -f "$USER_HOME/.local/share/icons/hicolor/scalable/apps/tuya-lamp.svg"
rm -f "$USER_HOME/.local/share/applications/tuya-lamp.desktop"

if command -v kpackagetool6 >/dev/null 2>&1; then
    echo "==> Removendo widget Plasmoid ($PLASMOID_ID)..."
    sudo -u "$TARGET_USER" kpackagetool6 -t Plasma/Applet -r "$PLASMOID_ID" >/dev/null 2>&1 || true
fi

if [ "$PURGE" = true ]; then
    echo "==> Removendo configurações em $USER_HOME/.config/tuya-lamp..."
    rm -rf "$USER_HOME/.config/tuya-lamp"
else
    echo "Configurações mantidas em $USER_HOME/.config/tuya-lamp (use --purge para apagar)."
fi

echo "Desinstalação concluída!"
