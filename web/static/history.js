// Usage panel of "Verbruik en Kosten": power over time and energy per
// period (Verbruik), or per-phase grid quality and the meter's events (Net),
// for one device and a period chosen by the tab (usage.js).
import { lineChart, columnChart } from "./charts.js";
import { h, api, power, energy, gas, liters, flow, valueUnit, dates, fmt } from "./util.js";

function bucketTitle(bucket, start) {
  const d = new Date(start);
  if (bucket === "quarter") return `${dates.day(d)} ${dates.short(d)}–${dates.short(new Date(d.getTime() + 15 * 60e3))}`;
  if (bucket === "hour") return `${dates.day(d)} ${dates.short(d)}–${dates.short(new Date(d.getTime() + 3600e3))}`;
  if (bucket === "day") return dates.day(d);
  if (bucket === "year") return String(d.getFullYear());
  return dates.month(d);
}

function bucketTick(bucket, start) {
  const d = new Date(start);
  if (bucket === "quarter" || bucket === "hour") return dates.short(d);
  if (bucket === "day") return String(d.getDate());
  if (bucket === "year") return String(d.getFullYear());
  return new Intl.DateTimeFormat("nl-NL", { month: "short" }).format(d).replace(".", "");
}

const PER = { quarter: "kwartier", hour: "uur", day: "dag", month: "maand", year: "jaar" };

/** Months folded into years (the energy API has no year bucket); leading years without data are left out. */
function yearly(buckets) {
  const years = new Map();
  for (const b of buckets) {
    const y = new Date(b.start).getFullYear();
    let x = years.get(y);
    if (!x) { x = { start: new Date(y, 0, 1).toISOString(), complete: true }; years.set(y, x); }
    for (const k of ["import_kwh", "export_kwh", "gas_m3", "water_m3"]) {
      if (b[k] != null) x[k] = (x[k] || 0) + b[k];
    }
    x.complete = x.complete && b.complete;
  }
  const list = [...years.values()];
  const has = (x) => x.import_kwh != null || x.export_kwh != null || x.gas_m3 != null || x.water_m3 != null;
  const first = list.findIndex(has);
  return first < 0 ? [] : list.slice(first);
}

/**
 * The panel. load(device, p, mode) draws it for a device and a period p
 * ({from, to, bucket, rate, yearly}) in mode "verbruik" or "net".
 */
export function historyPanel() {
  const summary = h("section", { class: "summary", "aria-label": "Totalen" });
  const rateCard = chartCard();
  const energyCard = chartCard();
  const gasCard = chartCard();
  const grid = { voltage: chartCard(), current: chartCard(), power: chartCard(), events: eventsCard() };
  const gridNodes = [grid.voltage.node, grid.current.node, grid.power.node, grid.events.node];
  const node = h("div", { class: "usage-panel" }, summary, rateCard.node, energyCard.node, gasCard.node, ...gridNodes);

  let seq = 0;
  async function load(device, p, mode, width) {
    const net = mode === "net";
    const my = ++seq;
    const cards = [rateCard, energyCard, gasCard, grid.voltage, grid.current, grid.power];
    for (const c of cards) c.chart.classList.add("loading");
    const q = (extra) => `device=${encodeURIComponent(device.id)}&from=${p.from.toISOString()}&to=${p.to.toISOString()}${extra}`;
    const points = Math.min(1440, Math.max(200, Math.round(width || 800)));
    const energyBucket = p.yearly ? "month" : p.bucket;
    const [rate, energyRes, gridRes] = await Promise.all([
      !net && p.rate ? api("GET", `/api/history/rate?${q(`&points=${points}`)}`).catch((e) => ({ error: e.message })) : null,
      net ? null : api("GET", `/api/history/energy?${q(`&bucket=${energyBucket}`)}`).catch((e) => ({ error: e.message })),
      net && GRID_KINDS.has(device.kind) ? api("GET", `/api/history/grid?${q(`&points=${points}`)}`).catch((e) => ({ error: e.message })) : null,
    ]);
    if (my !== seq) return; // a newer request superseded this one
    for (const c of cards) c.chart.classList.remove("loading");
    if (energyRes && !energyRes.error && p.yearly) energyRes.buckets = yearly(energyRes.buckets);

    summary.hidden = net;
    rateCard.node.hidden = energyCard.node.hidden = gasCard.node.hidden = net;
    for (const n of gridNodes) n.hidden = true;
    if (net) {
      renderGrid(grid, device, p, gridRes);
      return;
    }
    renderSummary(summary, device, energyRes);
    renderRate(rateCard, device, p, rate);
    renderEnergy(energyCard, gasCard, device, p, energyRes);
  }
  return { node, load };
}

