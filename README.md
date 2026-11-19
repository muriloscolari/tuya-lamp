# tuya-lamp

Controle local via Bluetooth (protocolo **Tuya Beacon Mesh P2**) para lâmpadas smart Tuya Bluetooth-only (testado na **Zinnia Crux CR90**) diretamente do Linux — sem celular, sem gateway e sem nuvem no dia a dia.

## Componentes

1. **Backend em Go (`tuya-lampd`)**
   - Socket HCI bruto (`AF_BLUETOOTH` / `CAP_NET_RAW`) em Go puro.
   - Servidor HTTP em `127.0.0.1:18765` com API REST, Server-Sent Events (`/api/events`) e fila com *debounce/coalescing* para sliders.
   - Receptor **OpenRGB DDP (UDP `127.0.0.1:4048`)** integrado para sincronizar a lâmpada com efeitos do OpenRGB (como *Sincronização de Áudio*, *Ambiente*, *Espiral*, etc.) junto ao seu controlador WLED/ESP32.
   - Login Tuya via QR Code + recuperação automática da `beacon_key` integrado em Go puro.
2. **Frontend Web**
   - Servido direto pelo backend em `http://127.0.0.1:18765`, incluindo painel **OpenRGB Sync (DDP)**.
3. **Widget Plasmoid (KDE Plasma 6)**
   - Widget nativo QML (`me.muriloscolari.tuyalamp`) para a barra/painel do Plasma 6 com ícone SVG próprio e toggle rápido de OpenRGB Sync.

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

## Integração com OpenRGB (DDP)

O daemon `tuya-lampd` escuta pacotes **DDP v1** em `udp://127.0.0.1:4048` (o mesmo protocolo de rede usado pelo WLED no OpenRGB) e converte a cor recebida para comandos BLE Tuya Beacon P2 com *rate limiting*, *deadband* de HSV, deduplicação de *keepalive* e rotação automática de `seq`/`src`.

### Configuração no OpenRGB

- **Automática (recomendada, com o OpenRGB fechado):**
  ```bash
  tuya-lamp openrgb setup
  ```
  Ou clique em **Adicionar ao OpenRGB.json** dentro da seção **Como conectar no OpenRGB** da interface web (`http://127.0.0.1:18765`). O utilitário preserva seus dispositivos existentes (como o `WLED`) e cria um backup `OpenRGB.json.bak`.
- **Manual (pela interface do OpenRGB):**
  1. Abra **Configurações → Dispositivos DDP** (*Settings → DDP Devices*).
  2. Adicione um dispositivo com:
     - **Nome:** `Zinnia Crux CR90`
     - **IP:** `127.0.0.1`
     - **Porta:** `4048`
     - **Num LEDs:** `1`
     - **Keepalive:** `1000`
  3. Clique em **Detectar dispositivos** (*Detect Devices*). A lâmpada aparecerá na lista de dispositivos do OpenRGB e do plugin **Effects** (marque a caixa dela junto ao `WLED` em **Sincronização de Áudio** / **Audio Sync**).

### Dicas para Sincronização de Áudio

- **Modo de rolagem (Roll Mode):** no OpenRGB Effects → *Sincronização de Áudio*, se você marcar o **WLED** e a **Zinnia Crux CR90**, o modo *Linear horizontal* funciona bem (a lâmpada recebe a cor principal atual).
- **Intervalo BLE:** o padrão é `150 ms (~6.7 Hz)` porque o protocolo Tuya Beacon P2 transmite via *BLE Advertising*. Se quiser respostas mais rápidas em músicas agitadas, reduza para `100–120 ms` na Web UI ou com `tuya-lamp openrgb interval 110`.
- **No preto (`#000`):** durante silêncio, você pode escolher **Brilho mínimo (`dim`)** para evitar piscar/desligar a lâmpada continuamente, ou **Desligar (`off`)** se quiser blackout total no silêncio.
- **Pausa automática:** ao trocar manualmente para branco ou outra cor pela Web UI/CLI/Plasmoid durante um efeito ativo do OpenRGB, o sync pausa automaticamente para o OpenRGB não sobrescrever sua escolha.

## Uso via CLI

```bash
tuya-lamp on
tuya-lamp off
tuya-lamp toggle
tuya-lamp bright 60
tuya-lamp temp 80
tuya-lamp color purple
tuya-lamp status

# Integração OpenRGB (DDP)
tuya-lamp openrgb status
tuya-lamp openrgb on
tuya-lamp openrgb off
tuya-lamp openrgb bright 80
tuya-lamp openrgb interval 150
tuya-lamp openrgb black dim    # ou off
tuya-lamp openrgb setup
```
