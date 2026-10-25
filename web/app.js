const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

const state = {
  devices: [],
  filter: "all",
  search: "",
  pending: new Set(),
  config: null,
  auth: false,
};

const ICONS = {
  phone: "📱", computer: "💻", camera: "📷", appliance: "🧺", tv: "📺",
  speaker: "🔊", router: "🛰️", plug: "🔌", light: "💡", wearable: "⌚",
  tablet: "📲", unknown: "❓",
};
const TYPE_LABELS = {
  phone: "手机", computer: "电脑", camera: "摄像头", appliance: "家电",
  tv: "电视", speaker: "音箱", router: "路由器", plug: "插座", light: "灯",
  wearable: "穿戴", tablet: "平板", unknown: "未知",
};

function escapeHTML(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

async function api(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
  });
  if (res.status === 401) {
    showLogin();
    throw new Error("unauthorized");
  }
  let data = {};
  try { data = await res.json(); } catch (_) {}
  if (!res.ok) throw new Error(data.error || res.statusText || "request failed");
  return data;
}

function showLogin() {
  state.auth = false;
  $("#login").classList.remove("hidden");
  $("#app").classList.add("hidden");
}

function showApp() {
  state.auth = true;
  $("#login").classList.add("hidden");
  $("#app").classList.remove("hidden");
}

function showToast(msg, isError = false) {
  const t = $("#toast");
  t.textContent = msg;
  t.classList.toggle("err", isError);
  t.classList.add("show");
  clearTimeout(showToast._timer);
  showToast._timer = setTimeout(() => t.classList.remove("show"), 2800);
}

function renderStatus(s) {
  const parts = [];
  if (s.router_host) parts.push(s.router_host);
  if (s.gateway) parts.push("代理网关 " + s.gateway);
  if (s.mock) parts.push("Mock 模式");
  if (s.scan_error) parts.push(s.scan_error);
  $("#router-line").textContent = parts.join(" · ") || "—";

  const pill = $("#router-status");
  pill.classList.remove("ok", "bad");
  const label = pill.querySelector("span");
  if (s.mock) {
    pill.classList.add("ok");
    label.textContent = "Mock 模式";
  } else if (s.router_ok) {
    pill.classList.add("ok");
    label.textContent = "路由器在线";
  } else {
    pill.classList.add("bad");
    label.textContent = "路由器离线";
  }
}

function renderStats() {
  const total = state.devices.length;
  const online = state.devices.filter((d) => d.online).length;
  const proxy = state.devices.filter((d) => d.mode === "proxy").length;
  $("#stat-total").textContent = total;
  $("#stat-online").textContent = online;
  $("#stat-proxy").textContent = proxy;
  $("#stat-direct").textContent = total - proxy;
}

function renderGrid() {
  const grid = $("#device-grid");
  const q = state.search.trim().toLowerCase();
  const list = state.devices.filter((d) => {
    if (state.filter === "proxy" && d.mode !== "proxy") return false;
    if (state.filter === "direct" && d.mode !== "direct") return false;
    if (!q) return true;
    return [d.name, d.brand, d.mac, d.ip, d.type].some((v) =>
      String(v || "").toLowerCase().includes(q)
    );
  });

  if (list.length === 0) {
    grid.innerHTML = `<div class="empty"><strong>没有匹配的设备</strong>试试清空搜索或切换筛选。</div>`;
    return;
  }

  grid.innerHTML = list.map((d) => {
    const isProxy = d.mode === "proxy";
    const pending = state.pending.has(d.mac);
    const type = d.type || "unknown";
    return `
      <article class="device ${isProxy ? "proxy" : ""} ${d.online ? "" : "offline"}">
        <div class="device-head">
          <div class="device-icon">${ICONS[type] || ICONS.unknown}</div>
          <div class="device-title">
            <h3>${escapeHTML(d.name)}</h3>
            <p>${escapeHTML(d.brand)} · ${TYPE_LABELS[type] || TYPE_LABELS.unknown}</p>
          </div>
        </div>
        <div class="badges">
          <span class="tag ${d.online ? "online" : "offline"}">${d.online ? "在线" : "离线"}</span>
          <span class="tag ${isProxy ? "proxy" : ""}">${isProxy ? "透明代理" : "直连"}</span>
          ${pending ? '<span class="tag pending">待应用</span>' : ""}
        </div>
        <div class="device-meta">
          <div><span>MAC</span><code>${escapeHTML(d.mac)}</code></div>
          <div><span>IP</span><code>${escapeHTML(d.ip || "—")}</code></div>
        </div>
        <div class="device-foot">
          <span class="mode-label ${isProxy ? "proxy" : ""}">当前：<b>${isProxy ? "代理" : "直连"}</b></span>
          <button class="switch ${isProxy ? "on" : ""}" data-mac="${escapeHTML(d.mac)}" aria-label="切换代理"></button>
        </div>
      </article>`;
  }).join("");

  $$(".switch", grid).forEach((btn) => {
    btn.addEventListener("click", () => toggleMode(btn.dataset.mac));
  });
}

function updateApply() {
  const n = state.pending.size;
  const btn = $("#apply-btn");
  const badge = $("#pending-count");
  btn.disabled = n === 0;
  badge.textContent = n;
  badge.classList.toggle("hidden", n === 0);
}

function renderAll() {
  renderStats();
  renderGrid();
  updateApply();
}