function chartCard() {
  const title = h("h3");
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
  return { node, title, sub, legend, chart, table, toggle };
}

function legendItem(color, label) {
  const key = h("span", { class: "key" });
  key.style.background = color;
  return h("span", {}, key, label);
}

function table(headers, rows) {
  return h("table", { class: "data" },
    h("thead", {}, h("tr", {}, ...headers.map((t) => h("th", { scope: "col" }, t)))),
    h("tbody", {}, ...rows.map((r) => h("tr", {}, ...r.map((c) => h("td", {}, c))))));
}

function sum(buckets, key) {
  let total = null;
  for (const b of buckets) if (b[key] != null) total = (total || 0) + b[key];
  return total;
}

function renderSummary(node, device, res) {
  if (!res || res.error) { node.hidden = true; return; }
  const b = res.buckets;
  const tiles = [];
  const tileEl = (label, value, cls, hint) => h("div", { class: "card tile" },
    h("div", { class: "label" }, cls ? h("span", { class: `key ${cls}` }) : null, label),
    h("div", { class: "value" }, ...valueUnit(value)),
    hint ? h("div", { class: "hint" }, hint) : null);
  if (device.kind === "water") {
    tiles.push(tileEl("Waterverbruik", liters(sum(b, "water_m3") != null ? sum(b, "water_m3") * 1000 : null), "gas"));
  } else {
    const imp = sum(b, "import_kwh"), exp = sum(b, "export_kwh");
    const battery = device.kind === "battery";
    tiles.push(tileEl(battery ? "Geladen" : "Afname", energy(imp), "import"));
    if (exp) tiles.push(tileEl(battery ? "Ontladen" : "Teruglevering", energy(exp), "export"));
    if (exp && !battery) tiles.push(tileEl("Netto", energy((imp || 0) - exp), null, (imp || 0) - exp < 0 ? "meer teruggeleverd dan afgenomen" : null));
    const g = sum(b, "gas_m3");
    if (g != null) tiles.push(tileEl("Gas", gas(g), "gas"));
  }
  // the running period is never complete yet; only flag finished periods
  // periods before the first data are not "incomplete": there was no meter yet
  const hasData = (x) => x.import_kwh != null || x.export_kwh != null || x.gas_m3 != null || x.water_m3 != null;
  const firstData = b.findIndex(hasData);
  const incomplete = firstData >= 0 && b.some((x, i) => i >= firstData && !x.complete && (i + 1 < b.length ? new Date(b[i + 1].start) : new Date(res.to)) <= Date.now());
  if (incomplete && b.length) tiles.push(h("div", { class: "card tile" }, h("div", { class: "label" }, "Volledigheid"), h("div", { class: "hint" }, "Niet voor alle kwartieren zijn complete meetgegevens; totalen kunnen iets afwijken.")));
  node.replaceChildren(...tiles);
  node.hidden = tiles.length === 0;
}

