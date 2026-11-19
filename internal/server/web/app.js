const $ = (id) => document.getElementById(id);

let currentStatus = null;
let dragLockUntil = 0;

function tempLabel(v) {
  if (v < 25) return `Quente (${v}%)`;
  if (v > 75) return `Frio (${v}%)`;
  return `Neutro (${v}%)`;
}

function updateOrb(st) {
  const orb = $("bulb-orb");
  if (!st.state.on) {
    orb.style.background = "#232836";
    orb.style.boxShadow = "none";
    return;
  }
  if (st.state.mode === "color") {
    const color = `hsl(${st.state.hue}, ${st.state.sat}%, 55%)`;
    orb.style.background = color;
    orb.style.boxShadow = `0 0 22px ${color}`;
  } else {
    const t = st.state.temp / 100;
    const r = Math.round(255 - t * 40);
    const g = Math.round(190 + t * 50);
    const b = Math.round(115 + t * 140);
    const color = `rgb(${r}, ${g}, ${b})`;
    orb.style.background = color;
    orb.style.boxShadow = `0 0 22px rgba(${r}, ${g}, ${b}, 0.7)`;
  }
}

function intervalLabel(ms) {
  const hz = (1000 / ms).toFixed(1);
  return `${ms} ms (~${hz} Hz)`;
}

function clamp(v, lo, hi) {
  return Math.max(lo, Math.min(hi, v));
}

function renderOpenRGB(st) {
  const o = st.openrgb;
  if (!o) return;
  const cfg = o.config || {};

  const toggleBtn = $("openrgb-toggle");
  toggleBtn.classList.toggle("on", !!cfg.enabled);
  toggleBtn.textContent = cfg.enabled ? "Ativado" : "Desativado";

  const dot = $("openrgb-dot");
  const rgb = o.last_rgb || [0, 0, 0];
  if (o.connected) {
    const color = `rgb(${rgb[0]}, ${rgb[1]}, ${rgb[2]})`;
    dot.style.background = color;
    dot.style.boxShadow = `0 0 10px ${color}`;
  } else {
    dot.style.background = "#363e52";
    dot.style.boxShadow = "none";
  }

  let statusText = "Aguardando OpenRGB";
  if (o.last_error) {
    statusText = `Erro DDP: ${o.last_error}`;
  } else if (o.streaming) {
    statusText = `Sincronizando (${o.packets_rx} rx / ${o.frames_tx} ble)`;
  } else if (o.connected && !cfg.enabled) {
    statusText = "OpenRGB conectado (pausado)";
  } else if (o.connected && !st.state.on) {
    statusText = "OpenRGB conectado (lâmpada desligada)";
  } else if (o.connected) {
    statusText = `OpenRGB conectado (${o.listen_addr || "127.0.0.1:4048"})`;
  } else if (o.listen_addr) {
    statusText = `Aguardando OpenRGB (${o.listen_addr})`;
  }
  $("openrgb-status").textContent = statusText;

  if (o.listen_addr && o.listen_addr.includes(":")) {
    const parts = o.listen_addr.split(":");
    const port = parts.pop();
    const host = parts.join(":");
    $("ddp-spec-ip").textContent = host === "0.0.0.0" || !host ? "127.0.0.1" : host;
    $("ddp-spec-port").textContent = port || "4048";
  }

  if (Date.now() > dragLockUntil) {
    const mb = cfg.max_bright ?? 100;
    const mi = cfg.min_interval_ms ?? 40;
    $("openrgb-bright-range").value = mb;
    $("openrgb-bright-val").textContent = `${mb}%`;
    $("openrgb-interval-range").value = mi;
    $("openrgb-interval-val").textContent = intervalLabel(mi);
  }

  const blackMode = cfg.black_mode === "off" ? "off" : "dim";
  $("black-dim-btn").classList.toggle("active", blackMode === "dim");
  $("black-off-btn").classList.toggle("active", blackMode === "off");
}

function render(st) {
  currentStatus = st;
  $("lamp-name").textContent = st.name || "Zinnia Crux CR90";

  if (!st.configured) {
    $("lamp-meta").textContent = "Não configurada — abra Configuração abaixo";
  } else if (st.last_error) {
    $("lamp-meta").textContent = `Erro: ${st.last_error}`;
  } else {
    const modeStr = st.state.mode === "color" ? `Cor ${st.state.hue}°` : `Branco ${st.state.temp}%`;
    const syncTag = st.openrgb && st.openrgb.streaming && st.state.on ? " • OpenRGB Sync" : "";
    $("lamp-meta").textContent = `Nó ${st.node} • ${st.state.on ? "Ligada" : "Desligada"} • ${modeStr}${syncTag}`;
  }

  const pbtn = $("power-btn");
  pbtn.classList.toggle("on", !!st.state.on);
  $("power-label").textContent = st.state.on ? "Ligada" : "Desligada";

  const isColor = st.state.mode === "color";
  $("tab-white").classList.toggle("active", !isColor);
  $("tab-color").classList.toggle("active", isColor);
  $("panel-white").classList.toggle("hidden", isColor);
  $("panel-color").classList.toggle("hidden", !isColor);

  if (Date.now() > dragLockUntil) {
    $("bright-range").value = st.state.bright;
    $("bright-val").textContent = `${st.state.bright}%`;
    $("temp-range").value = st.state.temp;
    $("temp-val").textContent = tempLabel(st.state.temp);
    $("hue-range").value = st.state.hue;
    $("hue-val").textContent = `${st.state.hue}°`;
    $("sat-range").value = st.state.sat;
    $("sat-val").textContent = `${st.state.sat}%`;
  }
  updateOrb(st);
  renderOpenRGB(st);
}

