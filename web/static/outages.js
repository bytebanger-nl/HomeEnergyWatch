// How often a device (HomeWizard or plugin) did not respond: one line per
// device with the number and total duration in the last 30 days, which opens
// a list with the time, duration and message of each outage. Shared by the
// device list (devices.js) and the plugin devices (plugins.js). A stop of the
// gateway is not counted (see internal/outage).
import { h, api, dates } from "./util.js";

const DAYS = 30;

/** "45 s", "12 min", "2 u 5 min", "3 d 4 u". */
export function duration(seconds) {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s} s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min`;
  const hours = Math.floor(m / 60), min = m % 60;
  if (hours < 24) return min ? `${hours} u ${min} min` : `${hours} u`;
  const d = Math.floor(hours / 24), hr = hours % 24;
  return hr ? `${d} d ${hr} u` : `${d} d`;
}

function row(o) {
  const start = new Date(o.start);
  const end = o.end ? new Date(o.end) : null;
  const secs = ((end || new Date()) - start) / 1000;
  let until = end ? dates.dayTime(end) : "nog steeds";
  if (o.cut) until += " (gateway stopte)";
  return h("tr", {},
    h("td", {}, dates.dayTime(start)),
    h("td", {}, until),
    h("td", { class: "num" }, duration(secs) + (o.cut ? " of langer" : "")),
    h("td", { class: "outage-msg" }, o.message || ""));
}

/**
 * The outage line of a device: "Niet bereikbaar: 3× in 30 dagen, samen
 * 14 min", a click shows the list. Loads once when made; refresh() loads
 * again (e.g. after the device went offline or came back).
 */
export function outageLine(deviceId) {
  const summary = h("summary", {}, "Storingen laden…");
  const body = h("div", { class: "outage-list" });
  const node = h("details", { class: "outages small" }, summary, body);
  let seq = 0;
  const refresh = async () => {
    const n = ++seq;
    let res;
    try {
      res = await api("GET", `/api/outages?device=${encodeURIComponent(deviceId)}&days=${DAYS}`);
    } catch {
      if (n === seq) node.hidden = true;
      return;
    }
    if (n !== seq) return;
    node.hidden = false;
    if (!res.count) {
      summary.textContent = `Altijd bereikbaar in de afgelopen ${DAYS} dagen`;
      node.classList.add("none");
      body.replaceChildren();
      return;
    }
    node.classList.remove("none");
    summary.textContent = `Niet bereikbaar: ${res.count}× in ${DAYS} dagen, samen ${duration(res.total_s)}`;
    body.replaceChildren(h("table", { class: "outage-table" },
      h("thead", {}, h("tr", {}, h("th", {}, "Vanaf"), h("th", {}, "Tot"), h("th", { class: "num" }, "Duur"), h("th", {}, "Melding"))),
      h("tbody", {}, ...res.outages.map(row))),
    h("p", { class: "hint" }, "Een herstart of stop van de gateway telt niet als storing. Pas na drie mislukte pogingen achter elkaar telt een apparaat als niet bereikbaar."));
  };
  refresh();
  return { node, refresh };
}
