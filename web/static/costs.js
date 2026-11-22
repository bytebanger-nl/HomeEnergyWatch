// Costs panel of "Verbruik en Kosten": costs per hour, day, month or year,
// and the contracts they are calculated with. Energy and water have their
// own contracts and the same layout.
import { columnChart } from "./charts.js";
import { state } from "./app.js";
import { h, api, energy, gas, liters, dates } from "./util.js";

const eurFmt = new Intl.NumberFormat("nl-NL", { style: "currency", currency: "EUR" });
export const eur = (v) => (v == null ? "–" : eurFmt.format(v));
const axisFmt = new Intl.NumberFormat("nl-NL", { maximumFractionDigits: 2 });
const ctFmt = new Intl.NumberFormat("nl-NL", { maximumFractionDigits: 3 });
const monthName = new Intl.DateTimeFormat("nl-NL", { month: "short" });
const longDate = new Intl.DateTimeFormat("nl-NL", { day: "numeric", month: "long", year: "numeric" });
const dayMonth = new Intl.DateTimeFormat("nl-NL", { day: "numeric", month: "long" });

const FIXED_SUGGESTIONS = [
  "Vaste leveringskosten stroom",
  "Vaste leveringskosten gas",
  "Netbeheerkosten stroom",
  "Netbeheerkosten gas",
  "Vermindering energiebelasting",
];

const WATER_SUGGESTIONS = ["Vastrecht"];

// The two cost timelines: energy (P1 meter, electricity and gas) and water
// (water meter). Each has its own contracts under its own API prefix.
const KINDS = {
  energy: {
    id: "energy", api: "/api", label: "Energie",
    noContract: "Voeg hieronder je energiecontract toe; dan worden de kosten berekend uit je verbruik en de marktprijzen.",
  },
  water: {
    id: "water", api: "/api/water", label: "Water",
    noContract: "Voeg hieronder je watercontract toe, met de tarieven van je jaarafrekening of van de site van je waterbedrijf; dan worden de kosten berekend uit het verbruik van je watermeter.",
  },
};

const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const parseDate = (s) => { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d); };

function bucketTitle(bucket, start) {
  const d = new Date(start);
  if (bucket === "hour") return `${dates.day(d)} ${dates.short(d)}–${dates.short(new Date(d.getTime() + 3600e3))}`;
  if (bucket === "day") return dates.day(d);
  if (bucket === "month") return dates.month(d);
  return String(d.getFullYear());
}

function bucketTick(bucket, start) {
  const d = new Date(start);
  if (bucket === "hour") return dates.short(d);
  if (bucket === "day") return String(d.getDate());
  if (bucket === "month") return monthName.format(d).replace(".", "");
  return String(d.getFullYear());
}

/** The contract type that applies to gas: its own on a contract that splits electricity and gas. */
const gasTypeOf = (c) => (c.split ? c.gas_type : c.type);

/** "Dynamisch", "Vast/variabel", or per part: "Stroom dynamisch · gas vast/variabel". */
function typeText(c) {
  const t = (type) => (type === "dynamic" ? "dynamisch" : "vast/variabel");
  if (c.split) return `Stroom ${t(c.type)} · gas ${t(c.gas_type)}`;
  return c.type === "dynamic" ? "Dynamisch" : "Vast/variabel";
}

/** The tariff lines of an energy contract or scenario: electricity and gas, each by its own type. */
function tariffLines(c, ct) {
  const e = c.electricity || {}, g = c.gas || {};
  const lines = [];
  if (c.type === "dynamic") {
    lines.push(`Stroom: marktprijs ${e.hourly ? "per uur" : "per kwartier"} + ${ct(e.markup_ct, "kWh")} opslag + ${ct(e.tax_ct, "kWh")} belasting`);
  } else {
    const rate = e.low_ct || e.normal_ct ? `${ct(e.normal_ct, "kWh")} normaal, ${ct(e.low_ct, "kWh")} dal` : ct(e.rate_ct, "kWh");
    lines.push(`Stroom: ${rate} + ${ct(e.tax_ct, "kWh")} belasting`);
  }
  if (gasTypeOf(c) === "dynamic") {
    if (g.markup_ct || g.tax_ct) lines.push(`Gas: dagprijs + ${ct(g.markup_ct, "m³")} opslag + ${ct(g.tax_ct, "m³")} belasting`);
  } else if (g.rate_ct || g.tax_ct) {
    lines.push(`Gas: ${ct(g.rate_ct, "m³")} + ${ct(g.tax_ct, "m³")} belasting`);
  }
  return lines;
}

/** A copy of contract c starting today; the advance continues with its last amount from then. */
function nextPeriod(c) {
  const start = iso(new Date());
  const copy = { ...structuredClone(c), id: 0, start, end: "", continues: true };
  for (const key of ["advance", "gas_advance"]) {
    const amounts = c[key]?.amounts || [];
    const last = amounts[amounts.length - 1];
    copy[key] = last ? { amounts: [{ from: nextPaymentOnOrAfter(last.from, start), eur: last.eur }] } : null;
  }
  return copy;
}

/** The first date on or after start in the monthly series of payments from date from (same day of the month; the 31st becomes the last day of a shorter month). */
function nextPaymentOnOrAfter(from, start) {
  const first = parseDate(from);
  for (let n = 0; n < 1200; n++) {
    const last = new Date(first.getFullYear(), first.getMonth() + n + 1, 0).getDate();
    const d = iso(new Date(first.getFullYear(), first.getMonth() + n, Math.min(first.getDate(), last)));
    if (d >= start) return d;
  }
  return start;
}

export const electricity = (b) => b.electricity_eur + b.electricity_tax_eur;
export const gasCost = (b) => b.gas_eur + b.gas_tax_eur;
export const credit = (b) => b.export_eur + b.netting_eur;
export const waterCost = (b) => b.water_eur + b.water_tax_eur;

/**
 * The costs panel of "Verbruik en Kosten" for one timeline (energy or
 * water): totals, the costs chart with its table, the advance, the
 * contracts and (energy) the what-if scenarios. The tab (usage.js) chooses
 * the device and the period: load(device, p) with device "" for the whole
 * connection (or the water meter) or a socket or kWh meter, and p a period
 * ({from, to, costBucket}). onContracts(fn) tells the tab the contracts
 * whenever they change, for its contract periods.
 */