async function sendCommand(payload) {
  const res = await fetch("/api/command", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  const data = await res.json();
  if (res.ok) render(data);
}

async function sendOpenRGB(payload) {
  const res = await fetch("/api/openrgb", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
  const data = await res.json();
  if (res.ok) render(data);
}

$("power-btn").addEventListener("click", () => {
  sendCommand({ action: "toggle" });
});

$("tab-white").addEventListener("click", () => {
  sendCommand({ action: "white" });
});

$("tab-color").addEventListener("click", () => {
  sendCommand({ action: "color" });
});

function bindSlider(id, onInput, onCommit) {
  const el = $(id);
  el.addEventListener("input", () => {
    dragLockUntil = Date.now() + 800;
    onInput(Number(el.value));
  });
  el.addEventListener("change", () => {
    dragLockUntil = Date.now() + 300;
    onCommit(Number(el.value));
  });
}

bindSlider("bright-range", (v) => { $("bright-val").textContent = `${v}%`; }, (v) => sendCommand({ action: "bright", bright: v }));
bindSlider("temp-range", (v) => { $("temp-val").textContent = tempLabel(v); }, (v) => sendCommand({ action: "temp", temp: v }));
bindSlider("hue-range", (v) => { $("hue-val").textContent = `${v}°`; }, (v) => sendCommand({ action: "color", hue: v }));
bindSlider("sat-range", (v) => { $("sat-val").textContent = `${v}%`; }, (v) => sendCommand({ action: "color", sat: v }));

bindSlider("openrgb-bright-range", (v) => { $("openrgb-bright-val").textContent = `${v}%`; }, (v) => sendOpenRGB({ max_bright: v }));
bindSlider("openrgb-interval-range", (v) => { $("openrgb-interval-val").textContent = intervalLabel(v); }, (v) => sendOpenRGB({ min_interval_ms: v, hold_ms: clamp(v - 5, 15, 180) }));

$("openrgb-toggle").addEventListener("click", () => {
  const enabled = currentStatus && currentStatus.openrgb && currentStatus.openrgb.config ? !currentStatus.openrgb.config.enabled : true;
  sendOpenRGB({ enabled });
});

document.querySelectorAll("[data-black]").forEach((btn) => {
  btn.addEventListener("click", () => {
    sendOpenRGB({ black_mode: btn.dataset.black });
  });
});

$("openrgb-register-btn").addEventListener("click", async () => {
  const btn = $("openrgb-register-btn");
  const statusEl = $("openrgb-register-status");
  btn.disabled = true;
  statusEl.textContent = "Configurando OpenRGB...";
  try {
    const res = await fetch("/api/openrgb/register", { method: "POST" });
    const data = await res.json();
    statusEl.textContent = res.ok ? data.message : (data.error || "Erro ao configurar OpenRGB");
  } catch (e) {
    statusEl.textContent = `Erro: ${e.message}`;
  } finally {
    btn.disabled = false;
  }
});

document.querySelectorAll("[data-white]").forEach((btn) => {
  btn.addEventListener("click", () => {
    const [bright, temp] = btn.dataset.white.split(",").map(Number);
    sendCommand({ action: "set", mode: "white", bright, temp });
  });
});

document.querySelectorAll("[data-hue]").forEach((btn) => {
  btn.addEventListener("click", () => {
    sendCommand({ action: "color", hue: Number(btn.dataset.hue), sat: 100 });
  });
});

$("capture-btn").addEventListener("click", async () => {
  $("capture-status").textContent = "Escutando 45s... Desligue e ligue a lâmpada no app do celular.";
  $("capture-btn").disabled = true;
  try {
    const res = await fetch("/api/setup/capture", { method: "POST" });
    const data = await res.json();
    $("capture-status").textContent = res.ok ? `Capturado (nó ${data.node})!` : `Erro: ${data.error}`;
  } catch (e) {
    $("capture-status").textContent = `Erro: ${e.message}`;
  } finally {
    $("capture-btn").disabled = false;
  }
});

let qrPollTimer = null;
$("qr-btn").addEventListener("click", async () => {
  const userCode = $("user-code").value.trim();
  if (!userCode) return;
  if (qrPollTimer) clearInterval(qrPollTimer);
  const res = await fetch("/api/setup/qr", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ user_code: userCode }),
  });
  const data = await res.json();
  if (!res.ok) {
    $("qr-status").textContent = `Erro: ${data.error}`;
    $("qr-box").classList.remove("hidden");
    return;
  }
  $("qr-img").src = `data:image/png;base64,${data.qr_png_b64}`;
  $("qr-status").textContent = "Escaneie no app Tuya/Smart Life e confirme...";
  $("qr-box").classList.remove("hidden");

  qrPollTimer = setInterval(async () => {
    const p = await fetch("/api/setup/qr/poll", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ token: data.token, user_code: userCode }),
    });
    const pd = await p.json();
    if (pd.confirmed) {
      clearInterval(qrPollTimer);
      if (pd.matched) {
        $("qr-status").textContent = `Pareado com '${pd.matched}'!`;
        if (pd.status) render(pd.status);
      } else {
        $("qr-status").textContent = "Login confirmado! Capture OFF/ON primeiro se ainda não capturou.";
      }
    }
  }, 3000);
});

const es = new EventSource("/api/events");
es.onmessage = (ev) => {
  try { render(JSON.parse(ev.data)); } catch (_) {}
};

fetch("/api/status").then((r) => r.json()).then(render).catch(() => {});
