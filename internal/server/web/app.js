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

function render(st) {
  currentStatus = st;
  $("lamp-name").textContent = st.name || "Zinnia Crux CR90";

  if (!st.configured) {
    $("lamp-meta").textContent = "Não configurada — abra Configuração abaixo";
  } else if (st.last_error) {
    $("lamp-meta").textContent = `Erro: ${st.last_error}`;
  } else {
    const modeStr = st.state.mode === "color" ? `Cor ${st.state.hue}°` : `Branco ${st.state.temp}%`;
    $("lamp-meta").textContent = `Nó ${st.node} • ${st.state.on ? "Ligada" : "Desligada"} • ${modeStr}`;
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