function renderRate(card, device, p, res) {
  card.node.hidden = !p.rate;
  if (!p.rate) return;
  const water = device.kind === "water";
  card.title.textContent = water ? "Waterdoorstroming" : "Vermogen";
  card.legend.replaceChildren();
  if (!res || res.error) {
    card.sub.textContent = res?.error || "";
    card.chart.replaceChildren(h("div", { class: "chart-empty" }, "Historie niet beschikbaar."));
    card.table.replaceChildren();
    return;
  }
  const stepMs = res.step_s * 1000;
  const signed = device.kind === "p1" || device.kind === "battery" || device.kind === "kwh";
  card.sub.textContent = water
    ? `Liter per minuut, gemiddeld per ${stepLabel(res.step_s)}`
    : `${signed && device.kind === "p1" ? "Boven 0 = afname, onder 0 = teruglevering. " : ""}Gemiddeld per ${stepLabel(res.step_s)}`;
  const points = res.points.map((pt) => ({ t: new Date(pt.t).getTime() + stepMs / 2, v: pt.avg, min: pt.min, max: pt.max }));
  const unitFmt = water ? (v) => flow(v).join(" ") : (v) => power(v).join(" ");
  lineChart(card.chart, {
    label: card.title.textContent,
    series: [{ name: water ? "Gemiddeld" : "Gemiddeld vermogen", color: water ? "var(--gas)" : "var(--import)", points }],
    from: p.from.getTime(), to: Math.min(p.to.getTime(), Date.now() + stepMs),
    format: (v, pt) => (pt && pt.max !== pt.min ? `${unitFmt(v)} (${unitFmt(pt.min)} – ${unitFmt(pt.max)})` : unitFmt(v)),
    axisFormat: water ? (v) => fmt.n1(v) : (v) => (Math.abs(v) >= 10000 ? `${fmt.n0(v / 1000)}k` : fmt.n0(v)),
    title: (t) => dates.dayTime(new Date(t - stepMs / 2)),
    stepMs, gapMs: Math.max(stepMs * 3, 5 * 60e3),
  });
  card.table.replaceChildren(table(
    ["Tijd", "Gemiddeld", "Min", "Max"],
    res.points.map((pt) => [dates.dayTime(new Date(pt.t)), unitFmt(pt.avg), unitFmt(pt.min), unitFmt(pt.max)]),
  ));
}

function stepLabel(s) {
  if (s < 120) return "minuut";
  if (s < 3600) return `${Math.round(s / 60)} minuten`;
  return `${fmt.n1(s / 3600)} uur`;
}

function renderEnergy(card, gasCard, device, p, res) {
  const water = device.kind === "water";
  const battery = device.kind === "battery";
  card.node.hidden = false;
  card.legend.replaceChildren();
  gasCard.node.hidden = true;
  if (!res || res.error) {
    card.title.textContent = "Energie";
    card.sub.textContent = res?.error || "";
    card.chart.replaceChildren(h("div", { class: "chart-empty" }, "Historie niet beschikbaar."));
    return;
  }
  const per = PER[p.yearly ? "year" : p.bucket];
  const bucket = p.yearly ? "year" : p.bucket;
  const buckets = res.buckets;
  if (water) {
    card.title.textContent = "Waterverbruik per " + per;
    card.sub.textContent = "Liter";
    columnChart(card.chart, {
      label: card.title.textContent,
      buckets: buckets.map((b) => ({ start: b.start, values: { water: b.water_m3 != null ? b.water_m3 * 1000 : null }, complete: b.complete })),
      series: [{ key: "water", name: "Water", color: "var(--gas)", sign: 1 }],
      format: (v) => liters(v).join(" "), axisFormat: (v) => fmt.n0(v),
      title: (b) => bucketTitle(bucket, b.start) + (b.complete ? "" : " (onvolledig)"),
      tickLabel: (b) => bucketTick(bucket, b.start),
    });
    card.table.replaceChildren(table(["Periode", "Liter"], buckets.map((b) => [bucketTitle(bucket, b.start), b.water_m3 != null ? liters(b.water_m3 * 1000).join(" ") : "–"])));
    return;
  }

  const names = battery ? ["Geladen", "Ontladen"] : ["Afname", "Teruglevering"];
  const hasExport = buckets.some((b) => b.export_kwh);
  card.title.textContent = battery ? `Energie per ${per}` : `Stroomverbruik per ${per}`;
  card.sub.textContent = hasExport ? `kWh · ${names[0].toLowerCase()} boven, ${names[1].toLowerCase()} onder de nullijn` : "kWh";
  const series = [{ key: "import", name: names[0], color: "var(--import)", sign: 1 }];
  if (hasExport) series.push({ key: "export", name: names[1], color: "var(--export)", sign: -1 });
  card.legend.replaceChildren(...(series.length > 1 ? series.map((s) => legendItem(s.color, s.name)) : []));
  columnChart(card.chart, {
    label: card.title.textContent,
    buckets: buckets.map((b) => ({ start: b.start, values: { import: b.import_kwh ?? null, export: hasExport ? (b.export_kwh ?? null) : null }, complete: b.complete })),
    series,
    format: (v) => energy(v).join(" "), axisFormat: (v) => fmt.n1(v),
    title: (b) => bucketTitle(bucket, b.start) + (b.complete ? "" : " (onvolledig)"),
    tickLabel: (b) => bucketTick(bucket, b.start),
  });
  const headers = ["Periode", names[0]].concat(hasExport ? [names[1]] : []);
  card.table.replaceChildren(table(headers, buckets.map((b) => [bucketTitle(bucket, b.start), energy(b.import_kwh).join(" ")].concat(hasExport ? [energy(b.export_kwh).join(" ")] : []))));

  if (buckets.some((b) => b.gas_m3 != null)) {
    gasCard.node.hidden = false;
    gasCard.title.textContent = `Gasverbruik per ${per}`;
    gasCard.sub.textContent = "m³ · de slimme meter meldt gas elke 5 minuten (DSMR 5) of elk uur (DSMR 4)";
    gasCard.legend.replaceChildren();
    columnChart(gasCard.chart, {
      label: gasCard.title.textContent,
      buckets: buckets.map((b) => ({ start: b.start, values: { gas: b.gas_m3 ?? null }, complete: b.complete })),
      series: [{ key: "gas", name: "Gas", color: "var(--gas)", sign: 1 }],
      format: (v) => gas(v).join(" "), axisFormat: (v) => fmt.n2(v),
      title: (b) => bucketTitle(bucket, b.start),
      tickLabel: (b) => bucketTick(bucket, b.start),
    });
    gasCard.table.replaceChildren(table(["Periode", "Gas"], buckets.map((b) => [bucketTitle(bucket, b.start), gas(b.gas_m3).join(" ")])));
  }
}

