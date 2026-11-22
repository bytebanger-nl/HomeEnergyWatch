// Entry point: connection to the gateway (server-sent events), shared state
// and the hash router.
import { renderLive } from "./live.js";
import { renderDevices } from "./devices.js";
import { renderUsage } from "./usage.js";
import { renderAlerts, renderAutomations } from "./rules.js";
import { renderSettings } from "./settings.js";
import { renderKlimaat } from "./klimaat.js";
import { renderBerichten, inboxIcon } from "./berichten.js";
import { api } from "./util.js";
import { authStatus, showLogin } from "./security.js";

const LIVE_WINDOW_MS = 10 * 60 * 1000;

export const state = {
  info: null,
  devices: new Map(), // id -> DeviceView
  plugins: new Map(), // plugin device id -> DeviceView (integrations next to HomeWizard)
  pluginTabs: [], // the user's own tabs for plugin devices: [{id, name, position}]
  tabOrder: [], // order of the tab row by key (see tabList); empty = default
  conn: "connecting", // connecting | live | retry | down
  loaded: false,
  live: new Map(), // id -> [{t, v}] rolling window for sparklines
  controls: new Map(), // "device\u0000key" -> the latest command's status (see controlStatus)
  listeners: new Set(),
};

/** Subscribe to state changes; returns an unsubscribe function. */
export function subscribe(fn) {
  state.listeners.add(fn);
  return () => state.listeners.delete(fn);
}
function emit(change) {
  if (["snapshot", "plugin", "plugins", "plugin_tabs", "tab_order"].includes(change.type)) layoutTabs();
  for (const fn of state.listeners) fn(change);
}

// ---------- tab row ----------
// The fixed tabs come from index.html. Plugin devices have their own tabs:
// Klimaat for devices on no tab of the user's own, and one per own tab
// (#/klimaat/<id>, key "tab:<id>"), by default right after Klimaat. A plugin
// tab without devices is not shown, so without plugin devices there is none.
// The order is chosen under Instellingen and shared by every client
// (state.tabOrder; empty = the default order).

// the fixed tabs in their default order, read before the row is reordered
const FIXED_TABS = [...document.querySelectorAll(".tabs a")].map((a) => a.dataset.route);

/** The keys of all tabs in their default order. */
function defaultTabKeys() {
  const keys = FIXED_TABS;
  const own = state.pluginTabs.map((t) => `tab:${t.id}`);
  const at = keys.indexOf("klimaat") + 1;
  return [...keys.slice(0, at), ...own, ...keys.slice(at)];
}

/** Puts the keys in the stored order; keys of deleted tabs are skipped. A key
 * it does not know (a new own tab) follows the fixed tab it follows by default
 * (Klimaat), after other new ones, not an own tab the user moved elsewhere. */
function orderTabKeys(keys, order) {
  if (!order || !order.length) return keys;
  const out = order.filter((k) => keys.includes(k));
  const added = new Set();
  keys.forEach((k, i) => {
    if (out.includes(k)) return;
    let at = 0;
    for (let j = i - 1; j >= 0; j--) {
      if (keys[j].startsWith("tab:") && !added.has(keys[j])) continue;
      at = out.indexOf(keys[j]) + 1;
      break;
    }
    out.splice(at, 0, k);
    added.add(k);
  });
  return out;
}

/** All tabs in their current order: {key, name, shown}. */
export function tabList() {
  const onTab = new Map(); // tab id (0 = Klimaat) -> device count
  for (const d of state.plugins.values()) onTab.set(d.tab_id || 0, (onTab.get(d.tab_id || 0) || 0) + 1);
  const own = new Map(state.pluginTabs.map((t) => [`tab:${t.id}`, t]));
  return orderTabKeys(defaultTabKeys(), state.tabOrder).map((key) => {
    const t = own.get(key);
    if (t) return { key, name: t.name, shown: !!onTab.get(t.id) };
    const a = document.querySelector(`.tabs a[data-route="${key}"]:not([data-sub])`);
    return { key, name: a?.textContent || key, shown: key !== "klimaat" || !!onTab.get(0) };
  });
}

let tabsKey = "";
function layoutTabs() {
  const tabs = tabList();
  const key = JSON.stringify(tabs);
  if (key === tabsKey) return;
  tabsKey = key;
  const row = document.querySelector(".tabs-row");
  if (!row) return;
  const old = new Map([...row.querySelectorAll("a")].map((a) => [a.dataset.sub ? `tab:${a.dataset.sub}` : a.dataset.route, a]));
  for (const t of tabs) {
    let a = old.get(t.key);
    old.delete(t.key);
    if (!a) {
      const id = t.key.slice(4);
      a = document.createElement("a");
      a.href = `#/klimaat/${id}`;
      a.dataset.route = "klimaat";
      a.dataset.sub = id;
    }
    if (a.dataset.sub) a.textContent = t.name;
    a.hidden = !t.shown;
    row.append(a);
  }
  for (const a of old.values()) if (a.dataset.sub) a.remove(); // deleted own tabs
  markCurrentTab();
}


