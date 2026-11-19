# tuya-lamp

Controle local via Bluetooth (protocolo **Tuya Beacon Mesh P2**) para lâmpadas smart Tuya Bluetooth-only (testado na **Zinnia Crux CR90**) diretamente do Linux — sem celular, sem gateway e sem nuvem no dia a dia.

## Componentes

1. **Backend em Go (`tuya-lampd`)**
   - Socket HCI bruto (`AF_BLUETOOTH` / `CAP_NET_RAW`) em Go puro.
   - Servidor HTTP em `127.0.0.1:18765` com API REST, Server-Sent Events (`/api/events`) e fila com *debounce/coalescing* para sliders.
   - Login Tuya via QR Code + recuperação automática da `beacon_key` integrado em Go puro.
2. **Frontend Web**
   - Servido direto pelo backend em `http://127.0.0.1:18765`.
3. **Widget Plasmoid (KDE Plasma 6)**
   - Widget nativo QML (`me.muriloscolari.tuyalamp`) para a barra/painel do Plasma 6 com ícone SVG próprio.

## Instalação automática

```bash
chmod +x install.sh uninstall.sh
./install.sh
```

O instalador compila o binário Go, instala o serviço `tuya-lampd.service` (rodando como seu usuário apenas com `CAP_NET_RAW`), instala o ícone `tuya-lamp`, o comando `tuya-lamp` no `PATH` e registra o widget **Tuya Lamp** no KDE Plasma 6.

## Desinstalação

```bash
./uninstall.sh          # mantém ~/.config/tuya-lamp
./uninstall.sh --purge  # remove tudo, inclusive chaves salvas
```

## Uso via CLI

```bash
tuya-lamp on
tuya-lamp off
tuya-lamp toggle
tuya-lamp bright 60
tuya-lamp temp 80
tuya-lamp color purple
tuya-lamp status
```
