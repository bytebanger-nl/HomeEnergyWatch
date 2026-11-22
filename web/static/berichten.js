// Berichten: what needs the user's attention, in one place. The outages of
// every device (HomeWizard and plugin; see outages.js for the line per
// device) and the messages about what went wrong (a report not mailed, a
// backup failed, a command of an automation that did not take effect, a
// login refused). Both can be confirmed as read. The icon in the header
// (inboxIcon) is only there while something is unread and opens this page.
import { state, subscribe } from "./app.js";
import { h, api, dates } from "./util.js";
import { duration } from "./outages.js";

const PLUGIN_PREFIX = "plugin:";
const LIST_SHOWN = 100;

/** The name of a HomeWizard or plugin device, by id. */
export function deviceName(id) {
  const key = id.startsWith(PLUGIN_PREFIX) ? id.slice(PLUGIN_PREFIX.length) : id;
  const d = state.devices.get(key);
  if (d) return d.name;
  const p = state.plugins.get(key);
  if (p) return `${p.name || p.address} (${p.plugin_name})`;
  return key;
}

/** Calls fn at most once per wait ms, on the trailing edge. */
function debounce(fn, wait) {
  let timer = null;
  return () => { clearTimeout(timer); timer = setTimeout(fn, wait); };
}

/** Keeps the header icon in step: shown with the count while something is unread. */
export function inboxIcon() {
  const link = document.getElementById("inbox");
  if (!link) return;
  const count = link.querySelector(".inbox-count");
  let seq = 0;
  const load = async () => {
    const n = ++seq;
    let res;
    try { res = await api("GET", "/api/inbox"); } catch { return; }
    if (n !== seq) return;
    link.hidden = !res.unread;
    count.textContent = res.unread > 99 ? "99+" : String(res.unread);
    const parts = [];
    if (res.outages) parts.push(`${res.outages} ${res.outages === 1 ? "storing" : "storingen"}`);
    if (res.notices) parts.push(`${res.notices} ${res.notices === 1 ? "bericht" : "berichten"}`);
    link.title = `Ongelezen: ${parts.join(" en ")}`;
    link.setAttribute("aria-label", link.title);
  };
  const later = debounce(load, 1000);
  subscribe((change) => {
    if (change.type === "outages" || change.type === "notices") later();
    else if (change.type === "snapshot") load(); // (re)connected
  });
}

function noticeItem(n) {
  const when = n.count > 1
    ? `${n.count}× · laatst ${dates.dayTime(new Date(n.last))} · eerst ${dates.dayTime(new Date(n.first))}`
    : dates.dayTime(new Date(n.last));
  const subject = n.kind === "control" && n.subject ? `${deviceName(n.subject)}: ` : "";
  return h("li", { class: n.read ? "inbox-item" : "inbox-item unread" },
    h("div", { class: "inbox-head" }, h("strong", {}, n.title), h("span", { class: "muted small" }, when)),
    h("div", { class: "small" }, subject + (n.text || "")));
}