export function rateKey(device) {
  return device.kind === "water" ? "water_lpm" : "power_w";
}

function pushLive(device) {
  const v = device.reading?.values?.[rateKey(device)];
  if (v == null) return;
  const t = new Date(device.reading.time).getTime();
  let buf = state.live.get(device.id);
  if (!buf) { buf = []; state.live.set(device.id, buf); }
  if (buf.length && buf[buf.length - 1].t >= t) return;
  buf.push({ t, v });
  const cutoff = Date.now() - LIVE_WINDOW_MS;
  while (buf.length && buf[0].t < cutoff) buf.shift();
}

function setDevice(device) {
  state.devices.set(device.id, device);
  pushLive(device);
}

// seed sparklines with the last minutes of history so they are not empty
async function seedLive() {
  const to = new Date();
  const from = new Date(to.getTime() - LIVE_WINDOW_MS);
  await Promise.all([...state.devices.values()].map(async (d) => {
    try {
      const res = await api("GET", `/api/history/rate?device=${encodeURIComponent(d.id)}&from=${from.toISOString()}&to=${to.toISOString()}&points=20`);
      const seeded = res.points.map((p) => ({ t: new Date(p.t).getTime() + 30e3, v: p.avg }));
      const buf = state.live.get(d.id) || [];
      const first = buf.length ? buf[0].t : Infinity;
      state.live.set(d.id, [...seeded.filter((p) => p.t < first), ...buf]);
    } catch { /* history is optional */ }
  }));
  emit({ type: "live" });
}

// ---------- connection ----------
let source = null;
let retryTimer = null;

function setConn(conn) {
  if (state.conn === conn) return;
  state.conn = conn;
  const el = document.getElementById("conn");
  el.dataset.state = conn;
  el.querySelector(".conn-text").textContent = {
    connecting: "Verbinden…",
    live: state.info ? `Live · elke ${state.info.interval_ms / 1000} s` : "Live",
    retry: "Opnieuw verbinden…",
    down: "Geen verbinding",
  }[conn];
  emit({ type: "conn" });
}

function connect() {
  clearTimeout(retryTimer);
  if (source) source.close();
  source = new EventSource("/api/stream");
  source.onmessage = (msg) => {
    let event;
    try { event = JSON.parse(msg.data); } catch { return; }
    if (event.type === "snapshot") {
      state.info = event.data.info;
      const firstLoad = !state.loaded;
      state.devices.clear();
      for (const d of event.data.devices) setDevice(d);
      state.plugins.clear();
      for (const d of event.data.plugins || []) state.plugins.set(d.id, d);
      state.pluginTabs = event.data.plugin_tabs || [];
      state.tabOrder = event.data.tab_order || [];
      state.controls.clear();
      for (const c of event.data.controls || []) state.controls.set(controlKey(c.device, c.key), c);
      state.loaded = true;
      state.conn = "";
      setConn("live");
      emit({ type: "snapshot" });
      if (firstLoad) seedLive();
    } else if (event.type === "device") {
      setDevice(event.data);
      emit({ type: "device", id: event.data.id });
    } else if (event.type === "removed") {
      state.devices.delete(event.data.id);
      state.live.delete(event.data.id);
      emit({ type: "removed", id: event.data.id });
    } else if (event.type === "plugin") {
      state.plugins.set(event.data.id, event.data);
      emit({ type: "plugin", id: event.data.id });
    } else if (event.type === "plugins") {
      state.plugins.clear();
      for (const d of event.data || []) state.plugins.set(d.id, d);
      emit({ type: "plugins" });
    } else if (event.type === "plugin_tabs") {
      state.pluginTabs = event.data || [];
      emit({ type: "plugin_tabs" });
    } else if (event.type === "tab_order") {
      state.tabOrder = event.data || [];
      emit({ type: "tab_order" });
    } else if (event.type === "control") {
      applyControl(event.data);
    } else if (event.type === "outages" || event.type === "notices") {
      emit({ type: event.type }); // an outage started or ended, a notice came or was read
    } else if (event.type === "ista") {
      emit({ type: "ista" }); // new heating data from mijn.ista.nl
    }
  };
  source.onerror = () => {
    // a stream refused for want of a login looks like any other error
    authStatus().then((st) => { if (st && !st.authenticated) needLogin(); });
    if (source.readyState === EventSource.CLOSED) {
      setConn("down");
      retryTimer = setTimeout(connect, 5000);
    } else {
      setConn(state.loaded ? "retry" : "down");
    }
  };
}

document.addEventListener("visibilitychange", () => {
  if (document.body.dataset.login) return;
  if (document.visibilityState === "visible" && (!source || source.readyState === EventSource.CLOSED)) connect();
});