// Grid quality: per-phase voltage, current and power (from minute data, so
// within the retention period) and the meter's sags, swells and outages.
const GRID_KINDS = new Set(["p1", "kwh"]);
const PHASE_COLORS = ["var(--l1)", "var(--l2)", "var(--l3)"];

function eventsCard() {
  const title = h("h3", {}, "Netgebeurtenissen");
  const sub = h("div", { class: "chart-sub" });
  const list = h("ul", { class: "events" });
  const node = h("section", { class: "card chart-card" }, h("div", { class: "chart-head" }, h("div", {}, title, sub)), list);
  return { node, sub, list };
}

function renderGrid(grid, device, p, res) {
  const charts = [grid.voltage, grid.current, grid.power];
  for (const c of [...charts, grid.events]) c.node.hidden = true;
  if (!res) return;
  if (res.error) {
    grid.events.node.hidden = false;
    grid.events.sub.textContent = res.error;
    grid.events.list.replaceChildren();
    return;
  }
  renderEvents(grid.events, device, res.events);
  if (res.phases === 0 || !res.points.length) {
    // older than the minute data (kept HWLOCAL_RETENTION_DAYS): only the events
    if (device.kind === "p1") grid.events.sub.textContent += " Spanning, stroomsterkte en vermogen per fase zijn er alleen voor de bewaartermijn van de minuutgegevens.";
    return;
  }

  const stepMs = res.step_s * 1000;
  const phases = [...Array(res.phases).keys()];
  const per = stepLabel(res.step_s);
  const draw = (card, title, sub, pick, format, axisFormat, extra = {}) => {
    const series = phases.map((i) => ({
      name: `L${i + 1}`, color: PHASE_COLORS[i],
      points: res.points.filter((pt) => pt.phases[i] && pick(pt.phases[i]) != null)
        .map((pt) => ({ t: new Date(pt.t).getTime() + stepMs / 2, v: pick(pt.phases[i]), min: pt.phases[i].v_min, max: pt.phases[i].v_max })),
    }));
    if (!series.some((s) => s.points.length)) return;
    card.node.hidden = false;
    card.title.textContent = title;
    card.sub.textContent = sub;
    card.legend.replaceChildren(...(series.length > 1 ? series.map((s) => legendItem(s.color, s.name)) : []));
    lineChart(card.chart, {
      label: title, series, area: false,
      from: p.from.getTime(), to: Math.min(p.to.getTime(), Date.now() + stepMs),
      format, axisFormat,
      title: (t) => dates.dayTime(new Date(t - stepMs / 2)),
      stepMs, gapMs: Math.max(stepMs * 3, 5 * 60e3), ...extra,
    });
    card.table.replaceChildren(table(["Tijd", ...series.map((s) => s.name)],
      res.points.map((pt) => [dates.dayTime(new Date(pt.t)),
        ...phases.map((i) => (pt.phases[i] && pick(pt.phases[i]) != null ? format(pick(pt.phases[i])) : "–"))])));
  };

  const volt = (v) => `${fmt.n1(v)} V`;
  draw(grid.voltage, res.phases > 1 ? "Spanning per fase" : "Spanning",
    `Gemiddeld per ${per} · normaal tussen 207 en 253 V (230 V ± 10%)`,
    (x) => x.v_avg,
    (v, pt) => (pt && pt.max != null && pt.max !== pt.min ? `${volt(v)} (${fmt.n1(pt.min)} – ${fmt.n1(pt.max)})` : volt(v)),
    (v) => fmt.n0(v), { zero: false });
  draw(grid.current, res.phases > 1 ? "Stroomsterkte per fase" : "Stroomsterkte",
    `Hoogste stroomsterkte per ${per}, bij afname en bij teruglevering`,
    (x) => x.i_max, (v) => `${fmt.n1(v)} A`, (v) => fmt.n0(v));
  // with one phase the power equals the total in the chart above
  if (res.phases > 1) {
    draw(grid.power, "Vermogen per fase", `Boven 0 = afname, onder 0 = teruglevering. Gemiddeld per ${per}`,
      (x) => x.p_avg, (v) => power(v).join(" "), (v) => (Math.abs(v) >= 10000 ? `${fmt.n0(v / 1000)}k` : fmt.n0(v)));
  }
}