export function costsPanel(kindId) {
  const k = KINDS[kindId] || KINDS.energy;
  const water = k.id === "water";
  const ui = { contracts: [], device: "", p: null };
  let listener = null;

  const notice = h("div", { class: "notice", hidden: true });
  const summary = h("section", { class: "summary", "aria-label": "Totalen" });
  // what the "Onvolledig" tile counts, per period (opened from that tile)
  const gapsBox = h("section", { class: "card gaps-card", hidden: true, "aria-label": "Wat ontbreekt er" });
  let onDay = null;
  const advance = h("section", { class: "card advance-card", hidden: true, "aria-label": "Voorschot" });
  const card = chartCard();
  const contractList = h("div", { class: "contract-list" });
  const addBtn = h("button", { class: "btn primary", type: "button" }, "Contract toevoegen");
  const formSlot = h("div");
  const contractsTitle = h("h2", { class: "section-title" }, water ? "Watercontracten" : "Contracten");
  const contractsHelp = h("p", { class: "secondary contract-help" }, "Bedragen inclusief btw. Een contract loopt tot het volgende begint, of tot de einddatum. Vaste kosten per maand tellen per dag mee (× 12 / 365) vanaf de begindatum."
    + (water ? " Verandert alleen het tarief (meestal per 1 januari), maak dan een nieuwe periode als vervolg op het vorige contract." : ""));
  const node = h("div", { class: "usage-panel" },
    notice, summary, gapsBox, card.node, advance,
    contractsTitle, contractsHelp, contractList, h("div", { class: "row" }, addBtn), formSlot);
  // what-if scenarios are for energy only: you cannot choose your water company
  const compare = water ? null : compareSection(() => ui.contracts);
  if (compare) node.append(compare.node);
  addBtn.addEventListener("click", () => openForm(null));

  let seq = 0, gapsSeq = 0;
  const gapsQuery = () => {
    const scope = ui.device ? `&device=${encodeURIComponent(ui.device)}&scope=usage` : "";
    return `${k.api}/costs/gaps?from=${ui.p.from.toISOString()}&to=${ui.p.to.toISOString()}${scope}`;
  };
  async function loadGaps() {
    const my = ++gapsSeq;
    gapsBox.replaceChildren(h("p", { class: "secondary" }, "Ophalen wat er ontbreekt…"));
    let res;
    try {
      res = (await api("GET", gapsQuery())).result;
    } catch (e) {
      if (my === gapsSeq) gapsBox.replaceChildren(h("div", { class: "form-error" }, e.message));
      return;
    }
    if (my !== gapsSeq || gapsBox.hidden) return;
    renderGaps(gapsBox, res, { water, device: !!ui.device, close: () => toggleGaps(false), onDay });
  }
  /** Opens or closes the details of the "Onvolledig" tile. */
  function toggleGaps(open = gapsBox.hidden) {
    gapsBox.hidden = !open;
    for (const b of summary.querySelectorAll(".incomplete-tile")) b.setAttribute("aria-expanded", String(open));
    if (open) {
      loadGaps();
      gapsBox.scrollIntoView({ behavior: "smooth", block: "nearest" });
    } else {
      gapsSeq++;
    }
  }

  async function load(device, p) {
    ui.device = device || "";
    ui.p = p;
    const pc = { ...p, bucket: p.costBucket };
    const my = ++seq;
    card.chart.classList.add("loading");
    let res;
    try {
      const scope = ui.device ? `&device=${encodeURIComponent(ui.device)}&scope=usage` : "";
      res = await api("GET", `${k.api}/costs?from=${pc.from.toISOString()}&to=${pc.to.toISOString()}&bucket=${pc.bucket}${scope}`);
    } catch (e) {
      res = { error: e.message };
    }
    if (my !== seq) return;
    card.chart.classList.remove("loading");
    if (res.error) {
      notice.hidden = false;
      notice.replaceChildren(h("h2", {}, "Kosten niet beschikbaar"), h("p", {}, res.error));
      summary.hidden = card.node.hidden = true;
      return;
    }
    notice.hidden = !!ui.contracts.length;
    if (!ui.contracts.length) notice.replaceChildren(h("h2", {}, water ? "Nog geen watercontract" : "Nog geen contract"), h("p", {}, k.noContract));
    summary.hidden = card.node.hidden = false;
    // the advance and the scenarios belong to the whole connection
    advance.classList.toggle("off", !!ui.device);
    if (compare) compare.node.hidden = !!ui.device;
    // a new period or device: open details follow it
    if (!gapsBox.hidden) {
      if (res.result.total.complete) toggleGaps(false); else loadGaps();
    }
    if (ui.device) {
      const d = state.devices.get(ui.device);
      renderUsageSummary(summary, res.result, toggleGaps);
      renderUsageChart(card, pc, res.result, d?.name || ui.device);
      return;
    }
    renderSummary(summary, res.result, water, toggleGaps);
    renderChart(card, pc, res.result, water);
  }
  const reload = () => { if (ui.p) load(ui.device, ui.p); };

  async function loadContracts() {
    try {
      ui.contracts = (await api("GET", `${k.api}/contracts`)).contracts;
    } catch (e) {
      contractList.replaceChildren(h("div", { class: "form-error" }, e.message));
      return;
    }
    renderContracts();
    loadAdvance();
    compare?.load();
    listener?.(ui.contracts);
  }

  async function loadAdvance() {
    try {
      const res = await api("GET", `${k.api}/advance`);
      renderAdvance(advance, res.advances || (res.advance ? [res.advance] : []), water);
    } catch {
      advance.hidden = true;
    }
  }

  function renderContracts() {
    const cs = ui.contracts;
    if (!cs.length) {
      contractList.replaceChildren(h("div", { class: "notice" }, "Nog geen contracten."));
      return;
    }
    // newest on top; the cards still look at their neighbours in time
    contractList.replaceChildren(...cs.map((c, i) => contractCard(c, cs[i + 1], cs[i - 1])).reverse());
  }

  function contractCard(c, following, previous) {
    let until = "doorlopend";
    if (c.end) until = `t/m ${longDate.format(parseDate(c.end))}`;
    else if (following) until = `t/m ${longDate.format(new Date(parseDate(following.start).getTime() - 86400e3))}`;
    const e = c.electricity || {}, w = c.water || {};
    const ct = (v, unit) => `${ctFmt.format(v)} ct/${unit}`;
    const lines = [];
    if (water) {
      lines.push(`Water: ${ct(w.rate_ct, "m³")}${w.tax_ct ? ` + ${ct(w.tax_ct, "m³")} belasting op leidingwater` : ""}`);
    } else {
      lines.push(...tariffLines(c, ct));
    }
    if (!water && e.export_ct) lines.push(`Teruglevering: ${c.type === "dynamic" ? "marktprijs " + (e.export_ct > 0 ? "+ " : "− ") + ct(Math.abs(e.export_ct), "kWh") : ct(e.export_ct, "kWh")}`);
    const sum = (ms) => ms.reduce((s, m) => s + m.eur, 0);
    const ms = c.monthly || [];
    if (c.split && ms.length) {
      const gasMs = ms.filter((m) => m.part === "gas"), elMs = ms.filter((m) => m.part !== "gas");
      lines.push(`Vaste kosten: stroom ${eur(sum(elMs))}, gas ${eur(sum(gasMs))} per maand (${ms.length} posten)`);
    } else if (ms.length) {
      lines.push(`Vaste kosten: ${eur(sum(ms))} per maand (${ms.length} posten)`);
    }
    const advanceLine = (label, adv) => {
      const amounts = adv?.amounts || [];
      if (!amounts.length) return null;
      return `${label}: ${amounts.map((a) => `${eur(a.eur)} per maand${amounts.length > 1 ? ` vanaf ${longDate.format(parseDate(a.from))}` : ""}`).join(", ")}`;
    };
    if (c.split) {
      lines.push(advanceLine("Voorschot stroom", c.advance) || "Geen voorschot voor stroom: kosten worden achteraf berekend");
      lines.push(advanceLine("Voorschot gas", c.gas_advance) || "Geen voorschot voor gas: kosten worden achteraf berekend");
    } else {
      lines.push(advanceLine("Voorschot", c.advance) || (water ? "Geen voorschot" : "Geen voorschot: kosten worden achteraf berekend"));
    }
    const edit = h("button", { class: "btn", type: "button", onclick: () => openForm(c) }, "Bewerken");
    const copy = h("button", { class: "btn", type: "button", onclick: () => openForm(nextPeriod(c)) }, "Nieuwe periode");
    const del = h("button", { class: "btn danger", type: "button" }, "Verwijderen");
    del.addEventListener("click", async () => {
      if (!confirm(`Contract "${c.name}" verwijderen?`)) return;
      try {
        await api("DELETE", `${k.api}/contracts/${c.id}`);
        await loadContracts();
        reload();
      } catch (err) { alert(err.message); }
    });
    return h("section", { class: "card contract" },
      h("div", { class: "contract-head" },
        h("div", {}, h("h3", {}, c.name), h("div", { class: "secondary" }, `${water ? "Water" : typeText(c)} · vanaf ${longDate.format(parseDate(c.start))} ${until}${c.continues && previous ? ` · vervolg op ${previous.name} (tariefwijziging)` : ""}`)),
        h("div", { class: "row" }, copy, edit, del)),
      h("ul", { class: "contract-lines" }, ...lines.map((l) => h("li", {}, l))));
  }

  function openForm(c) {
    const last = ui.contracts[ui.contracts.length - 1];
    const blank = water
      ? { id: 0, name: "", type: "water", start: iso(new Date()), end: "", water: {}, monthly: WATER_SUGGESTIONS.map((name) => ({ name, eur: 0 })) }
      : { id: 0, name: "", type: "dynamic", start: iso(new Date()), end: "", vat_pct: 21, electricity: {}, gas: {}, monthly: FIXED_SUGGESTIONS.map((name) => ({ name, eur: 0 })) };
    const initial = c || (last ? { ...nextPeriod(last), continues: false } : blank);
    const form = contractForm(initial, async () => {
      formSlot.replaceChildren();
      await loadContracts();
      reload();
    }, () => formSlot.replaceChildren(), { water, api: k.api });
    formSlot.replaceChildren(form);
    form.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  const ready = loadContracts();
  return {
    node,
    load,
    ready,
    contracts: () => ui.contracts,
    onContracts(fn) { listener = fn; },
    /** fn(date) shows the usage of one day (a link in the details of what is missing). */
    onDay(fn) { onDay = fn; },
    /** Shows or hides the contracts, advance and scenarios (only with the whole connection). */
    setExtras(on) {
      for (const n of [contractsTitle, contractsHelp, contractList, addBtn.parentNode, formSlot]) n.hidden = !on;
    },
  };
}

export function chartCard() {
  const title = h("h3", {}, "Kosten");
  const sub = h("div", { class: "chart-sub" });
  const legend = h("div", { class: "legend" });
  const chart = h("div", { class: "chart" });
  const toggle = h("button", { class: "table-toggle", type: "button" }, "Toon als tabel");
  const table = h("div", { class: "table-wrap", hidden: true });
  toggle.addEventListener("click", () => {
    table.hidden = !table.hidden;
    toggle.textContent = table.hidden ? "Toon als tabel" : "Verberg tabel";
  });
  const node = h("section", { class: "card chart-card" },
    h("div", { class: "chart-head" }, h("div", {}, title, sub), legend), chart, toggle, table);
  return { node, title, sub, legend, chart, table };
}

export function legendItem(color, text) {
  const key = h("span", { class: "key" });
  key.style.background = color;
  return h("span", {}, key, text);
}

export function tile(label, value, hint, cls) {
  return h("div", { class: "card tile" },
    h("div", { class: "label" }, cls ? h("span", { class: `key ${cls}` }) : null, label),
    h("div", { class: "value" }, value),
    hint ? h("div", { class: "hint" }, hint) : null);
}

function missingText(m) {
  const parts = [];
  if (m.contract_days) parts.push(`${m.contract_days} ${m.contract_days === 1 ? "dag" : "dagen"} zonder contract`);
  if (m.quarters) parts.push(`${m.quarters} kwartieren zonder meting (niet meegeteld)`);
  if (m.partial) parts.push(`${m.partial} kwartieren gedeeltelijk gemeten (geschat)`);
  if (m.anomalous) parts.push(`${m.anomalous} kwartieren met een meterafwijking, zoals een reset (niet meegeteld)`);
  if (m.prices) parts.push(`${m.prices} kwartieren zonder stroomprijs (niet meegeteld)`);
  if (m.gas_prices) parts.push(`${m.gas_prices} kwartieren zonder gasprijs (niet meegeteld)`);
  return parts.join(", ");
}

const litersText = (m3) => liters(m3 * 1000).join(" ");

/** The "Onvolledig" tile: what is missing in short; a click opens the details. */
function incompleteTile(missing, onGaps) {
  return h("button", { type: "button", class: "card tile incomplete-tile", "aria-expanded": "false", onclick: () => onGaps() },
    h("div", { class: "label" }, "Onvolledig"),
    h("div", { class: "hint" }, `${missingText(missing)}. De bedragen zijn daardoor onvolledig of deels geschat.`),
    h("div", { class: "more" }, "Wat ontbreekt er? ›"));
}

const nQuarters = (n) => `${n.toLocaleString("nl-NL")} ${n === 1 ? "kwartier" : "kwartieren"}${n >= 8 && n % 4 === 0 ? ` (${(n / 4).toLocaleString("nl-NL")} uur)` : ""}`;
const nDays = (n) => `${n.toLocaleString("nl-NL")} ${n === 1 ? "dag" : "dagen"}`;
const sameDay = (a, b) => a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();

/** "di 14 apr 00:00–15:00", up to midnight "–24:00", across days with both days. */
function gapRange(g) {
  const from = new Date(g.from), to = new Date(g.to);
  if (g.kind === "contract") {
    const last = new Date(to.getTime() - 1);
    return sameDay(from, last) ? dates.day(from) : `${dates.day(from)} – ${dates.day(last)}`;
  }
  const last = new Date(to.getTime() - 1); // the last moment of the period
  if (sameDay(from, last)) {
    const end = sameDay(from, to) ? dates.short(to) : "24:00";
    return `${dates.day(from)} ${dates.short(from)}–${end}`;
  }
  return `${dates.dayTime(from)} – ${dates.dayTime(to)}`;
}

/** What each kind of gap means and what to do about it. */
function gapKinds(o) {
  const what = o.water ? "de watermeter" : o.device ? "dit apparaat" : "de P1-meter";
  return {
    quarters: {
      title: "Geen meting",
      text: `Van ${what} zijn hier geen meterstanden. Dit verbruik telt niet mee in de bedragen.`,
      todo: "Liep de gateway toen nog niet, importeer dan een export uit de HomeWizard Energy-app (Instellingen › Historie importeren). Was het apparaat offline, kijk dan of het stroom en wifi had.",
    },
    partial: {
      title: "Gedeeltelijk gemeten",
      text: "Een meterstand op de grens van het kwartier ontbrak, bijvoorbeeld omdat de gateway net opstartte. Het verbruik is geschat uit de standen die er wel zijn; het bedrag klopt bij benadering.",
      todo: "Meestal niet nodig. Een import uit de HomeWizard Energy-app vervangt deze kwartieren door gemeten waarden.",
    },
    anomalous: {
      title: "Meterafwijking",
      text: "De meterstand liep terug, bijvoorbeeld door een nieuwe of gereset meter. Dit kwartier telt niet mee.",
      todo: "Een import uit de HomeWizard Energy-app kan deze kwartieren aanvullen.",
    },
    prices: {
      title: "Geen stroomprijs",
      text: "Kwartieren met verbruik onder een dynamisch contract, maar zonder marktprijs. Ze tellen niet mee in de bedragen.",
      todo: "De gateway haalt ontbrekende dagen elke dag opnieuw op. Blijft dit staan, kijk dan bij Instellingen › Prijsbronnen.",
    },
    gas_prices: {
      title: "Geen gasprijs",
      text: "Kwartieren met gasverbruik onder een dynamisch contract, maar zonder gasprijs. Ze tellen niet mee in de bedragen.",
      todo: "De gateway haalt ontbrekende dagen elke dag opnieuw op.",
    },
    contract: {
      title: "Geen contract",
      text: "Dagen die onder geen enkel contract vallen. Daar zijn geen kosten berekend, ook geen vaste kosten.",
      todo: "Voeg hieronder een contract toe, of pas de begin- of einddatum van een contract aan.",
    },
  };
}

const GAPS_SHOWN = 10;

/** The details of what is missing: per kind the periods, what it means and what to do. */
function renderGaps(node, res, o) {
  const kinds = gapKinds(o);
  const close = h("button", { type: "button", class: "btn", onclick: o.close }, "Sluiten");
  const head = h("div", { class: "chart-head" }, h("h3", {}, "Wat ontbreekt er?"), close);
  const groups = [];
  for (const [kind, info] of Object.entries(kinds)) {
    const gaps = res.gaps.filter((g) => g.kind === kind);
    if (!gaps.length) continue;
    const total = gaps.reduce((s, g) => s + g.count, 0);
    const list = h("ul", { class: "gap-list" });
    const item = (g) => {
      const range = gapRange(g);
      const label = kind === "contract" ? nDays(g.count) : nQuarters(g.count);
      // a link to the usage of that day, for gaps within one day
      const oneDay = kind !== "contract" && sameDay(new Date(g.from), new Date(new Date(g.to).getTime() - 1));
      const when = o.onDay && oneDay
        ? h("button", { type: "button", class: "link-btn", title: "Toon het verbruik van die dag", onclick: () => o.onDay(new Date(g.from)) }, range)
        : h("span", {}, range);
      return h("li", {}, when, h("span", { class: "muted" }, ` · ${label}`));
    };
    const show = (n) => list.replaceChildren(...gaps.slice(0, n).map(item));
    show(GAPS_SHOWN);
    const more = gaps.length > GAPS_SHOWN
      ? h("button", { type: "button", class: "link-btn", onclick: (ev) => { show(gaps.length); ev.target.remove(); } }, `Toon alle ${gaps.length} perioden`)
      : null;
    groups.push(h("div", { class: "gap-group" },
      h("h4", {}, `${info.title}: ${kind === "contract" ? nDays(total) : nQuarters(total)}`),
      h("p", { class: "secondary" }, info.text),
      list, more,
      h("p", { class: "gap-todo" }, h("b", {}, "Wat kun je doen? "), info.todo)));
  }
  if (!groups.length) groups.push(h("p", { class: "secondary" }, "Er ontbreekt niets (meer) in deze periode."));
  const notes = [];
  if (res.measured_from) {
    const start = new Date(res.measured_from);
    notes.push(h("p", { class: "muted" },
      `De metingen beginnen op ${dates.dayTime(start)}; daarvoor telt niets als ontbrekend. Wil je eerdere kosten zien, importeer dan een export uit de HomeWizard Energy-app (Instellingen › Historie importeren).`));
  }
  if (res.truncated) notes.push(h("p", { class: "muted" }, "Alleen de eerste perioden worden getoond."));
  node.replaceChildren(head, ...groups, ...notes);
}

function renderSummary(node, r, water, onGaps) {
  const t = r.total;
  if (water) {
    const tiles = [
      tile("Totaal", eur(t.total_eur), `${litersText(t.water_m3)} water`),
      tile("Water", eur(waterCost(t)), `waarvan ${eur(t.water_tax_eur)} belasting op leidingwater`, "gas"),
      tile("Vaste kosten", eur(t.fixed_eur), "per dag naar rato", "fixed"),
    ];
    if (!t.complete) tiles.push(incompleteTile(t.missing, onGaps));
    node.replaceChildren(...tiles);
    return;
  }
  const tiles = [
    tile("Totaal", eur(t.total_eur), `${energy(t.import_kwh).join(" ")} afname${t.gas_m3 ? `, ${gas(t.gas_m3).join(" ")} gas` : ""}`),
    tile("Stroom", eur(electricity(t)), `waarvan ${eur(t.electricity_tax_eur)} energiebelasting`, "import"),
  ];
  if (t.gas_m3 || t.gas_eur) tiles.push(tile("Gas", eur(gasCost(t)), `waarvan ${eur(t.gas_tax_eur)} energiebelasting`, "gas"));
  tiles.push(tile("Vaste kosten", eur(t.fixed_eur), "per dag naar rato", "fixed"));
  if (t.export_kwh || t.netting_eur) {
    tiles.push(tile("Teruglevering", eur(-credit(t)), `${energy(t.export_kwh).join(" ")}${t.netting_eur ? `, waarvan ${eur(t.netting_eur)} saldering` : ""}`, "export"));
  }
  if (!t.complete) tiles.push(incompleteTile(t.missing, onGaps));
  node.replaceChildren(...tiles);
}

// ---------- usage costs of one device ----------

function renderUsageSummary(node, r, onGaps) {
  const t = r.total;
  const tiles = [
    tile("Stroomkosten", eur(electricity(t)), `${energy(t.import_kwh).join(" ")} afname, waarvan ${eur(t.electricity_tax_eur)} energiebelasting`, "import"),
  ];
  if (!t.complete) tiles.push(incompleteTile(t.missing, onGaps));
  node.replaceChildren(...tiles);
}

function renderUsageChart(card, p, r, name) {
  const buckets = r.buckets;
  const per = { hour: "uur", day: "dag", month: "maand", year: "jaar" }[p.bucket];
  card.title.textContent = `Stroomkosten ${name}`;
  card.sub.textContent = `Per ${per}: alleen de afname van dit apparaat, tegen het tarief van dat moment, inclusief opslag, energiebelasting en btw. Zonder vaste kosten en teruglevering; telt niet op bij de hele aansluiting.`;
  const series = [{ key: "power", name: "Stroom", color: "var(--import)", sign: 1 }];
  const legend = series.map((s) => legendItem(s.color, s.name));
  if (buckets.some((b) => !b.complete)) legend.push(h("span", {}, h("span", { class: "key incomplete" }), "Onvolledig"));
  card.legend.replaceChildren(...legend);
  columnChart(card.chart, {
    label: `Stroomkosten ${name} per periode`,
    buckets: buckets.map((b) => ({ start: b.start, values: { power: electricity(b) }, complete: b.complete })),
    series,
    markIncomplete: true,
    format: (v) => eurFmt.format(v),
    axisFormat: (v) => `€ ${axisFmt.format(v)}`,
    title: (b) => bucketTitle(p.bucket, b.start) + (b.complete ? "" : " (onvolledig)"),
    tickLabel: (b) => bucketTick(p.bucket, b.start),
  });
  const headers = ["Periode", "Afname", "Stroomkosten"];
  card.table.replaceChildren(h("table", { class: "data" },
    h("thead", {}, h("tr", {}, ...headers.map((x) => h("th", { scope: "col" }, x)))),
    h("tbody", {}, ...buckets.map((b) => h("tr", { title: b.complete ? "" : missingText(b.missing) },
      h("td", {}, bucketTitle(p.bucket, b.start) + (b.complete ? "" : " *")),
      h("td", {}, energy(b.import_kwh).join(" ")),
      h("td", {}, h("strong", {}, eur(electricity(b)))))))));
}

// The net amount of a period, the same as the Totaal column of the table,
// drawn next to the breakdown (below the baseline when negative).
const TOTAL = { key: "total", name: "Totaal", color: "var(--total)", sign: 1, total: true };

function renderChart(card, p, r, water) {
  const buckets = r.buckets;
  card.title.textContent = "Kosten";
  const per = { hour: "uur", day: "dag", month: "maand", year: "jaar" }[p.bucket];
  if (water) {
    card.sub.textContent = `Per ${per}, inclusief btw.`;
    const series = [
      { key: "water", name: "Water", color: "var(--gas)", sign: 1 },
      { key: "fixed", name: "Vaste kosten", color: "var(--fixed)", sign: 1 },
      TOTAL,
    ];
    card.legend.replaceChildren(...series.map((s) => legendItem(s.color, s.name)));
    columnChart(card.chart, {
      label: "Waterkosten per periode",
      buckets: buckets.map((b) => ({ start: b.start, values: { water: waterCost(b), fixed: b.fixed_eur, total: b.total_eur }, complete: b.complete })),
      series,
      format: (v) => eurFmt.format(v),
      axisFormat: (v) => `€ ${axisFmt.format(v)}`,
      title: (b) => bucketTitle(p.bucket, b.start) + (b.complete ? "" : " (onvolledig)"),
      tickLabel: (b) => bucketTick(p.bucket, b.start),
    });
    const headers = ["Periode", "Verbruik", "Water", "Vast", "Totaal"];
    card.table.replaceChildren(h("table", { class: "data" },
      h("thead", {}, h("tr", {}, ...headers.map((x) => h("th", { scope: "col" }, x)))),
      h("tbody", {}, ...buckets.map((b) => h("tr", { title: b.complete ? "" : missingText(b.missing) },
        h("td", {}, bucketTitle(p.bucket, b.start) + (b.complete ? "" : " *")),
        h("td", {}, litersText(b.water_m3)), h("td", {}, eur(waterCost(b))), h("td", {}, eur(b.fixed_eur)),
        h("td", {}, h("strong", {}, eur(b.total_eur))))))));
    return;
  }
  const hasGas = buckets.some((b) => b.gas_m3 || b.gas_eur);
  const hasCredit = buckets.some((b) => credit(b));
  card.sub.textContent = `Per ${per}, inclusief btw. Terugleveren en salderen staan onder de nullijn.`;
  const series = [{ key: "power", name: "Stroom", color: "var(--import)", sign: 1 }];
  if (hasGas) series.push({ key: "gas", name: "Gas", color: "var(--gas)", sign: 1 });
  series.push({ key: "fixed", name: "Vaste kosten", color: "var(--fixed)", sign: 1 });
  if (hasCredit) series.push({ key: "credit", name: "Terug", color: "var(--export)", sign: -1 });
  series.push(TOTAL);
  card.legend.replaceChildren(...series.map((s) => legendItem(s.color, s.name)));
  columnChart(card.chart, {
    label: "Kosten per periode",
    buckets: buckets.map((b) => ({
      start: b.start,
      values: { power: electricity(b), gas: gasCost(b), fixed: b.fixed_eur, credit: credit(b), total: b.total_eur },
      complete: b.complete,
    })),
    series,
    format: (v) => eurFmt.format(v),
    axisFormat: (v) => `€ ${axisFmt.format(v)}`,
    title: (b) => bucketTitle(p.bucket, b.start) + (b.complete ? "" : " (onvolledig)"),
    tickLabel: (b) => bucketTick(p.bucket, b.start),
  });
  const headers = ["Periode", "Stroom", "Gas", "Vast", "Terug", "Totaal"];
  card.table.replaceChildren(h("table", { class: "data" },
    h("thead", {}, h("tr", {}, ...headers.map((x) => h("th", { scope: "col" }, x)))),
    h("tbody", {}, ...buckets.map((b) => h("tr", { title: b.complete ? "" : missingText(b.missing) },
      h("td", {}, bucketTitle(p.bucket, b.start) + (b.complete ? "" : " *")),
      h("td", {}, eur(electricity(b))), h("td", {}, eur(gasCost(b))), h("td", {}, eur(b.fixed_eur)),
      h("td", {}, eur(-credit(b))), h("td", {}, h("strong", {}, eur(b.total_eur))))))));
}

// ---------- what-if comparison ----------

const COMPARE_PERIODS = [
  { id: "12m", label: "Afgelopen 12 maanden" },
  { id: "year", label: "Dit jaar" },
  { id: "lastyear", label: "Vorig jaar" },
];

function comparePeriod(id) {
  const now = new Date();
  if (id === "year") return { from: new Date(now.getFullYear(), 0, 1), to: new Date(now.getFullYear() + 1, 0, 1) };
  if (id === "lastyear") return { from: new Date(now.getFullYear() - 1, 0, 1), to: new Date(now.getFullYear(), 0, 1) };
  return { from: new Date(now.getFullYear(), now.getMonth() - 11, 1), to: new Date(now.getFullYear(), now.getMonth() + 1, 1) };
}

/** "€ 84 goedkoper" / "€ 12 duurder" for a scenario total against the actual one. */
function diffText(scenario, actual) {
  const d = scenario - actual;
  if (Math.abs(d) < 0.5) return "even duur";
  return d < 0 ? `${eur(-d)} goedkoper` : `${eur(d)} duurder`;
}

function compareSection(contracts) {
  const period = h("select", { "aria-label": "Periode" }, ...COMPARE_PERIODS.map((p) => h("option", { value: p.id }, p.label)));
  const table = h("div", { class: "table-wrap" });
  const note = h("p", { class: "hint" });
  const list = h("div", { class: "contract-list" });
  const formSlot = h("div");
  const add = h("button", { class: "btn primary", type: "button" }, "Scenario toevoegen");
  const copy = h("button", { class: "btn", type: "button" }, "Mijn contract als startpunt");
  let scenarios = [];

  const node = h("div", {},
    h("h2", { class: "section-title" }, "Vergelijken"),
    h("p", { class: "secondary contract-help" }, "Wat had je betaald met een ander contract? Een scenario rekent je eigen gemeten verbruik door met andere tarieven, over dezelfde dagen als je echte contracten. Scenario's tellen niet mee in je kosten of voorschot."),
    h("section", { class: "card compare-card" },
      h("div", { class: "row" }, period),
      table, note),
    list, h("div", { class: "row" }, add, copy), formSlot);

  const openForm = (c) => {
    const form = contractForm(c, async () => { formSlot.replaceChildren(); await loadScenarios(); }, () => formSlot.replaceChildren(), { scenario: true });
    formSlot.replaceChildren(form);
    form.scrollIntoView({ behavior: "smooth", block: "start" });
  };
  add.addEventListener("click", () => openForm({ id: 0, name: "", type: "fixed", vat_pct: 21, electricity: {}, gas: {}, monthly: FIXED_SUGGESTIONS.map((name) => ({ name, eur: 0 })) }));
  copy.addEventListener("click", () => {
    const cs = contracts();
    const today = iso(new Date());
    const current = [...cs].reverse().find((c) => c.start <= today) || cs[cs.length - 1];
    if (!current) { alert("Voeg eerst je eigen contract toe."); return; }
    openForm({ ...structuredClone(current), id: 0, name: `${current.name} (aangepast)`, rules_2027: false });
  });
  period.addEventListener("change", () => load());

  async function loadScenarios() {
    try {
      scenarios = (await api("GET", "/api/scenarios")).scenarios;
    } catch (e) {
      list.replaceChildren(h("div", { class: "form-error" }, e.message));
      return;
    }
    list.replaceChildren(...scenarios.map(scenarioCard));
    load();
  }

  function scenarioCard(sc) {
    const c = sc.contract;
    const ct = (v, unit) => `${ctFmt.format(v)} ct/${unit}`;
    const lines = tariffLines(c, ct);
    const monthly = (c.monthly || []).reduce((t, m) => t + m.eur, 0);
    if (monthly) lines.push(`Vaste kosten: ${eur(monthly)} per maand`);
    if (sc.rules_2027) lines.push("Met de regels van 2027: geen salderen, teruglevering tegen de kale marktprijs");
    const edit = h("button", { class: "btn", type: "button", onclick: () => openForm({ ...structuredClone(c), id: sc.id, name: sc.name, rules_2027: sc.rules_2027 }) }, "Bewerken");
    const del = h("button", { class: "btn danger", type: "button" }, "Verwijderen");
    del.addEventListener("click", async () => {
      if (!confirm(`Scenario "${sc.name}" verwijderen?`)) return;
      try { await api("DELETE", `/api/scenarios/${sc.id}`); await loadScenarios(); } catch (err) { alert(err.message); }
    });
    return h("section", { class: "card contract" },
      h("div", { class: "contract-head" },
        h("div", {}, h("h3", {}, sc.name), h("div", { class: "secondary" }, `Scenario · ${typeText(c).toLowerCase()}`)),
        h("div", { class: "row" }, edit, del)),
      h("ul", { class: "contract-lines" }, ...lines.map((l) => h("li", {}, l))));
  }

  let seq = 0;
  async function load() {
    if (!scenarios.length) {
      table.replaceChildren(h("p", { class: "secondary" }, "Nog geen scenario's. Voeg er een toe, of begin met je eigen contract en pas de tarieven aan."));
      note.textContent = "";
      return;
    }
    const p = comparePeriod(period.value);
    const my = ++seq;
    let res;
    try {
      res = await api("GET", `/api/compare?from=${p.from.toISOString()}&to=${p.to.toISOString()}&bucket=month`);
    } catch (e) {
      table.replaceChildren(h("div", { class: "form-error" }, e.message));
      return;
    }
    if (my !== seq) return;
    const cmp = res.comparison;
    const actual = cmp.actual;
    const head = ["Maand", "Werkelijk", ...cmp.scenarios.map((s) => s.scenario.name)];
    // months without any contract are not compared
    const daysIn = (start) => { const d = new Date(start); return new Date(d.getFullYear(), d.getMonth() + 1, 0).getDate(); };
    const rows = actual.buckets.flatMap((b, i) => (b.missing.contract_days >= daysIn(b.start) ? [] : [[
      bucketTitle("month", b.start) + (b.complete ? "" : " *"),
      eur(b.total_eur),
      ...cmp.scenarios.map((s) => eur(s.result.buckets[i]?.total_eur)),
    ]]));
    const total = actual.total.total_eur;
    table.replaceChildren(h("table", { class: "data" },
      h("thead", {}, h("tr", {}, ...head.map((x) => h("th", { scope: "col" }, x)))),
      h("tbody", {}, ...rows.map((r) => h("tr", {}, ...r.map((c) => h("td", {}, c))))),
      h("tfoot", {},
        h("tr", {}, h("th", { scope: "row" }, "Totaal"), h("td", {}, h("strong", {}, eur(total))),
          ...cmp.scenarios.map((s) => h("td", {}, h("strong", {}, eur(s.result.total.total_eur))))),
        h("tr", {}, h("th", { scope: "row" }, "Verschil"), h("td", {}, ""),
          ...cmp.scenarios.map((s) => h("td", {}, diffText(s.result.total.total_eur, total)))))));
    const parts = ["Terugblik met je werkelijke verbruik en de marktprijzen van toen; toekomstige prijzen zijn anders."];
    if (cmp.scenarios.some((s) => s.scenario.contract.type === "dynamic" || gasTypeOf(s.scenario.contract) === "dynamic")) parts.push("Met een dynamisch contract verschuif je vaak verbruik naar goedkope uren; een besparing is daardoor eerder een ondergrens.");
    if (actual.total.missing.contract_days) parts.push("Dagen zonder contract worden niet vergeleken; maanden zonder contract staan er niet in.");
    const m = { ...actual.total.missing, contract_days: 0 };
    const rest = missingText(m);
    if (rest) parts.push(`* Onvolledig: ${rest}; dat geldt voor werkelijk en scenario gelijk.`);
    note.textContent = parts.join(" ");
  }

  return { node, load: loadScenarios };
}

// ---------- advance payments ----------

/** "€ 120 terug" or "€ 80 bijbetalen" for a result (paid − cost). */
export function resultText(v) {
  if (Math.abs(v) < 0.5) return "ongeveer € 0";
  return v > 0 ? `${eur(v)} terug` : `${eur(-v)} bijbetalen`;
}

/** "5 betalingen · volgende op 22 november" under the amount paid so far. */
function paidHint(a) {
  const count = `${a.payments} ${a.payments === 1 ? "betaling" : "betalingen"}`;
  return a.next_payment ? `${count} · volgende op ${dayMonth.format(new Date(a.next_payment))}` : count;
}

/** The advance card: one block per status (all payments, or per part of a contract that splits electricity and gas). */
function renderAdvance(node, list, water) {
  node.hidden = !list.length;
  if (!list.length) return;
  node.replaceChildren(...list.map((a) => h("div", { class: "advance-part" }, ...advanceBlock(a, water))));
}

const PART_TITLE = { electricity: "Voorschot stroom", gas: "Voorschot gas" };

export function advanceBlock(a, water) {
  const last = new Date(new Date(a.to).getTime() - 86400e3);
  const balance = a.balance_eur;
  const even = Math.abs(balance) < 0.5;
  const f = a.forecast;
  // next to the tiles: the whole period's expected fixed and usage costs
  // (usage net of export credits; all tariff periods of a chain)
  const expected = f && f.fixed_cost_eur != null && f.usage_cost_eur != null
    ? h("dl", { class: "advance-expected" },
      h("dt", {}, "Verwachte vaste kosten deze periode"), h("dd", {}, eur(f.fixed_cost_eur)),
      h("dt", {}, "Verwachte verbruikskosten deze periode"), h("dd", {}, eur(f.usage_cost_eur)))
    : null;
  const children = [
    h("div", { class: "chart-head" }, h("div", {},
      h("h3", {}, PART_TITLE[a.part] || "Voorschot"),
      h("div", { class: "chart-sub" }, `${a.contract_name}${a.contracts > 1 ? ` (${a.contracts} tariefperiodes)` : ""} · afrekenperiode ${longDate.format(new Date(a.from))} t/m ${longDate.format(last)} · nu ${eur(a.monthly_eur)} per maand`))),
    h("div", { class: "price-tiles" },
      h("div", {}, h("div", { class: "label secondary" }, "Betaald tot nu toe"), h("div", { class: "value" }, eur(a.paid_eur)), h("div", { class: "hint" }, paidHint(a))),
      h("div", {}, h("div", { class: "label secondary" }, "Kosten tot nu toe"), h("div", { class: "value" }, eur(a.cost_eur)), h("div", { class: "hint" }, a.estimated_days ? `waarvan ${a.estimated_days} ${a.estimated_days === 1 ? "dag" : "dagen"} geschat` : "volgens je contract en metingen")),
      h("div", {}, h("div", { class: "label secondary" }, "Stand"), h("div", { class: "value" }, even ? "€ 0" : eur(Math.abs(balance))),
        h("div", { class: "hint" }, even ? "precies gelijk" : balance > 0 ? "je staat voor" : "je staat achter")),
      expected),
  ];
  if (f) {
    const ended = new Date(a.now) >= new Date(a.to);
    const band = ended || Math.abs(f.high_eur - f.low_eur) < 1 ? "" : ` (tussen ${resultText(f.low_eur)} en ${resultText(f.high_eur)})`;
    children.push(h("p", {}, h("strong", {}, `${ended ? "Uitkomst" : "Verwacht bij de afrekening"}: ${resultText(f.result_eur)}`), band));
    if (f.advice_monthly_eur != null) {
      const diff = f.advice_monthly_eur - a.monthly_eur;
      let advice;
      if (Math.abs(diff) < 5) advice = `Je voorschot van ${eur(a.monthly_eur)} per maand past goed.`;
      else if (f.advice_monthly_eur === 0) advice = "Je hebt al meer betaald dan de verwachte kosten tot het einde van de periode; een lager voorschot is mogelijk.";
      else advice = `Advies: ${diff > 0 ? "verhoog" : "verlaag"} je voorschot naar ${eur(f.advice_monthly_eur)} per maand om op nul uit te komen (nog ${f.remaining_payments} ${f.remaining_payments === 1 ? "betaling" : "betalingen"} in deze periode).`;
      children.push(h("p", {}, advice));
    }
    if (!ended) children.push(h("p", { class: "hint" }, a.method_text ?? (water ? waterMethodText(f) : methodText(f, a.part))));
    if (!ended) {
      const list = percentagesNode(a.percentages ?? f.method?.percentages);
      if (list) children.push(list);
    }
  } else if (a.forecast_error) {
    children.push(h("p", { class: "hint" }, a.forecast_error));
  }
  if (a.estimated_days) children.push(h("p", { class: "hint" }, `Op ${a.estimated_days} ${a.estimated_days === 1 ? "dag" : "dagen"} van deze periode ontbreken metingen (bijvoorbeeld voordat de gateway draaide); dat verbruik is geschat zoals in de prognose. Importeer je historie onder Instellingen om dat te verbeteren.`));
  const unpriced = (a.missing?.prices || 0) + (a.missing?.gas_prices || 0);
  if (unpriced) children.push(h("p", { class: "hint" }, `Niet meegeteld: ${missingText(a.missing)}.`));
  return children;
}

const nf0 = new Intl.NumberFormat("nl-NL", { maximumFractionDigits: 0 });
const nf1 = new Intl.NumberFormat("nl-NL", { maximumFractionDigits: 1 });

/** Labels of the percentages a forecast uses (forecast.Used keys). */
export const PERCENT_LABEL = {
  period_min: "vorige periode telt mee vanaf",
  day_min: "dag telt als gemeten vanaf",
  usage_electricity: "marge op het stroomverbruik",
  usage_gas: "marge op het gasverbruik",
  usage_water: "marge op het waterverbruik",
  usage_heating: "marge op het verbruik",
  entered: "marge op het ingevulde jaarverbruik",
  entered_heating: "marge op het ingevulde verbruik",
  price: "marge op de marktprijzen",
  price_heating: "marge op de prijs uit de vorige afrekening",
  trend_import: "trend afname",
  trend_export: "trend teruglevering",
  trend_gas: "trend gas",
  trend_water: "trend water",
};

/** Where a percentage comes from: "berekend uit 4 maanden", "standaard" or "zelf ingesteld". */
export function percentSource(source, months) {
  if (source === "computed") return months ? `berekend uit ${months} ${months === 1 ? "maand" : "maanden"}` : "berekend";
  return source === "setting" ? "zelf ingesteld" : "standaard";
}

/** One used percentage as text, e.g. "marge op het gasverbruik ±6% (berekend uit 4 maanden)". */
export function percentValue(key, pct) {
  if (key.startsWith("trend_")) return `${pct > 0 ? "+" : pct < 0 ? "−" : ""}${nf1.format(Math.abs(pct))}%`;
  if (key === "period_min" || key === "day_min") return `${nf1.format(pct)}%`;
  return `±${nf1.format(pct)}%`;
}

function percentText(u) {
  const value = u.key.startsWith("trend_") && u.source === "setting" ? `uit (berekend: ${percentValue(u.key, u.auto ?? 0)})` : percentValue(u.key, u.pct);
  return `${PERCENT_LABEL[u.key] || u.key} ${value} (${percentSource(u.source, u.months)})`;
}

/** The percentages a forecast used, with a link to change them; the two minimums only when set. */
export function percentagesNode(list) {
  const shown = (list || []).filter((u) => !["period_min", "day_min"].includes(u.key) || u.source === "setting");
  if (!shown.length) return null;
  return h("p", { class: "hint" }, `Gebruikte percentages: ${shown.map(percentText).join("; ")}. `,
    h("a", { href: "#/instellingen/prognose" }, "Aanpassen onder Instellingen"), ".");
}

function previousPeriod(m) {
  const last = new Date(new Date(m.previous_to).getTime() - 86400e3);
  return `${longDate.format(new Date(m.previous_from))} t/m ${longDate.format(last)}`;
}

const FIELD = {
  import_kwh: { name: "afname", unit: "kWh" },
  export_kwh: { name: "teruglevering", unit: "kWh" },
  gas_m3: { name: "gas", unit: "m³" },
  water_m3: { name: "water", unit: "m³" },
};

/** Where the usage of the rest of the period comes from, per field. */
function sourceParts(m, fields) {
  const src = m.sources || {};
  const amount = (k, y) => `${(k === "water_m3" ? nf1 : nf0).format(y[k] || 0)} ${FIELD[k].unit}`;
  const entered = fields.filter((k) => src[k] === "contract");
  const previous = fields.filter((k) => src[k] === "previous");
  const parts = [];
  if (entered.length) {
    let text = `${entered.map((k) => `${FIELD[k].name} ${amount(k, m.yearly)} per jaar`).join(", ")} zoals ingevuld bij het contract, verdeeld met een seizoensprofiel`;
    if (m.previous_yearly) text += ` (vorige periode: ${entered.map((k) => amount(k, m.previous_yearly)).join(", ")})`;
    parts.push(text);
  }
  if (previous.length) {
    let text = `${previous.map((k) => FIELD[k].name).join(", ")} uit dezelfde dagen van de vorige periode (${previousPeriod(m)}, ${nf0.format((m.previous_coverage || 0) * 100)}% gemeten)`;
    if (m.profile_days && !entered.length) text += `; ${m.profile_days} ${m.profile_days === 1 ? "dag" : "dagen"} daarvan niet gemeten, die komen uit het jaarverbruik van die periode`;
    parts.push(text);
  }
  return parts;
}

function waterMethodText(f) {
  const m = f.method;
  const parts = [`Prognose voor de rest van de periode: ${nf1.format(f.water_m3)} m³ water`, ...sourceParts(m, ["water_m3"])];
  let text = parts.join("; ") + ".";
  if (Math.abs(f.high_eur - f.low_eur) >= 1) text += " De marge houdt rekening met afwijkingen in je verbruik.";
  return text;
}

/** How the forecast was made; part "gas" or "electricity" limits it to that part's usage. */
function methodText(f, part) {
  const m = f.method;
  const used = [];
  if (part !== "gas") {
    used.push(`${nf0.format(f.import_kwh)} kWh afname`);
    if (f.export_kwh >= 1) used.push(`${nf0.format(f.export_kwh)} kWh teruglevering`);
  }
  if (part !== "electricity" && (f.gas_m3 >= 1 || part === "gas")) used.push(`${nf0.format(f.gas_m3)} m³ gas`);
  const fields = part === "gas" ? ["gas_m3"] : part === "electricity" ? ["import_kwh", "export_kwh"] : ["import_kwh", "export_kwh", "gas_m3"];
  const parts = [`Prognose voor de rest van de periode: ${used.join(", ")}`, ...sourceParts(m, fields)];
  const prices = {
    month_last_year: "marktprijzen van dezelfde maand vorig jaar",
    last_30_days: "gemiddelde marktprijzen van de laatste 30 dagen",
    mixed: "marktprijzen van vorig jaar en van de laatste 30 dagen",
  }[m.prices];
  if (prices) parts.push(prices);
  let text = parts.join("; ") + ".";
  if (Math.abs(f.high_eur - f.low_eur) >= 1) {
    text += " De marge houdt rekening met afwijkingen in je verbruik" + (m.prices ? " en in de marktprijzen" : "") + ".";
  }
  return text;
}

// ---------- contract form ----------

export function numberInput(value, attrs = {}) {
  return h("input", { type: "number", step: "any", inputmode: "decimal", value: value ?? "", ...attrs });
}

export function field(text, input, hint) {
  return h("label", { class: "field" }, h("span", {}, text), input, hint ? h("small", { class: "secondary" }, hint) : null);
}

/** The part a fixed cost probably belongs to by its name: gas when it says so, else electricity (like the energy tax reduction). */
const guessPart = (name) => (/gas/i.test(name) ? "gas" : "electricity");

/**
 * Monthly fixed costs: rows of description and euros, with suggestions.
 * setSplit(true) shows a choice per row whether the cost belongs to
 * electricity or gas (a contract that splits them); read() then includes it.
 */
function monthlyEditor(c, suggestions) {
  const rows = h("div", { class: "monthly-rows" });
  const listId = `fixed-cost-names-${suggestions === FIXED_SUGGESTIONS ? "energy" : "water"}`;
  let split = false;
  const add = (m = { name: "", eur: "" }) => {
    const n = h("input", { type: "text", value: m.name, placeholder: "Omschrijving", list: listId, "aria-label": "Omschrijving" });
    const v = numberInput(m.eur, { "aria-label": "Bedrag per maand in euro" });
    const part = h("select", { class: "part", "aria-label": "Hoort bij", hidden: !split },
      h("option", { value: "electricity" }, "Stroom"), h("option", { value: "gas" }, "Gas"));
    part.value = m.part || guessPart(m.name || "");
    let chosen = !!m.part; // follow the name until the user picks a part
    part.addEventListener("change", () => { chosen = true; });
    n.addEventListener("input", () => { if (!chosen) part.value = guessPart(n.value); });
    const rm = h("button", { class: "btn", type: "button", "aria-label": "Post verwijderen" }, "×");
    const row = h("div", { class: "monthly-row" }, n, part, h("span", { class: "secondary" }, "€"), v, rm);
    row.read = () => ({ name: n.value.trim(), eur: Number(v.value || 0), ...(split ? { part: part.value } : {}) });
    row.setSplit = (on) => { part.hidden = !on; };
    rm.addEventListener("click", () => row.remove());
    rows.appendChild(row);
  };
  for (const m of c.monthly || []) add(m);
  const node = h("div", {}, rows,
    h("datalist", { id: listId }, ...suggestions.map((s) => h("option", { value: s }))),
    h("div", { class: "row" }, h("button", { class: "btn", type: "button", onclick: () => add() }, "Post toevoegen")));
  const setSplit = (on) => {
    split = on;
    rows.classList.toggle("split", on);
    for (const r of rows.children) r.setSplit(on);
  };
  return { node, setSplit, read: () => [...rows.children].map((r) => r.read()).filter((m) => m.name || m.eur) };
}

/** Monthly advance payments with dates from which they apply; start is the contract's start date input, key the contract field ("advance" or "gas_advance"). */
export function advanceEditor(c, start, key = "advance") {
  const series = c[key];
  const toggle = h("input", { type: "checkbox", checked: !!series?.amounts?.length });
  const rows = h("div", { class: "monthly-rows" });
  const add = (a = { from: start.value || c.start || "", eur: "" }) => {
    const d = h("input", { type: "date", value: a.from, "aria-label": "Vanaf" });
    const v = numberInput(a.eur, { min: 0, "aria-label": "Voorschot per maand in euro" });
    const rm = h("button", { class: "btn", type: "button", "aria-label": "Bedrag verwijderen" }, "×");
    const row = h("div", { class: "monthly-row advance-row" }, h("span", { class: "secondary" }, "vanaf"), d, h("span", { class: "secondary" }, "€"), v, rm);
    row.read = () => ({ from: d.value, eur: Number(v.value || 0) });
    rm.addEventListener("click", () => row.remove());
    rows.appendChild(row);
  };
  for (const a of series?.amounts || []) add(a);
  const node = h("div", {}, rows,
    h("p", { class: "secondary" }, "Bedrag per maand incl. btw. Elke regel is een betaling op die datum, daarna elke maand op dezelfde dag het volle bedrag, tot de volgende regel (de 31e wordt in een kortere maand de laatste dag). Een eenmalig andere eerste betaling, bijvoorbeeld 150,57 op 14 april en daarna 126 elke 22e, vul je in als twee regels: 14 april en 22 april."),
    h("div", { class: "row" }, h("button", { class: "btn", type: "button", onclick: () => add({ from: "", eur: "" }) }, "Ander bedrag vanaf een datum")));
  const sync = () => {
    node.hidden = !toggle.checked;
    if (toggle.checked && !rows.children.length) add();
  };
  toggle.addEventListener("change", sync);
  sync();
  const read = () => (toggle.checked ? { amounts: [...rows.children].map((r) => r.read()).filter((a) => a.from || a.eur) } : null);
  return { toggle, node, read, filled: () => !!read()?.amounts.length };
}

/** Form for a water contract: tariff per m³, tax on tap water, fixed costs, advance and expected yearly usage. */
function waterContractForm(c, onSaved, onCancel, base) {
  const w = c.water || {};
  const name = h("input", { type: "text", value: c.name || "", placeholder: "Bijv. Waterbedrijf 2026", required: true });
  const start = h("input", { type: "date", value: c.start || "", required: true });
  const end = h("input", { type: "date", value: c.end || "" });
  const continues = h("input", { type: "checkbox", checked: !!c.continues });
  const rate = numberInput(w.rate_ct, { min: 0 });
  const tax = numberInput(w.tax_ct, { min: 0 });
  const monthly = monthlyEditor(c, WATER_SUGGESTIONS);
  const adv = advanceEditor(c, start);
  const estWater = numberInput(c.estimate?.water_m3 || "", { min: 0 });
  const error = h("div", { class: "form-error", role: "alert", hidden: true });
  const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
  const cancel = h("button", { class: "btn", type: "button", onclick: onCancel }, "Annuleren");
  const num = (input) => Number(input.value || 0);

  const form = h("form", { class: "card contract-form" },
    h("h3", {}, c.id ? "Watercontract bewerken" : "Nieuw watercontract"),
    h("div", { class: "form-grid" },
      field("Naam", name),
      field("Begindatum", start, "mag elke dag zijn"), field("Einddatum (optioneel)", end, "laatste dag; leeg = tot het volgende contract")),
    h("label", { class: "check" }, continues, " Vervolg op het vorige contract: alleen de tarieven veranderen (het afrekenjaar en het voorschot lopen door)"),
    h("h4", {}, "Water"),
    h("div", { class: "form-grid" },
      field("Tarief water", rate, "ct per m³, incl. btw"),
      field("Belasting op leidingwater", tax, "ct per m³, incl. btw; telt over de eerste 300 m³ per jaar")),
    h("h4", {}, "Vaste kosten per maand"),
    h("p", { class: "secondary" }, "Incl. btw, bijvoorbeeld het vastrecht. Staat het per jaar op je afrekening, deel het dan door 12."),
    monthly.node,
    h("h4", {}, "Voorschot"),
    h("label", { class: "check" }, adv.toggle, " Ik betaal een maandelijks voorschot"),
    adv.node,
    h("details", {}, h("summary", {}, "Verwacht jaarverbruik (voor de prognose, optioneel)"),
      h("p", { class: "secondary" }, "Leeg laten: de prognose gebruikt je verbruik van vorig jaar, of schat het jaarverbruik uit je metingen. Bij een tariefwijziging (vervolgcontract) hoef je het niet opnieuw in te vullen."),
      h("div", { class: "form-grid" }, field("Water", estWater, "m³ per jaar"))),
    error,
    h("div", { class: "row form-actions" }, save, cancel));

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    error.hidden = true;
    const body = {
      name: name.value, type: "water", start: start.value, end: end.value, continues: continues.checked,
      water: { rate_ct: num(rate), tax_ct: num(tax) },
      monthly: monthly.read(),
      advance: adv.read(),
      estimate: { water_m3: num(estWater) },
    };
    save.disabled = true;
    try {
      onSaved(c.id ? await api("PUT", `${base}/contracts/${c.id}`, body) : await api("POST", `${base}/contracts`, body));
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
    } finally {
      save.disabled = false;
    }
  });
  return form;
}

