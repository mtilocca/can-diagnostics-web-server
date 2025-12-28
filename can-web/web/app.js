const el = (id) => document.getElementById(id);

let refreshMs = 200;
let timer = null;

function fmtTime(ts) {
  const d = new Date(ts);
  return d.toLocaleTimeString();
}

async function fetchState() {
  const res = await fetch("/api/state");
  if (!res.ok) return;
  const data = await res.json();

  // Signals
  const stBody = el("signalsTable").querySelector("tbody");
  stBody.innerHTML = "";
  for (const s of data.signals) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td>${s.frame_name}<div class="muted mono">${s.frame_id}</div></td>
      <td class="mono">${s.name}</td>
      <td>${Number(s.value).toFixed(3).replace(/\.?0+$/, "")}</td>
      <td>${s.unit || ""}</td>
      <td><span class="pill">${s.dir}</span></td>
      <td class="mono">${fmtTime(s.updated_at)}</td>
      <td class="muted">${s.comment || ""}</td>
    `;
    stBody.appendChild(tr);
  }

  // Raw frames (latest first)
  const rtBody = el("rawTable").querySelector("tbody");
  rtBody.innerHTML = "";
  for (const f of data.raw.slice().reverse()) {
    const tr = document.createElement("tr");
    tr.innerHTML = `
      <td class="mono">${fmtTime(f.ts)}</td>
      <td class="mono">${f.id}</td>
      <td class="mono">${f.dlc}</td>
      <td class="mono">${f.data_hex}</td>
      <td class="mono">${f.data_ascii}</td>
    `;
    rtBody.appendChild(tr);
  }
}

function startPolling() {
  if (timer) clearInterval(timer);
  timer = setInterval(fetchState, refreshMs);
}

window.addEventListener("load", () => {
  el("applyRefresh").addEventListener("click", () => {
    refreshMs = Math.max(50, parseInt(el("refreshMs").value || "200", 10));
    startPolling();
  });

  startPolling();
  fetchState();
});