async function loadAll() {
  const [dev, status, cfg] = await Promise.all([
    api("/api/devices"),
    api("/api/status"),
    api("/api/config"),
  ]);
  state.devices = dev.devices || [];
  state.config = cfg;
  renderStatus(status);
  renderAll();
  if (dev.error) showToast(dev.error, true);
}

async function toggleMode(mac) {
  const d = state.devices.find((x) => x.mac === mac);
  if (!d) return;
  const prev = d.mode;
  const next = prev === "proxy" ? "direct" : "proxy";
  d.mode = next;
  state.pending.add(mac);
  renderAll();
  try {
    await api("/api/policy", { method: "POST", body: JSON.stringify({ mac, mode: next }) });
  } catch (err) {
    d.mode = prev;
    state.pending.delete(mac);
    renderAll();
    showToast(err.message, true);
  }
}

async function applyChanges() {
  const btn = $("#apply-btn");
  btn.disabled = true;
  try {
    await api("/api/apply", { method: "POST", body: "{}" });
    state.pending.clear();
    renderAll();
    showToast("已应用到路由器");
  } catch (err) {
    showToast("应用失败：" + err.message, true);
    updateApply();
  }
}

function setAuthMethod(method) {
  $$(".auth-tabs button").forEach((b) => b.classList.toggle("active", b.dataset.auth === method));
  $("#password-field").classList.toggle("hidden", method !== "password");
  $("#key-field").classList.toggle("hidden", method !== "key");
}

function currentAuthMethod() {
  return $(".auth-tabs button.active")?.dataset.auth || "password";
}

async function openSettings() {
  try {
    const cfg = await api("/api/config");
    state.config = cfg;
    $("#cfg-host").value = cfg.router_host || "";
    $("#cfg-user").value = cfg.router_user || "";
    $("#cfg-gateway").value = cfg.proxy_gateway || "";
    $("#cfg-webpass").value = "";
    $("#cfg-password").value = "";
    $("#cfg-key").value = "";
    setAuthMethod(cfg.auth_method || "password");
    $("#settings-output").classList.add("hidden");
    $("#settings-dialog").showModal();
  } catch (err) {
    showToast(err.message, true);
  }
}

$("#login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("#login-error").textContent = "";
  try {
    await api("/api/login", {
      method: "POST",
      body: JSON.stringify({ password: $("#login-password").value }),
    });
    $("#login-password").value = "";
    showApp();
    await loadAll();
  } catch (err) {
    $("#login-error").textContent = err.message === "unauthorized" ? "密码错误" : err.message;
  }
});

$("#refresh-btn").addEventListener("click", async () => {
  try {
    await loadAll();
    showToast("已刷新");
  } catch (err) {
    showToast(err.message, true);
  }
});

$("#settings-btn").addEventListener("click", openSettings);
$("#close-settings").addEventListener("click", () => $("#settings-dialog").close());
$("#apply-btn").addEventListener("click", applyChanges);

$("#logout-btn").addEventListener("click", async () => {
  try { await api("/api/logout", { method: "POST", body: "{}" }); } catch (_) {}
  showLogin();
});

$("#search").addEventListener("input", (e) => {
  state.search = e.target.value;
  renderGrid();
});

$$("#filter button").forEach((b) => {
  b.addEventListener("click", () => {
    state.filter = b.dataset.filter;
    $$("#filter button").forEach((x) => x.classList.toggle("active", x === b));
    renderGrid();
  });
});

$$(".auth-tabs button").forEach((b) => {
  b.addEventListener("click", () => setAuthMethod(b.dataset.auth));
});

$("#settings-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const body = {
    router_host: $("#cfg-host").value.trim(),
    router_user: $("#cfg-user").value.trim(),
    auth_method: currentAuthMethod(),
    password: $("#cfg-password").value,
    key: $("#cfg-key").value,
    web_password: $("#cfg-webpass").value,
    proxy_gateway: $("#cfg-gateway").value.trim(),
  };
  try {
    await api("/api/config", { method: "POST", body: JSON.stringify(body) });
    $("#settings-dialog").close();
    showToast("已保存");
    await loadAll();
  } catch (err) {
    showToast(err.message, true);
  }
});

$("#test-btn").addEventListener("click", async () => {
  const out = $("#settings-output");
  out.classList.remove("hidden");
  out.textContent = "测试中…";
  try {
    const res = await api("/api/test", { method: "POST", body: "{}" });
    out.textContent = (res.ok ? "连接成功\n" : "连接失败\n") + (res.output || "") + (res.error ? "\n" + res.error : "");
    showToast(res.ok ? "连接成功" : "连接失败", !res.ok);
  } catch (err) {
    out.textContent = err.message;
    showToast(err.message, true);
  }
});

$("#hook-btn").addEventListener("click", async () => {
  const out = $("#settings-output");
  out.classList.remove("hidden");
  out.textContent = "安装中…";
  try {
    const res = await api("/api/install-hook", { method: "POST", body: "{}" });
    out.textContent = (res.ok ? "安装成功\n" : "安装失败\n") + (res.output || "") + (res.error ? "\n" + res.error : "");
    showToast(res.ok ? "持久化已安装" : "安装失败", !res.ok);
  } catch (err) {
    out.textContent = err.message;
    showToast(err.message, true);
  }
});

(async function init() {
  try {
    const status = await api("/api/status");
    showApp();
    renderStatus(status);
    await loadAll();
  } catch (_) {
    showLogin();
  }
})();