// contractForm edits a contract, or with opts.scenario a what-if contract:
// the same tariffs without dates, advance or chain, plus the 2027 rules.
// opts.water edits a water contract (saved under opts.api).
function contractForm(c, onSaved, onCancel, opts = {}) {
  if (opts.water) return waterContractForm(c, onSaved, onCancel, opts.api);
  const scenario = !!opts.scenario;
  const rules2027 = h("input", { type: "checkbox", checked: !!c.rules_2027 });
  const e = c.electricity || {}, g = c.gas || {};
  const name = h("input", { type: "text", value: c.name || "", placeholder: "Bijv. Leverancier dynamisch 2026", required: true });
  const typeSelect = (value) => h("select", {},
    h("option", { value: "dynamic", selected: value !== "fixed" }, "Dynamisch (marktprijs + opslag)"),
    h("option", { value: "fixed", selected: value === "fixed" }, "Vast of variabel tarief"));
  const type = typeSelect(c.type);
  // stroom en gas apart: each its own type and advance (e.g. two suppliers)
  const split = h("input", { type: "checkbox", checked: !!c.split });
  const gasType = typeSelect(c.split ? c.gas_type : c.type);
  const start = h("input", { type: "date", value: c.start || "", required: true });
  const continues = h("input", { type: "checkbox", checked: !!c.continues });
  const end = h("input", { type: "date", value: c.end || "" });
  const vat = numberInput(c.vat_pct ?? 21, { min: 0, max: 100 });

  const markup = numberInput(e.markup_ct);
  const hourly = h("select", {},
    h("option", { value: "", selected: !e.hourly }, "Per kwartier"),
    h("option", { value: "hour", selected: !!e.hourly }, "Per uur (gemiddelde van de vier kwartieren)"));
  const dual = h("input", { type: "checkbox", checked: !!(e.normal_ct || e.low_ct) });
  const rate = numberInput(e.rate_ct);
  const normal = numberInput(e.normal_ct);
  const low = numberInput(e.low_ct);
  const tax = numberInput(e.tax_ct);
  const exportCt = numberInput(e.export_ct);
  const gasMarkup = numberInput(g.markup_ct);
  const gasRate = numberInput(g.rate_ct);
  const gasTax = numberInput(g.tax_ct);

  const adv = advanceEditor(c, start);
  const gasAdv = advanceEditor(c, start, "gas_advance");
  const est = c.estimate || {};
  const estImport = numberInput(est.import_kwh || "", { min: 0 });
  const estExport = numberInput(est.export_kwh || "", { min: 0 });
  const estGas = numberInput(est.gas_m3 || "", { min: 0 });
  const exportVat = h("select", {},
    h("option", { value: "", selected: c.export_vat == null }, "Automatisch: met btw t/m 2026, zonder btw vanaf 2027"),
    h("option", { value: "yes", selected: c.export_vat === true }, "Altijd met btw"),
    h("option", { value: "no", selected: c.export_vat === false }, "Altijd zonder btw"));

  const monthly = monthlyEditor(c, FIXED_SUGGESTIONS);

  const dynOnly = [field("Inkoopopslag stroom", markup, "ct per kWh, incl. btw"), field("Inkoopopslag gas", gasMarkup, "ct per m³, incl. btw")];
  const hourlyField = field("Marktprijs stroom", hourly, "zoals je leverancier afrekent: per kwartier, of per uur tegen het gemiddelde van dat uur");
  const singleRate = field("Tarief stroom", rate, "ct per kWh, incl. btw");
  const dualRates = [field("Normaal tarief", normal, "ct per kWh, incl. btw"), field("Daltarief", low, "doordeweeks 23–7 uur en weekend")];
  const fixedGas = field("Tarief gas", gasRate, "ct per m³, incl. btw");
  const dualField = h("label", { class: "check" }, dual, " Normaal- en daltarief");
  const exportField = field("Teruglevering", exportCt, " ");
  const exportVatField = field("Btw over de terugleververgoeding", exportVat, "particulieren krijgen vanaf 2027 meestal de kale marktprijs (kleineondernemersregeling)");

  const typeField = field("Soort", type);
  const gasTypeField = field("Soort gas", gasType);
  const splitField = h("label", { class: "check" }, split, " Stroom en gas apart: twee contracten, elk met een eigen soort en voorschot (bijvoorbeeld bij twee leveranciers; zet beide namen in de naam)");
  const advToggleText = h("span", {});
  const gasAdvSection = h("div", {},
    h("h4", {}, "Voorschot gas"),
    h("label", { class: "check" }, gasAdv.toggle, " Ik betaal een maandelijks voorschot voor gas"),
    gasAdv.node);
  const advTitle = h("h4", {}, "Voorschot");

  const sync = () => {
    const dyn = type.value === "dynamic";
    const dynGas = split.checked ? gasType.value === "dynamic" : dyn;
    gasTypeField.hidden = !split.checked;
    typeField.querySelector("span").textContent = split.checked ? "Soort stroom" : "Soort";
    advTitle.textContent = split.checked ? "Voorschot stroom" : "Voorschot";
    advToggleText.textContent = split.checked
      ? " Ik betaal een maandelijks voorschot voor stroom (uit: de kosten worden achteraf in rekening gebracht, zoals bij Tibber)"
      : " Ik betaal een maandelijks voorschot (uit: de kosten worden achteraf in rekening gebracht, zoals bij Tibber)";
    gasAdvSection.hidden = !split.checked;
    monthly.setSplit(split.checked);
    dynOnly[0].hidden = !dyn;
    hourlyField.hidden = !dyn;
    dynOnly[1].hidden = !dynGas;
    fixedGas.hidden = dynGas;
    dualField.hidden = dyn;
    singleRate.hidden = dyn || dual.checked;
    for (const f of dualRates) f.hidden = dyn || !dual.checked;
    exportField.querySelector("small").textContent = dyn
      ? "ct per kWh bovenop de marktprijs: + vergoeding, − terugleverkosten. Vanaf 2027 telt de marktprijs zonder btw en niet onder 0."
      : "terugleververgoeding in ct per kWh (t/m 2026 alleen boven je saldering)";
    exportVatField.hidden = !dyn;
  };
  type.addEventListener("change", sync);
  gasType.addEventListener("change", sync);
  dual.addEventListener("change", sync);
  split.addEventListener("change", () => {
    // switching it off drops what only a split contract has: ask first
    if (!split.checked && (gasAdv.filled() || gasType.value !== type.value)) {
      const lost = [gasType.value !== type.value ? "de aparte soort voor gas (gas krijgt dan dezelfde soort als stroom)" : "", gasAdv.filled() ? "het voorschot voor gas" : ""].filter(Boolean);
      if (!confirm(`Stroom en gas weer samen? Dan vervalt ${lost.join(" en ")} bij het opslaan.`)) {
        split.checked = true;
        return;
      }
    }
    if (split.checked) gasType.value = type.value; // start from the same type
    sync();
  });
  sync();

  const error = h("div", { class: "form-error", role: "alert", hidden: true });
  const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
  const cancel = h("button", { class: "btn", type: "button", onclick: onCancel }, "Annuleren");
  const num = (input) => Number(input.value || 0);

  const title = scenario ? (c.id ? "Scenario bewerken" : "Nieuw scenario") : (c.id ? "Contract bewerken" : "Nieuw contract");
  const form = h("form", { class: "card contract-form" },
    h("h3", {}, title),
    scenario
      ? h("div", { class: "form-grid" }, field("Naam", name), typeField, gasTypeField)
      : h("div", { class: "form-grid" },
        field("Naam", name), typeField, gasTypeField,
        field("Begindatum", start, "mag elke dag zijn"), field("Einddatum (optioneel)", end, "laatste dag; leeg = tot het volgende contract")),
    splitField,
    scenario
      ? h("label", { class: "check" }, rules2027, " Rekenen met de regels van 2027: geen salderen, teruglevering tegen de kale marktprijs (niet onder 0)")
      : h("label", { class: "check" }, continues, " Vervolg op het vorige contract: zelfde leverancier, alleen de tarieven veranderen (het afrekenjaar en het voorschot lopen door)"),
    h("h4", {}, "Stroom"),
    h("div", { class: "form-grid" }, dynOnly[0], hourlyField, singleRate, dualField, ...dualRates,
      field("Energiebelasting stroom", tax, "ct per kWh, incl. btw"), exportField),
    h("h4", {}, "Gas"),
    h("div", { class: "form-grid" }, dynOnly[1], fixedGas, field("Energiebelasting gas", gasTax, "ct per m³, incl. btw")),
    h("h4", {}, "Vaste kosten per maand"),
    h("p", { class: "secondary" }, "Incl. btw. Een korting, zoals de vermindering energiebelasting, vul je negatief in. Bij stroom en gas apart kies je per post waar hij bij hoort; de vermindering energiebelasting hoort bij stroom."),
    monthly.node,
    scenario ? null : advTitle,
    scenario ? null : h("label", { class: "check" }, adv.toggle, advToggleText),
    scenario ? null : adv.node,
    scenario ? null : gasAdvSection,
    scenario ? null : h("details", {}, h("summary", {}, "Verwacht jaarverbruik (voor de prognose, optioneel)"),
      h("p", { class: "secondary" }, "Leeg laten: de prognose gebruikt je verbruik van vorig jaar, of schat het jaarverbruik uit je metingen. Vul het in als je net begint of als er iets verandert, zoals een warmtepomp of zonnepanelen. Het is je verbruik per heel jaar, niet per contract: bij een tariefwijziging (vervolgcontract) hoef je het niet opnieuw in te vullen, en per veld telt het laatst ingevulde getal."),
      h("div", { class: "form-grid" }, field("Stroom afname", estImport, "kWh per jaar"), field("Teruglevering", estExport, "kWh per jaar"), field("Gas", estGas, "m³ per jaar"))),
    h("details", {}, h("summary", {}, "Btw"), h("div", { class: "form-grid" }, field("Btw over de marktprijs", vat, "procent; standaard 21"), exportVatField)),
    error,
    h("div", { class: "row form-actions" }, save, cancel));

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    error.hidden = true;
    const dyn = type.value === "dynamic";
    const dynGas = split.checked ? gasType.value === "dynamic" : dyn;
    const body = {
      name: name.value, type: type.value, start: start.value, end: end.value, vat_pct: num(vat), continues: continues.checked,
      split: split.checked, gas_type: split.checked ? gasType.value : "",
      electricity: {
        markup_ct: dyn ? num(markup) : 0,
        hourly: dyn && hourly.value === "hour",
        rate_ct: !dyn && !dual.checked ? num(rate) : 0,
        normal_ct: !dyn && dual.checked ? num(normal) : 0,
        low_ct: !dyn && dual.checked ? num(low) : 0,
        tax_ct: num(tax), export_ct: num(exportCt),
      },
      gas: { markup_ct: dynGas ? num(gasMarkup) : 0, rate_ct: dynGas ? 0 : num(gasRate), tax_ct: num(gasTax) },
      monthly: monthly.read(),
      advance: adv.read(),
      gas_advance: split.checked ? gasAdv.read() : null,
      estimate: { import_kwh: num(estImport), export_kwh: num(estExport), gas_m3: num(estGas) },
      export_vat: { yes: true, no: false }[exportVat.value] ?? null,
    };
    save.disabled = true;
    try {
      let saved;
      if (scenario) {
        const sc = { name: name.value, rules_2027: rules2027.checked, contract: { ...body, start: "", end: "", continues: false, advance: null, gas_advance: null, estimate: null } };
        saved = c.id ? await api("PUT", `/api/scenarios/${c.id}`, sc) : await api("POST", "/api/scenarios", sc);
      } else {
        saved = c.id ? await api("PUT", `/api/contracts/${c.id}`, body) : await api("POST", "/api/contracts", body);
      }
      onSaved(saved);
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
    } finally {
      save.disabled = false;
    }
  });
  return form;
}