const EVENT_LABEL = {
  sag: (e) => `Spanningsdip op L${e.phase}`,
  swell: (e) => `Spanningspiek op L${e.phase}`,
  power_fail: () => "Stroomonderbreking",
  long_power_fail: () => "Lange stroomstoring",
};

function renderEvents(card, device, events) {
  // a kWh meter has no event counters: only show the card when it has events
  if (device.kind !== "p1" && events.length === 0) return;
  card.node.hidden = false;
  const count = (kinds) => events.filter((e) => kinds.includes(e.kind)).reduce((n, e) => n + e.count, 0);
  const parts = [[count(["sag"]), "dip", "dips"], [count(["swell"]), "piek", "pieken"], [count(["power_fail", "long_power_fail"]), "storing", "storingen"]]
    .filter(([n]) => n > 0).map(([n, one, many]) => `${n} ${n === 1 ? one : many}`);
  card.sub.textContent = parts.length
    ? `${parts.join(", ")} in deze periode · de meter telt ze zelf; het tijdstip is wanneer hwlocal de teller zag oplopen`
    : "Geen spanningsdips, -pieken of stroomstoringen in deze periode.";
  const stamp = (d) => `${dates.day(d)} ${dates.time(d)}`;
  const when = (e) => {
    const at = new Date(e.at), since = new Date(e.since);
    // a gap means the gateway or (in an outage) the dongle itself was down
    if (at - since <= 2 * 60e3) return stamp(at);
    const mins = Math.round((at - since) / 60e3);
    const gap = mins < 120 ? `${mins} min` : `${fmt.n1(mins / 60)} uur`;
    const range = dates.day(since) === dates.day(at)
      ? `${dates.day(at)} tussen ${dates.time(since)} en ${dates.time(at)}`
      : `tussen ${stamp(since)} en ${stamp(at)}`;
    return `${range} (${gap} geen gegevens)`;
  };
  card.list.replaceChildren(...events.slice().reverse().map((e) => h("li", {},
    h("span", {}, (EVENT_LABEL[e.kind] || (() => e.kind))(e) + (e.count > 1 ? ` (${e.count}×)` : "")),
    h("span", { class: "when" }, when(e)))));
}
