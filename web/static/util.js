// Shared helpers: DOM building, formatting and the API client.

/** h("div", {class: "x", onclick: fn}, child, "text") */
export function h(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") node.className = v;
    else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
    else if (k === "text") node.textContent = v;
    else if (v === true) node.setAttribute(k, "");
    else node.setAttribute(k, v);
  }
  for (const c of children.flat()) {
    if (c == null || c === false) continue;
    node.appendChild(typeof c === "string" || typeof c === "number" ? document.createTextNode(String(c)) : c);
  }
  return node;
}

const nf = (min, max) => new Intl.NumberFormat("nl-NL", { minimumFractionDigits: min, maximumFractionDigits: max });
const nf0 = nf(0, 0), nf1 = nf(1, 1), nf2 = nf(2, 2), nf3 = nf(3, 3);

/** Number plus unit, split so the unit can be styled smaller. */
export function power(w) {
  if (w == null || Number.isNaN(w)) return ["–", "W"];
  if (Math.abs(w) >= 10000) return [nf1.format(w / 1000), "kW"];
  return [nf0.format(w), "W"];
}
export function energy(kwh) {
  if (kwh == null) return ["–", "kWh"];
  const a = Math.abs(kwh);
  return [a < 10 ? nf2.format(kwh) : a < 1000 ? nf1.format(kwh) : nf0.format(kwh), "kWh"];
}
export function gas(m3) {
  if (m3 == null) return ["–", "m³"];
  return [Math.abs(m3) < 100 ? nf3.format(m3) : nf1.format(m3), "m³"];
}
export function liters(l) {
  if (l == null) return ["–", "L"];
  return [Math.abs(l) < 100 ? nf1.format(l) : nf0.format(l), "L"];
}
export function flow(lpm) {
  if (lpm == null) return ["–", "L/min"];
  return [nf1.format(lpm), "L/min"];
}
export const fmt = {
  n0: (v) => nf0.format(v), n1: (v) => nf1.format(v), n2: (v) => nf2.format(v), n3: (v) => nf3.format(v),
};
export const join = ([v, u]) => `${v} ${u}`;

const eurFmt = new Intl.NumberFormat("nl-NL", { style: "currency", currency: "EUR" });
const nfUpTo1 = nf(0, 1);
/**
 * A plugin metric's number and unit, split like power(): euros as
 * "€ 1.537,60" (the sign in front, no unit after), temperatures always with
 * one decimal, anything else with at most one ("261 eenheden", "55,5 %").
 */
export function metricParts(v, unit) {
  if (unit === "€") return [eurFmt.format(v), ""];
  return [(unit === "°C" ? nf1 : nfUpTo1).format(v), unit || ""];
}
/** metricParts as one text: "€ 1.537,60", "21,5 °C". */
export function metricText(v, unit) {
  const [n, u] = metricParts(v, unit);
  return u ? `${n} ${u}` : n;
}

/** <span>value<small>unit</small></span> */
export function valueUnit([v, u]) {
  return [document.createTextNode(v), h("small", {}, u)];
}

const timeFmt = new Intl.DateTimeFormat("nl-NL", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const timeShort = new Intl.DateTimeFormat("nl-NL", { hour: "2-digit", minute: "2-digit" });
const dayFmt = new Intl.DateTimeFormat("nl-NL", { weekday: "short", day: "numeric", month: "short" });
const monthFmt = new Intl.DateTimeFormat("nl-NL", { month: "long", year: "numeric" });
export const dates = {
  time: (d) => timeFmt.format(d),
  short: (d) => timeShort.format(d),
  day: (d) => dayFmt.format(d),
  month: (d) => monthFmt.format(d),
  dayTime: (d) => `${dayFmt.format(d)} ${timeShort.format(d)}`,
};

export function ago(iso) {
  if (!iso) return "nooit";
  const s = Math.round((Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 5) return "zojuist";
  if (s < 60) return `${s} s geleden`;
  if (s < 3600) return `${Math.round(s / 60)} min geleden`;
  if (s < 86400) return `${Math.round(s / 3600)} uur geleden`;
  return dates.dayTime(new Date(iso));
}

export const STATUS_LABEL = {
  connecting: "Verbinden",
  online: "Online",
  offline: "Offline",
  disabled: "Uitgeschakeld",
  needs_pairing: "Koppelen nodig",
  pairing: "Koppelen…",
  api_disabled: "API uit",
};

export function statusPill(status) {
  return h("span", { class: "status", "data-status": status }, STATUS_LABEL[status] || status);
}

/**
 * The on/off switch of a socket on the dashboard, also used for every on/off
 * control of a plugin device, so they look and work the same. onchange gets
 * the new state; set() redraws it (text: "Aan"/"Uit" unless given).
 */
export function onOffSwitch(ariaLabel, onchange) {
  const input = h("input", { type: "checkbox", role: "switch", "aria-label": ariaLabel || "Aan/uit" });
  const text = h("span", { class: "switch-text" }, "Uit");
  const node = h("label", { class: "switch" }, input, text);
  input.addEventListener("change", () => onchange(input.checked));
  return {
    node,
    input,
    set(on, { disabled = false, label } = {}) {
      input.checked = !!on;
      input.disabled = disabled;
      text.textContent = label || (on ? "Aan" : "Uit");
    },
  };
}

/**
 * The tone of a plugin metric shown as a state, the same on the dashboard
 * and on Klimaat: on/off (bool), heating/cooling/off (a mode), and tado's
 * heat demand (key "heating", %): idle at 0, warm above. "" for no tone.
 */
export function metricTone(m) {
  if (!m) return "";
  if (m.kind === "bool") return m.value ? "on" : "off";
  if (m.kind === "number") {
    if (m.key === "heating" && m.unit === "%" && typeof m.value === "number") return m.value > 0 ? "warm" : "idle";
    return "";
  }
  const v = String(m.value).toLowerCase().trim();
  // a mode, not e.g. a fan speed that can also be "Automatisch"
  const isMode = m.key === "mode" || /^(modus|mode)$/i.test(m.label || "");
  // a radiator heating by its timer or programme, and an air conditioner on
  // auto, look like heating
  if (/verwarm|heat|^hot$|timer|programma|program/.test(v)) return "warm";
  if (isMode && /^(automatisch|auto)$/.test(v)) return "warm";
  if (/koel|cool|ontvocht|^dry$/.test(v)) return "cool";
  // eco and fan only: orange
  if (/^(eco|ventileren|fan)$/.test(v)) return "eco";
  // tado's schedule and manual setting are both "on", like "Aan"
  if (/^(aan|on|schema|handmatig|manual)$/.test(v)) return "on";
  if (/^(uit|off)$/.test(v)) return "off";
  return "";
}

/** A state chip in its tone (metricTone); class tone-chip. */
export function toneChip(text, tone) {
  return h("span", { class: `tone-chip${tone ? " " + tone : ""}` }, text);
}

export async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  if (res.status === 204) return null;
  let data = null;
  try { data = await res.json(); } catch { /* not JSON */ }
  // the session ended (logged out elsewhere, new password): app.js shows the login
  if (res.status === 401 && data && data.login) window.dispatchEvent(new Event("hw:login"));
  if (!res.ok) throw new Error((data && data.error) || `Fout ${res.status}`);
  return data;
}

export const val = (device, key) => device?.reading?.values?.[key];
export const today = (device, key) => device?.today?.[key];