/** The page #/berichten. */
export function renderBerichten(view) {
  let days = 30;
  let filter = ""; // a device id: only its outages in the list
  let showAll = false;
  let outages = null, notices = null;

  const readAll = h("button", { class: "btn primary", type: "button", disabled: true, onclick: () => markRead() }, "Alles als gelezen markeren");
  const period = h("select", { "aria-label": "Periode" },
    ...[7, 30, 90, 365].map((d) => h("option", { value: d, selected: d === days }, `Laatste ${d} dagen`)));
  period.addEventListener("change", () => { days = Number(period.value); load(); });
  const error = h("div", { class: "form-error", role: "alert", hidden: true });
  const noticeBox = h("section", { class: "card" });
  const outageBox = h("section", { class: "card" });
  view.append(
    h("div", { class: "chart-head" }, h("h2", { class: "section-title" }, "Berichten"), h("div", { class: "row" }, period, readAll)),
    error, noticeBox, outageBox);

  const draw = () => {
    const unread = (outages?.unread || 0) + (notices?.unread || 0);
    readAll.disabled = !unread;
    readAll.textContent = unread ? `Alles als gelezen markeren (${unread})` : "Alles gelezen";

    // messages about what went wrong
    const list = notices?.notices || [];
    noticeBox.replaceChildren(
      h("h3", {}, "Problemen"),
      list.length
        ? h("ul", { class: "inbox-list" }, ...list.map(noticeItem))
        : h("p", { class: "secondary" }, `Geen problemen in de laatste ${days} dagen.`));

    // the outages of every device, first per device
    const all = outages?.outages || [];
    const per = new Map();
    for (const o of all) {
      const p = per.get(o.device_id) || { id: o.device_id, count: 0, unread: 0, secs: 0, open: false };
      p.count++;
      if (!o.read) p.unread++;
      if (!o.end) p.open = true;
      const end = o.end ? new Date(o.end) : new Date();
      p.secs += Math.max(0, (end - new Date(o.start)) / 1000);
      per.set(o.device_id, p);
    }
    const devices = [...per.values()].sort((a, b) => b.count - a.count);
    const pick = (id) => { filter = filter === id ? "" : id; showAll = false; draw(); };
    const summary = devices.length ? h("div", { class: "outage-list" }, h("table", { class: "outage-table inbox-summary" },
      h("thead", {}, h("tr", {}, h("th", {}, "Apparaat"), h("th", { class: "num" }, "Storingen"), h("th", { class: "num" }, "Samen"), h("th", { class: "num" }, "Ongelezen"), h("th", {}, "Nu"))),
      h("tbody", {}, ...devices.map((p) => h("tr", { class: filter === p.id ? "selected" : "", onclick: () => pick(p.id), title: "Toon alleen dit apparaat" },
        h("td", {}, h("button", { type: "button", class: "link-btn" }, deviceName(p.id))),
        h("td", { class: "num" }, String(p.count)),
        h("td", { class: "num" }, duration(p.secs)),
        h("td", { class: "num" }, p.unread ? String(p.unread) : "–"),
        h("td", {}, p.open ? "niet bereikbaar" : ""))))))
      : h("p", { class: "secondary" }, `Alle apparaten waren bereikbaar in de laatste ${days} dagen.`);

    const rows = filter ? all.filter((o) => o.device_id === filter) : all;
    const shown = showAll ? rows : rows.slice(0, LIST_SHOWN);
    const more = rows.length > shown.length
      ? h("button", { type: "button", class: "link-btn", onclick: () => { showAll = true; draw(); } }, `Toon alle ${rows.length} storingen`)
      : null;
    const table = rows.length ? h("div", { class: "outage-list" }, h("table", { class: "outage-table" },
      h("thead", {}, h("tr", {}, h("th", {}, "Apparaat"), h("th", {}, "Vanaf"), h("th", {}, "Tot"), h("th", { class: "num" }, "Duur"), h("th", {}, "Melding"))),
      h("tbody", {}, ...shown.map((o) => {
        const start = new Date(o.start), end = o.end ? new Date(o.end) : null;
        return h("tr", { class: o.read ? "" : "unread" },
          h("td", {}, deviceName(o.device_id)),
          h("td", {}, dates.dayTime(start)),
          h("td", {}, end ? dates.dayTime(end) + (o.cut ? " (gateway stopte)" : "") : "nog steeds"),
          h("td", { class: "num" }, duration(((end || new Date()) - start) / 1000) + (o.cut ? " of langer" : "")),
          h("td", { class: "outage-msg" }, o.message || ""));
      })))) : null;

    // filtered: replaceChildren would print a null as text
    outageBox.replaceChildren(...[
      h("h3", {}, "Storingen van apparaten"),
      summary,
      filter ? h("p", { class: "small" }, `Alleen ${deviceName(filter)}. `, h("button", { type: "button", class: "link-btn", onclick: () => pick(filter) }, "Toon alle apparaten")) : null,
      table, more,
      h("p", { class: "hint" }, "Een apparaat telt als niet bereikbaar na drie mislukte pogingen achter elkaar. Een herstart of stop van de gateway telt niet als storing. Vetgedrukt is nog niet gelezen."),
    ].filter(Boolean));
  };

  let seq = 0;
  const load = async () => {
    const n = ++seq;
    try {
      const [o, b] = await Promise.all([
        api("GET", `/api/outages?days=${days}`),
        api("GET", `/api/notices?days=${days}`),
      ]);
      if (n !== seq) return;
      outages = o; notices = b;
      error.hidden = true;
      draw();
    } catch (e) {
      if (n !== seq) return;
      error.textContent = `Berichten niet geladen: ${e.message}`;
      error.hidden = false;
    }
  };

  // read up to the moment of the lists shown: what came in since stays unread
  const markRead = async () => {
    readAll.disabled = true;
    try {
      await Promise.all([
        outages?.unread ? api("POST", "/api/outages/read", { until: outages.to }) : null,
        notices?.unread ? api("POST", "/api/notices/read", { until: notices.to }) : null,
      ]);
    } catch (e) {
      error.textContent = `Niet gelukt: ${e.message}`;
      error.hidden = false;
    }
    load();
  };

  const later = debounce(load, 1000);
  const unsubscribe = subscribe((change) => {
    if (change.type === "outages" || change.type === "notices") later();
    else if (change.type === "snapshot" || change.type === "plugins") draw(); // names
  });
  load();
  return unsubscribe;
}
