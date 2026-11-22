// Price card: electricity and gas per quarter hour, today and tomorrow (gas
// prices hold per gas day, 06:00-06:00). With a contract the prices are
// what you pay (incl. VAT, markup and energy tax); without, the market price.
// A part (electricity or gas) under a fixed contract is left out: its market
// price says nothing about what you pay. The cheapest hour is only shown with
// a dynamic electricity contract. With both parts shown a switch picks the
// chart; for electricity a second switch shows quarters or clock hours (the
// average of the hour's quarters, which is what a contract that bills per
// hour charges; it starts on the contract's own choice).
import { columnChart } from "./charts.js";
import { h, api, dates } from "./util.js";

const ct = new Intl.NumberFormat("nl-NL", { minimumFractionDigits: 1, maximumFractionDigits: 1 });
const ctText = (v) => `${ct.format(v)} ct/kWh`;
const m3 = new Intl.NumberFormat("nl-NL", { style: "currency", currency: "EUR", minimumFractionDigits: 3, maximumFractionDigits: 3 });
const gasEur = (v) => `${m3.format(v)}/m³`;
const eur2 = new Intl.NumberFormat("nl-NL", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** Price of a quarter in ct/kWh: what you pay when known, else the market price. */
const priceCt = (p) => p.all_in_ct ?? p.eur_mwh / 10;
const gasPrice = (p) => p.all_in_eur_m3 ?? p.eur_m3;

const isNow = (p, t) => new Date(p.start).getTime() <= t && new Date(p.end).getTime() > t;

/** The contract type ("dynamic", "fixed", or "" without contract) of now, else of the first period. */
function typeNow(rows, t) {
  return (rows.find((p) => isNow(p, t)) || rows[0])?.contract || "";
}

/** The cheapest consecutive hour (4 quarters) that has not ended yet. */
function cheapestHour(prices, now) {
  const q = prices.filter((p) => new Date(p.end).getTime() > now);
  let best = null;
  for (let i = 0; i + 3 < q.length; i++) {
    const win = q.slice(i, i + 4);
    // only consecutive quarter hours
    if (win.some((p, j) => j && new Date(p.start).getTime() !== new Date(win[j - 1].end).getTime())) continue;
    const avg = win.reduce((s, p) => s + priceCt(p), 0) / 4;
    if (!best || avg < best.avg) best = { start: win[0].start, end: win[3].end, avg };
  }
  return best;
}

/**
 * Clock hours from quarters: the average of the hour's four quarters, both
 * market and all-in. An hour that is not complete is left out.
 */
function hours(qs) {
  const out = [];
  for (let i = 0; i < qs.length; i++) {
    const s = new Date(qs[i].start);
    if (s.getMinutes() !== 0) continue;
    const win = qs.slice(i, i + 4);
    const end = s.getTime() + 3600e3;
    if (win.length < 4 || new Date(win[3].end).getTime() !== end) continue;
    const avg = (f) => win.reduce((sum, p) => sum + f(p), 0) / 4;
    const allIn = win.every((p) => p.all_in_ct != null);
    out.push({ ...win[0], end: new Date(end).toISOString(), eur_mwh: avg((p) => p.eur_mwh), all_in_ct: allIn ? avg((p) => p.all_in_ct) : undefined });
  }
  return out;
}

/** Expand hourly periods to quarters so old (hourly) and new data look alike. */
function quarters(prices) {
  const out = [];
  for (const p of prices) {
    const s = new Date(p.start).getTime(), e = new Date(p.end).getTime();
    for (let t = s; t < e; t += 15 * 60e3) out.push({ ...p, start: new Date(t).toISOString(), end: new Date(t + 15 * 60e3).toISOString() });
  }
  return out;
}

const ui = {
  get() { try { return JSON.parse(localStorage.getItem("hw.prices") || "{}"); } catch { return {}; } },
  set(v) { try { localStorage.setItem("hw.prices", JSON.stringify(v)); } catch { /* private mode */ } },
};

const shortDay = (d) => dates.day(d).split(" ").slice(0, 2).join(" ");

export function priceCard() {
  const now = h("div", { class: "value price-now" });
  const nowHint = h("div", { class: "hint" });
  const powerTile = h("div", {}, h("div", { class: "label secondary" }, "Stroom nu"), now, nowHint);
  const cheap = h("div", { class: "value" });
  const cheapHint = h("div", { class: "hint" });
  const cheapTile = h("div", {}, h("div", { class: "label secondary" }, "Goedkoopste uur"), cheap, cheapHint);
  const gasNow = h("div", { class: "value price-now" });
  const gasHint = h("div", { class: "hint" });
  const gasTile = h("div", {}, h("div", { class: "label secondary" }, "Gas nu"), gasNow, gasHint);
  const chart = h("div", { class: "chart" });
  const sub = h("div", { class: "chart-sub" });
  let mode = ui.get().mode === "gas" ? "gas" : "power";
  // "quarter" or "hour"; unset follows the contract (per hour or per quarter)
  let interval = ["quarter", "hour"].includes(ui.get().interval) ? ui.get().interval : null;
  const save = () => ui.set({ mode, ...(interval ? { interval } : {}) });
  const modeBtn = (m, label) => h("button", { type: "button", "data-mode": m, onclick: () => { mode = m; save(); draw(); } }, label);
  const modes = h("div", { class: "segmented", role: "group", "aria-label": "Grafiek", hidden: true },
    modeBtn("power", "Stroom"), modeBtn("gas", "Gas"));
  const intervalBtn = (v, label) => h("button", { type: "button", "data-interval": v, onclick: () => { interval = v; save(); draw(); } }, label);
  const intervals = h("div", { class: "segmented", role: "group", "aria-label": "Stroomprijs per", hidden: true },
    intervalBtn("quarter", "15 min"), intervalBtn("hour", "Uur"));
  const node = h("section", { class: "card chart-card price-card", hidden: true },
    // title and toggle on one row, the explanation (longer for gas) below,
    // so the toggle stays in the same place for both
    h("div", { class: "chart-head price-head" }, h("h3", {}, "Energieprijzen"), h("div", { class: "price-switches" }, modes, intervals)),
    sub,
    h("div", { class: "price-tiles" }, powerTile, gasTile, cheapTile),
    chart);

  let timer = null;
  let data = []; // electricity quarters of today and tomorrow
  let rows = []; // what is shown: data, or its clock hours
  let perHour = false;
  let gas = []; // gas days overlapping today and tomorrow

  const drawPowerTiles = (t, current) => {
    now.textContent = current ? ctText(priceCt(current)) : "–";
    const when = current ? `${dates.short(new Date(current.start))}–${dates.short(new Date(current.end))}` : "geen prijs voor nu";
    nowHint.textContent = current && current.all_in_ct != null ? `markt ${ctText(current.eur_mwh / 10)} · ${when}` : when;
    // per hour: the cheapest clock hour, else any four quarters in a row
    const best = perHour
      ? rows.filter((p) => new Date(p.end).getTime() > t).reduce((b, p) => (!b || priceCt(p) < b.avg ? { start: p.start, end: p.end, avg: priceCt(p) } : b), null)
      : cheapestHour(data, t);
    cheap.textContent = best ? ctText(best.avg) : "–";
    cheapHint.textContent = best ? `${dates.day(new Date(best.start))} ${dates.short(new Date(best.start))}–${dates.short(new Date(best.end))}` : "";
  };

  const drawGasTile = (t) => {
    const cur = gas.find((p) => isNow(p, t));
    const next = cur && gas.find((p) => p.start === cur.end);
    gasNow.textContent = cur ? gasEur(gasPrice(cur)) : "–";
    const parts = [];
    if (cur?.all_in_eur_m3 != null) parts.push(`markt ${gasEur(cur.eur_m3)}`);
    if (!cur) parts.push("geen prijs voor nu");
    else parts.push(next ? `morgen vanaf 06:00 ${gasEur(gasPrice(next))}` : `gasdag tot ${dates.short(new Date(cur.end))}`);
    gasHint.textContent = parts.join(" · ");
  };

  /** One column per quarter hour, today and (when known) tomorrow, the same for both parts. */
  const drawChart = (rows, current, o) => {
    const idx = current ? rows.indexOf(current) : -1;
    columnChart(chart, {
      label: o.label,
      buckets: rows.map((p, i) => ({ start: p.start, now: i === idx, values: { price: o.value(p) } })),
      series: [{ key: "price", name: o.allIn ? "Jouw prijs" : "Marktprijs", color: o.color, sign: 1 }],
      format: o.format,
      axisFormat: o.axisFormat,
      signedAxis: true,
      title: (b) => `${dates.day(new Date(b.start))} ${dates.short(new Date(b.start))}${b.now ? " (nu)" : ""}`,
      tickLabel: (b) => {
        const d = new Date(b.start);
        return d.getMinutes() === 0 && d.getHours() % 6 === 0 ? (d.getHours() === 0 ? shortDay(d) : `${String(d.getHours()).padStart(2, "0")}:00`) : null;
      },
      labelEvery: 1,
      markerIndex: idx,
      markerColor: "var(--export)",
      markerLabel: "nu",
      dimBeforeMarker: true,
      height: 180,
    });
  };

  const drawPowerChart = (current, contractHourly) => {
    const allIn = rows.some((p) => p.all_in_ct != null);
    const tomorrow = new Date(data[data.length - 1].start).getDate() !== new Date().getDate();
    const span = tomorrow ? "vandaag en morgen" : "vandaag (morgen na ± 13:00)";
    const unit = perHour ? "uur" : "kwartier", units = perHour ? "uren" : "kwartieren";
    const avg = perHour ? " Een uurprijs is het gemiddelde van de vier kwartierprijzen." : "";
    // per quarter under a contract that bills per hour: each quarter costs its hour's average
    const billed = allIn && contractHourly && !perHour ? " Je contract rekent per uur af: elk kwartier kost het gemiddelde van zijn uur." : "";
    sub.textContent = (allIn
      ? `Stroom: wat je betaalt per ${unit}, ${span}: marktprijs met btw, opslag en energiebelasting volgens je contract.`
      : `Stroom: day-ahead marktprijs per ${unit}, ${span}. Exclusief opslag, energiebelasting en btw (voeg een contract toe onder Verbruik en Kosten).`) +
      avg + billed + ` Oranje is nu; verstreken ${units} zijn lichter.`;
    drawChart(rows, current, { label: `Stroomprijs per ${unit}`, value: priceCt, allIn, color: "var(--accent)", format: ctText, axisFormat: (v) => ct.format(v) });
  };

  /**
   * Gas per quarter hour over the same calendar days as electricity: a gas
   * day runs 06:00-06:00, so until 06:00 the price of yesterday's gas day
   * applies. Tomorrow is shown once its gas day is known.
   */
  const drawGasChart = (t) => {
    const today = new Date(t);
    today.setHours(0, 0, 0, 0);
    const tomorrowStart = new Date(today);
    tomorrowStart.setDate(tomorrowStart.getDate() + 1);
    const tomorrow = gas.some((p) => new Date(p.start).getTime() >= tomorrowStart.getTime());
    const end = new Date(tomorrowStart);
    if (tomorrow) end.setDate(end.getDate() + 1);
    const rows = quarters(gas).filter((p) => {
      const s = new Date(p.start).getTime();
      return s >= today.getTime() && s < end.getTime();
    });
    const allIn = rows.some((p) => p.all_in_eur_m3 != null);
    const span = tomorrow ? "vandaag en morgen" : "vandaag (morgen zodra de prijs bekend is)";
    sub.textContent = (allIn
      ? `Gas: wat je betaalt per kwartier, ${span}: marktprijs met btw, opslag en energiebelasting volgens je contract.`
      : `Gas: marktprijs per kwartier, ${span}. Exclusief opslag, energiebelasting en btw (voeg een contract toe onder Verbruik en Kosten).`) +
      " De prijs geldt per gasdag (06:00–06:00), tot 06:00 dus die van de dag ervoor. Oranje is nu; verstreken kwartieren zijn lichter.";
    drawChart(rows, rows.find((p) => isNow(p, t)), { label: "Gasprijs per kwartier", value: gasPrice, allIn, color: "var(--gas)", format: gasEur, axisFormat: (v) => eur2.format(v) });
  };

  const draw = () => {
    const t = Date.now();
    const powerType = typeNow(data, t), gasType = typeNow(gas, t);
    const shown = { power: data.length > 0 && powerType !== "fixed", gas: gas.length > 0 && gasType !== "fixed" };
    node.hidden = !shown.power && !shown.gas;
    if (node.hidden) return;
    powerTile.hidden = !shown.power;
    gasTile.hidden = !shown.gas;
    // the cheapest hour only matters when what you pay follows the market
    cheapTile.hidden = !(shown.power && powerType === "dynamic");
    modes.hidden = !(shown.power && shown.gas);
    const m = shown[mode] ? mode : (shown.power ? "power" : "gas");
    for (const b of modes.querySelectorAll("button")) b.setAttribute("aria-pressed", String(b.dataset.mode === m));
    // per quarter or per clock hour, for electricity only
    const contractHourly = !!(data.find((p) => isNow(p, t)) || data[0])?.hourly;
    perHour = (interval || (contractHourly ? "hour" : "quarter")) === "hour";
    intervals.hidden = m !== "power";
    for (const b of intervals.querySelectorAll("button")) b.setAttribute("aria-pressed", String((b.dataset.interval === "hour") === perHour));
    rows = perHour ? hours(data) : data;
    if (!rows.length) rows = data;
    const current = rows.find((p) => isNow(p, t));
    if (shown.power) drawPowerTiles(t, current);
    if (shown.gas) drawGasTile(t);
    if (m === "power") drawPowerChart(current, contractHourly); else drawGasChart(t);
  };

  const load = async () => {
    try {
      // today and tomorrow; for gas also the gas day that started yesterday 06:00
      const res = await api("GET", "/api/prices");
      data = quarters(res.prices);
      gas = res.gas || [];
      draw();
    } catch {
      /* keep the last data; the connection indicator shows problems */
    }
  };

  const start = () => {
    load();
    // reload (and so redraw the current quarter) at every quarter hour
    const tick = () => {
      const ms = 15 * 60e3 - (Date.now() % (15 * 60e3)) + 1000;
      timer = setTimeout(() => { load(); tick(); }, ms);
    };
    tick();
  };
  const stop = () => clearTimeout(timer);
  return { node, start, stop };
}
