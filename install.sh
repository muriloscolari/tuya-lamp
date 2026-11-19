#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
TARGET_USER="${SUDO_USER:-$USER}"
USER_HOME="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
USER_GROUP="$(id -gn "$TARGET_USER")"
PLASMOID_ID="me.muriloscolari.tuyalamp"

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
install -m 0755 -o root -g root "$ROOT/tuya-lampd" /usr/local/bin/tuya-lampd
ln -sf /usr/local/bin/tuya-lampd /usr/local/bin/tuya-lamp

echo "==> Instalando serviço systemd tuya-lampd.service..."
sed -e "s|@USER@|$TARGET_USER|g" -e "s|@GROUP@|$USER_GROUP|g" \
    "$ROOT/systemd/tuya-lampd.service" > /etc/systemd/system/tuya-lampd.service

systemctl daemon-reload
systemctl enable tuya-lampd.service
systemctl restart tuya-lampd.service

if command -v kpackagetool6 >/dev/null 2>&1; then
    echo "==> Instalando widget Plasmoid do KDE Plasma 6 ($PLASMOID_ID)..."
    PLASMOID_SRC="$ROOT/plasmoid/$PLASMOID_ID"
    if sudo -u "$TARGET_USER" kpackagetool6 -t Plasma/Applet -s "$PLASMOID_ID" >/dev/null 2>&1; then
        sudo -u "$TARGET_USER" kpackagetool6 -t Plasma/Applet -u "$PLASMOID_SRC"
    else
        sudo -u "$TARGET_USER" kpackagetool6 -t Plasma/Applet -i "$PLASMOID_SRC"
    fi
else
    echo "Aviso: kpackagetool6 não encontrado; pulando instalação do Plasmoid."
fi

echo ""
echo "Instalação concluída!"
echo "  - Backend + Web UI: http://127.0.0.1:8765"
echo "  - CLI:              tuya-lamp on | off | bright 50 | temp 80 | color purple"
echo "  - Serviço:          systemctl status tuya-lampd"
echo "  - Plasmoid KDE:     Clique direito no painel -> Adicionar Widgets -> 'Tuya Lamp'"