// ---------- commands to devices ----------
// Every command (a tile, a switch, an automation) is sent by the gateway in
// the background: it checks that the device took the new value and tries
// again when not. Its progress arrives as "control" events.

function controlKey(device, key) { return `${device}\u0000${key}`; }

/** The status of the latest command for a control: device is a HomeWizard id or "plugin:<id>". */
export function controlStatus(device, key) { return state.controls.get(controlKey(device, key)) || null; }

/** Whether a command is still being sent (its value is what the control should show meanwhile). */
export function controlBusy(st) { return !!st && (st.state === "busy" || st.state === "retry"); }

/**
 * What to say about a command under its control, or null: busy, trying
 * again, failed (error), done after more than one try. A failure stays
 * until the next command (the gateway forgets it after 10 minutes).
 */
export function controlNote(st) {
  if (!st) return null;
  // an automation's command says so: "Automatisering: niet gelukt …"
  const say = (text, error = false) => ({
    text: st.source === "rule" ? `Automatisering: ${text}` : text[0].toUpperCase() + text.slice(1),
    error,
  });
  if (st.state === "busy") return say("bezig…");
  if (st.state === "retry") return say(`lukt nog niet, opnieuw proberen (poging ${st.attempt} van ${st.attempts})…`);
  if (st.state === "failed") {
    const tries = st.attempt > 1 ? ` na ${st.attempt} pogingen` : "";
    return say(`niet gelukt${tries}: ${st.error}`, true);
  }
  if (st.state === "ok" && st.attempt > 1 && Date.now() - new Date(st.updated).getTime() < 60e3) {
    return say(`gelukt bij poging ${st.attempt}.`);
  }
  return null;
}

/** Record a command's status (from an API answer or an event) and redraw what shows it. */
export function applyControl(st) {
  const k = controlKey(st.device, st.key);
  const cur = state.controls.get(k);
  if (cur && cur.id > st.id) return; // an older command's late news
  state.controls.set(k, st);
  if (st.device.startsWith("plugin:")) emit({ type: "plugin", id: st.device.slice(7) });
  else emit({ type: "device", id: st.device });
}

/** Apply a device view returned by an API call immediately. */
export function applyDevice(device) {
  setDevice(device);
  emit({ type: "device", id: device.id });
}

// ---------- router ----------
const routes = {
  dashboard: renderLive, klimaat: renderKlimaat, verbruik: (v) => renderUsage(v),
  // the tabs Historie and Kosten became "Verbruik en Kosten"; their links still work
  historie: (v) => renderUsage(v, { view: "verbruik" }),
  kosten: (v, sub) => renderUsage(v, { view: "kosten", kind: sub === "water" ? "water" : sub === "verwarming" ? "heating" : "energy" }),
  meldingen: renderAlerts, automatisering: renderAutomations, apparaten: renderDevices, instellingen: renderSettings,
  berichten: renderBerichten, // opened from the icon in the header, not a tab
};
let cleanup = null;

function currentRoute() {
  // #/name or #/name/sub (e.g. #/kosten/water)
  const [name = "dashboard", sub = ""] = location.hash.replace(/^#\/?/, "").split("?")[0].split("/").map((s) => s || undefined);
  // #/live was the dashboard's old name (bookmarks, installed apps)
  return { name: name === "live" ? "dashboard" : name, sub };
}

function markCurrentTab() {
  const route = currentRoute();
  const sub = route.sub;
  // the old Historie and Kosten links open Verbruik en Kosten
  const name = route.name === "historie" || route.name === "kosten" ? "verbruik" : route.name;
  for (const a of document.querySelectorAll(".tabs a")) {
    // plugin tabs differ by their sub route
    const match = a.dataset.route === name && (name !== "klimaat" || (a.dataset.sub || "") === sub);
    if (match) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
}

function route() {
  const { name, sub } = currentRoute();
  const render = routes[name] || renderLive;
  markCurrentTab();
  if (cleanup) cleanup();
  const view = document.getElementById("view");
  view.replaceChildren();
  cleanup = render(view, sub) || null;
}

/** Stops the app and shows the login screen. */
function needLogin() {
  clearTimeout(retryTimer);
  if (source) { source.close(); source = null; }
  if (cleanup) { cleanup(); cleanup = null; }
  showLogin();
}
window.addEventListener("hw:login", needLogin);

// with a password set, nothing but the login screen works until logged in
const auth = await authStatus();
if (auth && auth.enabled && !auth.authenticated) {
  needLogin();
} else {
  window.addEventListener("hashchange", () => { if (!document.body.dataset.login) route(); });
  route();
  inboxIcon(); // loads on the first snapshot
  connect();
}

if ("serviceWorker" in navigator && window.isSecureContext) {
  navigator.serviceWorker.register("/sw.js").catch(() => { /* optional */ });
}
