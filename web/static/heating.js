// Heating from mijn.ista.nl (the ista plugin): the panel "Verwarming" of
// "Verbruik en Kosten". The choices on top are the tab's own (usage.js), as
// for energy and water: the room (in the place of the device), the period
// and the view. Views: Verbruik (usage per room; for one day, Gisteren, a
// column per room) and Kosten (costs of the chosen days, the advance of the
// settlement period and the heating contracts, one per settlement period).
// Plus a card for the dashboard.
import { columnChart } from "./charts.js";
import { state, subscribe } from "./app.js";
import { h, api, dates } from "./util.js";
import { eur, chartCard, legendItem, tile, advanceBlock, numberInput, field, advanceEditor, percentagesNode, resultText } from "./costs.js";

const nf0 = new Intl.NumberFormat("nl-NL", { maximumFractionDigits: 0 });
const nf1 = new Intl.NumberFormat("nl-NL", { maximumFractionDigits: 1 });
const nf2 = new Intl.NumberFormat("nl-NL", { maximumFractionDigits: 2 });
const nf6 = new Intl.NumberFormat("nl-NL", { minimumFractionDigits: 6, maximumFractionDigits: 6 });
const shortDate = new Intl.DateTimeFormat("nl-NL", { day: "numeric", month: "short" });
const longDate = new Intl.DateTimeFormat("nl-NL", { day: "numeric", month: "long", year: "numeric" });
const monthFmt = new Intl.DateTimeFormat("nl-NL", { month: "short", year: "2-digit" });
const monthLong = new Intl.DateTimeFormat("nl-NL", { month: "long", year: "numeric" });

const parseDate = (s) => { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d); };
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const addDays = (d, n) => { const x = new Date(d); x.setDate(x.getDate() + n); return x; };
const num = (v, f = nf0) => (v == null ? "–" : f.format(v));
export const periodName = (p) => `${p.start.slice(0, 4)}/${p.end.slice(2, 4)}`;
// When ista is behind: the last day it measured, if that is before yesterday
// (today is always still running). ista's estimates for the days after it
// are never shown; the gateway fetches the last 14 days again every day, so
// those days appear once ista has read the radiators.
const staleSince = (addr) => (addr?.last_measured && addr.last_measured < iso(addDays(new Date(), -1)) ? addr.last_measured : null);
const staleNotice = (lm, range) => (lm && range.to > lm
  ? h("p", { class: "notice small" }, `ista heeft de radiatoren na ${longDate.format(parseDate(lm))} nog niet uitgelezen. `
    + "De dagen daarna verschijnen zodra ista de standen heeft.")
  : null);
const sortedPeriods = (addr) => [...(addr?.periods || [])].sort((a, b) => b.y - a.y); // newest first

/** One line about an account's sync, for this panel and the plugin settings. */
export function istaStatusText(a) {
  if (!a) return "";
  if (a.state === "syncing") {
    if (!(a.todo > 0)) return "Bijwerken…";
    const left = a.remaining_s > 0 ? `, nog ongeveer ${a.remaining_s < 90 ? "een minuut" : `${Math.round(a.remaining_s / 60)} minuten`}` : "";
    return `Gegevens ophalen: ${a.done} van ${a.todo} dagen${left}. Nieuwste dagen eerst; na een herstart gaat het verder waar het was.`;
  }
  if (a.state === "auth_failed") return "ista weigerde het wachtwoord: pas het aan onder Instellingen › Plugins. Tot dan probeert de gateway het niet opnieuw.";
  if (a.state === "error") return `Bijwerken mislukt (${a.message}); over een half uur opnieuw.`;
  if (a.last_sync) return `Bijgewerkt ${dates.dayTime(new Date(a.last_sync))}; elke ochtend opnieuw.`;
  return "Wacht op de eerste keer ophalen.";
}

/** Whether there is an ista account (a plugin device of type ista). */
export function hasHeating() {
  return [...state.plugins.values()].some((d) => d.plugin_type === "ista");
}

/** A room's name, with its radiator number when two rooms share a name. */
export function roomName(r, addr) {
  const same = addr.rooms.filter((x) => x.name === r.name).length > 1;
  return same ? `${r.name} (radiator ${r.rad_nr})` : r.name;
}

/**
 * The days of a period choice of the tab for heating, `offset` periods back:
 * {from, to} (inclusive days, "YYYY-MM-DD"), the bucket of the usage and
 * costs, a label, and y, the settlement period the advance shows (the one
 * holding the last day, else the nearest one). Today is not offered: ista's
 * readings are per day and come in late; "yesterday" (one day, `offset`
 * days back) says so when ista does not have it yet. id "contract" is a
 * settlement period.
 */
export function heatingRange(id, offset, addr) {
  const now = new Date();
  const today0 = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const ps = sortedPeriods(addr);
  let r;
  switch (id) {
    case "yesterday": {
      const day = addDays(today0, -1 - offset);
      r = { from: day, to: day, bucket: "day", label: dates.day(day), day: true };
      break;
    }
    case "week": {
      const to = addDays(today0, -7 * offset);
      const from = addDays(to, -6);
      r = { from, to, bucket: "day", label: `${dates.day(from)} – ${dates.day(to)}` };
      break;
    }
    case "year": {
      const from = new Date(now.getFullYear() - offset, 0, 1);
      r = { from, to: new Date(from.getFullYear(), 11, 31), bucket: "month", label: String(from.getFullYear()) };
      break;
    }
    case "contract": {
      const p = ps[Math.min(offset, ps.length - 1)];
      if (!p) return null;
      r = { from: parseDate(p.from), to: parseDate(p.end), bucket: "month", label: `Afrekenperiode ${periodName(p)}`, period: p };
      break;
    }
    case "all": {
      const first = ps[ps.length - 1], last = ps[0];
      if (!first) return null;
      r = { from: parseDate(first.from), to: parseDate(last.end), bucket: "period", label: "Alle afrekenperiodes" };
      break;
    }
    default: { // month
      const from = new Date(now.getFullYear(), now.getMonth() - offset, 1);
      r = { from, to: new Date(from.getFullYear(), from.getMonth() + 1, 0), bucket: "day", label: monthLong.format(from) };
    }
  }
  const end = iso(r.to < today0 ? r.to : today0);
  const p = r.period || ps.find((x) => end >= x.start && end <= x.end) || ps.find((x) => x.end < end) || ps[ps.length - 1];
  return { ...r, from: iso(r.from), to: iso(r.to), y: p?.y || 0, settlement: p || null, first: addr?.move_in || "" };
}

const prefs = {
  get() { try { return JSON.parse(localStorage.getItem("hw.heating") || "{}"); } catch { return {}; } },
  set(v) { try { localStorage.setItem("hw.heating", JSON.stringify({ ...this.get(), ...v })); } catch { /* private mode */ } },
};

function segmented(label, items, onPick) {
  const g = h("div", { class: "segmented", role: "group", "aria-label": label });
  for (const [id, text] of items) g.append(h("button", { type: "button", "data-id": id, onclick: () => onPick(id) }, text));
  return g;
}
const press = (group, id) => { for (const b of group.querySelectorAll("button")) b.setAttribute("aria-pressed", String(b.dataset.id === id)); };

const bucketTitle = (bucket, b) => (b.start === b.end ? longDate.format(parseDate(b.start))
  : bucket === "month" && b.start.endsWith("-01") ? monthLong.format(parseDate(b.start))
    : `${shortDate.format(parseDate(b.start))} – ${longDate.format(parseDate(b.end))}`);
const bucketTick = (bucket, b) => {
  const d = parseDate(b.start);
  if (bucket === "day") return String(d.getDate());
  if (bucket === "week") return shortDate.format(d);
  if (bucket === "month") return monthFmt.format(d);
  return `${b.start.slice(0, 4)}/${b.end.slice(2, 4)}`;
};

/**
 * The panel. onChange is called when the addresses (rooms, periods) are
 * loaded or change, so the tab can draw its choices; address() returns the
 * current one; load({room, view, range}) shows a view.
 */
export function heatingPanel(onChange) {
  const saved = prefs.get();
  const ui = { measure: saved.measure === "ticks" ? "ticks" : "units", cuid: saved.cuid || "", last: null };
  const save = () => prefs.set({ measure: ui.measure, cuid: ui.cuid });
  let overview = null;

  const status = h("p", { class: "secondary ista-status" });
  const syncBtn = h("button", { class: "btn", type: "button" }, "Nu bijwerken");
  const addressSelect = h("select", { class: "select", "aria-label": "Adres", hidden: true, onchange: () => { ui.cuid = addressSelect.value; save(); onChange?.(); } });
  const body = h("div", { class: "usage-panel" });
  const formSlot = h("div");
  const node = h("div", {}, h("div", { class: "row ista-status-row" }, addressSelect, status, syncBtn), body);

  syncBtn.addEventListener("click", async () => {
    syncBtn.disabled = true;
    try { await api("POST", "/api/ista/sync"); } catch { /* shown by the status */ }
    setTimeout(() => { syncBtn.disabled = false; loadOverview(); }, 1500);
  });

  let seq = 0;
  let unsubscribe = null;
  let statusTimer = null;

  async function loadOverview() {
    try { overview = await api("GET", "/api/ista"); } catch (e) { overview = { error: e.message, accounts: [], addresses: [] }; }
    const list = overview.addresses || [];
    addressSelect.hidden = list.length < 2;
    addressSelect.replaceChildren(...list.map((a) => h("option", { value: a.cuid, selected: a.cuid === current()?.cuid }, `${a.address}, ${a.city}`)));
    drawStatus();
    return overview;
  }

  function drawStatus() {
    const acc = overview?.accounts || [];
    const addr = current();
    const parts = [];
    if (addr?.last_measured) parts.push(`Gemeten t/m ${longDate.format(parseDate(addr.last_measured))}`);
    for (const a of acc) parts.push(istaStatusText(a));
    status.textContent = parts.join(" · ") || "Nog geen gegevens.";
    // poll while a sync runs, so the progress moves
    clearTimeout(statusTimer);
    if (acc.some((a) => a.state === "syncing")) {
      statusTimer = setTimeout(async () => {
        const before = current()?.last_measured;
        await loadOverview();
        if (current()?.last_measured !== before) onChange?.();
      }, 5000);
    }
  }

  function current() {
    const list = overview?.addresses || [];
    return list.find((a) => a.cuid === ui.cuid) || list[0] || null;
  }

  async function load(opts) {
    ui.last = opts;
    const my = ++seq;
    if (!overview) await loadOverview();
    if (my !== seq) return;
    const addr = current();
    if (!addr || !addr.periods.length || !opts.range) {
      body.replaceChildren(h("div", { class: "notice" }, h("h2", {}, "Nog geen verwarmingsgegevens"),
        h("p", {}, "De gegevens van ista worden opgehaald zodra je account onder Instellingen › Plugins is toegevoegd. De eerste keer haalt de gateway alle dagen sinds je intrek op; dat duurt even.")));
      return;
    }
    ui.cuid = addr.cuid;
    if (opts.view === "kosten") await drawCosts(addr, opts, my);
    else await drawUsage(addr, opts, my);
  }
  const reload = () => { if (ui.last) load(ui.last); };

  function measureToggle() {
    const g = segmented("Kamers in", [["units", "Eenheden"], ["ticks", "Tikken"]], (id) => { ui.measure = id; save(); reload(); });
    press(g, ui.measure);
    return g;
  }

  const fail = (my, e) => { if (my === seq) body.replaceChildren(h("div", { class: "notice" }, h("p", {}, e.message))); };
  const q = (addr) => `cuid=${encodeURIComponent(addr.cuid)}`;

  // ---------- Verbruik ----------
  async function drawUsage(addr, { room, range }, my) {
    // one day that ista does not have yet: say so instead of an empty chart
    if (range.day && (!addr.last_measured || addr.last_measured < range.from)) {
      body.replaceChildren(h("div", { class: "notice" }, h("p", {},
        `ista heeft ${longDate.format(parseDate(range.from))} nog niet gemeten. `
        + (addr.last_measured ? `De laatst gemeten dag is ${longDate.format(parseDate(addr.last_measured))}; ga met ‹ terug.` : "Er zijn nog geen standen."))));
      return;
    }
    let u;
    try {
      u = await api("GET", `/api/ista/usage?${q(addr)}&from=${range.from}&to=${range.to}&bucket=${range.bucket}`);
    } catch (e) { fail(my, e); return; }
    if (my !== seq) return;
    const ri = room ? u.rooms.findIndex((r) => String(r.id) === room) : -1;
    const ticks = ui.measure === "ticks";
    const unit = ticks ? "tikken" : (u.unit || "eenheden");
    const own = (b) => {
      const arr = ticks ? b.ticks : b.units;
      if (ri >= 0) return arr[ri];
      if (!ticks) return b.total;
      return arr.some((v) => v != null) ? arr.reduce((s, v) => s + (v || 0), 0) : null;
    };
    const whole = ri < 0 && !ticks; // the comparisons are for the address's units
    const series = [{ key: "own", name: ri >= 0 ? roomName(u.rooms[ri], addr) : "Verbruik", color: "var(--export)", sign: 1 }];
    if (whole) {
      series.push({ key: "last", name: "Vorig jaar", color: "var(--fixed)", sign: 1 });
      series.push({ key: "building", name: "Gebouwgemiddelde", color: "var(--total)", sign: 1 });
    }
    const lm = staleSince(addr);
    const buckets = u.buckets.map((b) => ({ ...b, complete: !b.missing, values: { own: own(b), last: whole ? b.last_year : null, building: whole ? b.building : null } }));

    const t = u.total;
    const tiles = [tile(ri >= 0 ? roomName(u.rooms[ri], addr) : "Totaal", h("span", {}, num(own(t)), h("small", {}, ` ${unit}`)),
      !t.days ? "nog geen standen in deze periode"
        : t.missing ? `${t.missing} van ${t.days} dagen zonder stand`
        : `${t.days} ${t.days === 1 ? "dag" : "dagen"}`)];
    if (whole && t.last_year != null) {
      const diff = t.last_year ? Math.round((t.total - t.last_year) / t.last_year * 100) : null;
      tiles.push(tile("Zelfde dagen vorig jaar", h("span", {}, num(t.last_year), h("small", {}, ` ${unit}`)), diff == null ? null : `${diff > 0 ? "+" : ""}${diff}% nu`));
    }
    if (whole && t.building != null) {
      tiles.push(tile("Gebouwgemiddelde", h("span", {}, num(t.building), h("small", {}, ` ${unit}`)), "voor een woning als de jouwe"));
    }

    const card = chartCard();
    card.title.textContent = "Verbruik";
    card.sub.textContent = `${{ day: "Per dag", week: "Per week (ma–zo)", month: "Per maand", period: "Per afrekenperiode" }[range.bucket]}, ${unit}.`;
    card.legend.replaceChildren(...series.map((s) => legendItem(s.color, s.name)), measureToggle());
    // the table: per bucket every room, the total and the comparisons
    const fmtRoom = (v) => num(v, ticks ? nf1 : nf0);
    const sumTicks = (b) => (b.ticks.some((v) => v != null) ? b.ticks.reduce((s, v) => s + (v || 0), 0) : null);
    const head = h("tr", {}, h("th", {}, "Periode"), ...u.rooms.map((r) => h("th", {}, roomName(r, addr))),
      h("th", {}, "Totaal"), ...(ticks ? [] : [h("th", {}, "Vorig jaar"), h("th", {}, "Gebouw")]));
    const row = (b, name) => h("tr", {}, h("td", {}, name),
      ...u.rooms.map((_, i) => h("td", {}, fmtRoom((ticks ? b.ticks : b.units)[i]))),
      h("td", {}, h("strong", {}, num(ticks ? sumTicks(b) : b.total, ticks ? nf1 : nf0))),
      ...(ticks ? [] : [h("td", {}, num(b.last_year)), h("td", {}, num(b.building))]));
    card.table.replaceChildren(
      h("p", { class: "secondary" }, ticks
        ? "Tikken: wat de meter op de radiator telde. Eén tik is niet in elke kamer evenveel waard."
        : "Eenheden: tikken × de factor van de radiator, min de korting voor de ligging van de kamer. Hierop wordt afgerekend."),
      h("table", { class: "data first-nowrap" },
        h("thead", {}, head),
        h("tbody", {}, ...[...u.buckets].reverse().map((b) => row(b, bucketTitle(range.bucket, b) + (b.missing ? " *" : "")))),
        h("tfoot", {}, row(t, "Totaal"))),
      u.buckets.some((b) => b.missing) ? h("p", { class: "hint" }, "* niet voor elke dag een stand") : "");

    body.replaceChildren(...[staleNotice(lm, range), h("section", { class: "summary", "aria-label": "Totalen" }, ...tiles), card.node].filter(Boolean));
    if (range.day) {
      // one day: a column per room (the chosen room only, when there is one)
      const day = u.buckets[0] || t;
      const idx = ri >= 0 ? [ri] : u.rooms.map((_, i) => i);
      card.sub.textContent = `Per kamer, ${unit}.`;
      card.legend.replaceChildren(legendItem("var(--export)", "Verbruik"), measureToggle());
      columnChart(card.chart, {
        buckets: idx.map((i) => ({ name: roomName(u.rooms[i], addr), complete: !day.missing, values: { own: (ticks ? day.ticks : day.units)[i] } })),
        series: [{ key: "own", name: "Verbruik", color: "var(--export)", sign: 1 }], label: `Verbruik per kamer, ${unit}`,
        format: (v) => `${num(v, ticks ? nf1 : nf0)} ${unit}`, axisFormat: (v) => num(v),
        tickLabel: (b) => b.name, title: (b) => b.name, markIncomplete: true,
      });
      return;
    }
    columnChart(card.chart, {
      buckets, series, label: `Verbruik, ${unit}`,
      format: (v) => `${num(v, ticks ? nf1 : nf0)} ${unit}`, axisFormat: (v) => num(v),
      tickLabel: (b) => bucketTick(range.bucket, b), title: (b) => bucketTitle(range.bucket, b), markIncomplete: true,
    });
  }

  // ---------- Kosten ----------
  async function drawCosts(addr, { range }, my) {
    let r, c, cl;
    try {
      [r, c, cl] = await Promise.all([
        api("GET", `/api/ista/costs?${q(addr)}&from=${range.from}&to=${range.to}&bucket=${range.bucket}`),
        api("GET", `/api/ista/costs?${q(addr)}&y=${range.y}`),
        api("GET", `/api/ista/contracts?${q(addr)}`),
      ]);
    } catch (e) { fail(my, e); return; }
    if (my !== seq) return;
    const unit = r.unit || "eenheden";
    const t = r.total;
    const contracts = cl.contracts || [];
    // the outcome of each settled period (settlement minus the advance paid), from the same costs
    const settled = Object.fromEntries(await Promise.all(contracts.filter((k) => k.final && k.advance?.length).map(async (k) =>
      [k.y, k.y === c.period?.y ? c : await api("GET", `/api/ista/costs?${q(addr)}&y=${k.y}`).catch(() => null)])));
    if (my !== seq) return;
    // the costs only count the days ista measured; its estimates after them are left out

    const tiles = [
      tile("Totaal", eur(t.total), t.units != null ? `${num(t.units)} ${unit}${t.end ? `, t/m ${longDate.format(parseDate(t.end))}` : ""}` : t.days ? null : "nog geen gemeten standen in deze periode"),
      tile("Verbruik", eur(t.variable), "eenheden × prijs per eenheid", "export"),
      tile("Vaste kosten", eur(t.fixed), "vloeroppervlak × prijs per dm², per dag naar rato", "fixed"),
    ];
    if (t.unpriced || t.missing) {
      const parts = [];
      if (t.unpriced) parts.push(`${t.unpriced} ${t.unpriced === 1 ? "dag" : "dagen"} zonder prijzen (vul een contract in)`);
      if (t.missing) parts.push(`${t.missing} ${t.missing === 1 ? "dag" : "dagen"} zonder stand`);
      tiles.push(h("div", { class: "card tile" }, h("div", { class: "label" }, "Onvolledig"), h("div", { class: "hint" }, `${parts.join(", ")}. De bedragen zijn daardoor onvolledig.`)));
    }

    const card = chartCard();
    card.title.textContent = "Kosten";
    card.sub.textContent = `${{ day: "Per dag", week: "Per week", month: "Per maand", period: "Per afrekenperiode" }[range.bucket]}, tot de laatste dag die ista gemeten heeft. Vooral een schatting zolang de afrekening er niet is.`;
    const series = [
      { key: "variable", name: "Verbruik", color: "var(--export)", sign: 1 },
      { key: "fixed", name: "Vaste kosten", color: "var(--fixed)", sign: 1 },
      { key: "total", name: "Totaal", color: "var(--total)", sign: 1, total: true },
    ];
    card.legend.replaceChildren(...series.map((s) => legendItem(s.color, s.name)));
    card.table.replaceChildren(h("table", { class: "data" },
      h("thead", {}, h("tr", {}, ...["Periode", "Verbruik", "Verbruik", "Vast", "Totaal"].map((x, i) => h("th", { scope: "col" }, i === 1 ? unit : x)))),
      h("tbody", {}, ...r.buckets.map((b) => h("tr", {},
        h("td", {}, bucketTitle(range.bucket, b) + (b.missing || b.unpriced ? " *" : "")),
        h("td", {}, num(b.units)), h("td", {}, eur(b.variable)), h("td", {}, eur(b.fixed)), h("td", {}, h("strong", {}, eur(b.total))))))));

    const advance = h("section", { class: "card advance-card", "aria-label": "Voorschot" }, ...advanceChildren(c));
    const list = h("div", { class: "contract-list" }, ...(contracts.length
      ? [...contracts].reverse().map((k) => contractCard(addr, k, settled[k.y]))
      : [h("div", { class: "notice" }, "Nog geen contracten.")]));
    const free = sortedPeriods(addr).filter((p) => !contracts.some((k) => k.y === p.y));
    const addBtn = h("button", { class: "btn primary", type: "button", hidden: !free.length, onclick: () => openForm(addr, null, contracts) }, "Contract toevoegen");
    formSlot.replaceChildren();

    body.replaceChildren(
      ...(contracts.length ? [] : [h("div", { class: "notice" }, h("h2", {}, "Nog geen verwarmingscontract"),
        h("p", {}, "Voeg hieronder een contract toe met het vloeroppervlak en, als je die hebt, de prijzen van je ista-afrekening. Daarmee rekent de app je kosten en je voorschot uit."))]),
      ...[staleNotice(staleSince(addr), range)].filter(Boolean),
      h("section", { class: "summary", "aria-label": "Totalen" }, ...tiles),
      card.node, advance,
      h("h2", { class: "section-title" }, "Verwarmingscontracten"),
      h("p", { class: "secondary contract-help" }, "Eén contract per afrekenperiode van ista (1 juli t/m 30 juni). Bedragen zoals op de jaarafrekening van ista. "
        + "Zolang de afrekening er niet is, rekent de app met de prijzen die je invult, of met die van de laatste ingevoerde afrekening."),
      list, h("div", { class: "row" }, addBtn), formSlot);
    columnChart(card.chart, {
      label: "Stookkosten per periode",
      buckets: r.buckets.map((b) => ({ start: b.start, end: b.end, values: { variable: b.variable, fixed: b.fixed, total: b.total }, complete: !b.missing && !b.unpriced })),
      series,
      format: (v) => eur(v),
      axisFormat: (v) => `€ ${nf2.format(v)}`,
      title: (b) => bucketTitle(range.bucket, b) + (b.missing || b.unpriced ? " (onvolledig)" : ""),
      tickLabel: (b) => bucketTick(range.bucket, b),
      markIncomplete: true,
    });
  }

  /** The advance card of a settlement period, as for water; without an advance the forecast alone. */
  function advanceChildren(c) {
    const p = c.period;
    const unit = c.unit || "eenheden";
    const k = c.contract;
    const head = (title) => h("div", { class: "chart-head" }, h("div", {}, h("h3", {}, title),
      h("div", { class: "chart-sub" }, `ista · afrekenperiode ${longDate.format(parseDate(p.start))} t/m ${longDate.format(parseDate(p.end))}`
        + (c.monthly ? ` · nu ${eur(c.monthly)} per maand` : ""))));
    const extra = [];
    if (c.unit_price != null) {
      const src = { settlement: "uit de afrekening", contract: "uit het contract", previous: `uit de afrekening ${c.price_from - 1}/${String(c.price_from).slice(2)}${k?.correction_pct ? `, ${k.correction_pct > 0 ? "+" : ""}${k.correction_pct}%` : ""}` }[c.price_source] || "";
      extra.push(h("p", { class: "hint" }, `Prijs per eenheid € ${nf6.format(c.unit_price)} · per dm² € ${nf6.format(c.area_price)} (${src})`
        + (c.fixed_period != null && !c.final ? `. Vaste deel ${eur(c.fixed_period)} voor ${num(c.area_dm2)} dm²${c.period.from !== c.period.start ? ", naar rato van de dagen sinds je intrek" : ""}.` : ".")));
    }
    if (c.check) {
      extra.push(h("p", { class: c.check.ok ? "hint" : "form-error" },
        `Controle: ${eur(c.check.variable)} variabel + ${eur(c.check.fixed)} vast${c.check.prorated ? " (naar rato van de dagen)" : ""} = ${eur(c.check.computed)}`
        + (c.check.stated ? (c.check.ok ? ", gelijk aan de afrekening." : `, de afrekening zegt ${eur(c.check.stated)}. Kijk de ingevulde getallen na.`) : ".")));
    }
    if (c.unit_price == null) {
      return [head("Afrekenperiode " + periodName(p)),
        h("p", {}, "Nog geen prijzen voor deze periode: voeg een contract toe met de prijzen van je ista-afrekening, of voer de afrekening van een eerdere periode in.")];
    }
    const coverage = c.previous_coverage != null ? `, ${num(c.previous_coverage * 100)}% gemeten` : "";
    const method = c.final ? "" : ({
      last_year: `Prognose voor de rest van de periode: ${num(c.units_rest)} ${unit}, uit je verbruik van dezelfde dagen van de vorige periode${coverage}.`,
      estimate: `Prognose voor de rest van de periode: ${num(c.units_rest)} ${unit}, uit je verwachte jaarverbruik (${num(k?.estimate_units)} ${unit}, zoals ingevuld bij het contract${c.units_previous != null ? `; vorige periode ${num(c.units_previous)} ${unit}` : ""}) verdeeld met ${c.profile === "building" ? "het stookpatroon van het gebouw" : "een standaard stookprofiel"}.`,
    }[c.rest_basis] || "")
      + (c.cost_high != null && c.cost_low != null && c.cost_high - c.cost_low >= 1
        ? ` De marge houdt rekening met afwijkingen in je verbruik${c.price_source === "previous" ? " en in de prijs, die nog uit de vorige afrekening komt" : ""}.` : "");
    const noForecast = c.forecast_error || "Nog geen prognose: vul bij het contract een verwacht jaarverbruik in.";
    if (k?.advance?.length) {
      const all = c.paid + c.to_pay;
      const ended = c.final;
      const a = {
        contract_name: "ista", contracts: 1, from: p.start, to: iso(addDays(parseDate(p.end), 1)), now: ended ? iso(addDays(parseDate(p.end), 1)) : new Date().toISOString(),
        monthly_eur: c.monthly, paid_eur: c.paid, payments: c.payments, next_payment: c.next_payment,
        cost_eur: c.cost_so_far ?? 0, balance_eur: c.paid - (c.cost_so_far ?? 0), estimated_days: 0,
        method_text: method,
        percentages: c.percentages,
        forecast: c.cost_expected != null ? {
          result_eur: all - c.cost_expected,
          low_eur: all - (c.cost_high ?? c.cost_expected), high_eur: all - (c.cost_low ?? c.cost_expected),
          advice_monthly_eur: c.advice ?? null, remaining_payments: c.payments_left,
          fixed_cost_eur: c.fixed_expected ?? null, usage_cost_eur: c.variable_expected ?? null,
        } : null,
        forecast_error: c.cost_expected == null ? noForecast : null,
      };
      return [...advanceBlock(a, false), ...extra];
    }
    return [head(c.final ? "Afrekening" : "Prognose"),
      h("div", { class: "price-tiles" },
        h("div", {}, h("div", { class: "label secondary" }, "Kosten tot nu toe"), h("div", { class: "value" }, eur(c.cost_so_far)),
          h("div", { class: "hint" }, c.last_day ? `t/m ${longDate.format(parseDate(c.last_day))}, ${num(c.units_so_far)} ${unit}` : "nog geen standen")),
        h("div", {}, h("div", { class: "label secondary" }, c.final ? "Afrekening" : "Verwacht voor de periode"), h("div", { class: "value" }, eur(c.cost_expected)),
          h("div", { class: "hint" }, c.final ? "" : c.cost_low != null && c.cost_high - c.cost_low >= 1 ? `tussen ${eur(c.cost_low)} en ${eur(c.cost_high)}` : c.units_expected != null ? `${num(c.units_expected)} ${unit}` : "")),
        ...(c.units_previous != null && !c.final ? [h("div", {}, h("div", { class: "label secondary" }, "Vorige periode"), h("div", { class: "value" }, `${num(c.units_previous)}`), h("div", { class: "hint" }, `${unit}, hele periode`))] : [])),
      ...(method ? [h("p", { class: "hint" }, method)] : []),
      ...(!c.final && c.cost_expected == null ? [h("p", { class: "hint" }, noForecast)] : []),
      ...(!c.final && percentagesNode(c.percentages) ? [percentagesNode(c.percentages)] : []),
      h("p", { class: "hint" }, "Betaal je een voorschot voor stookkosten? Vul het in bij het contract, dan zie je hier hoe je ervoor staat."),
      ...extra];
  }

  /** A settled period's costs (from /api/ista/costs) as "€ 1.043,92 − € 600,00 voorschot (12 betalingen) = € 443,92 bijbetalen". */
  function settledText(s) {
    if (!s?.final || s.cost_expected == null) return null;
    const paid = s.paid + s.to_pay;
    const n = s.payments + s.payments_left;
    return `Uitkomst: ${eur(s.cost_expected)} − ${eur(paid)} voorschot (${n} ${n === 1 ? "betaling" : "betalingen"}) = ${resultText(paid - s.cost_expected)}`;
  }

  function contractCard(addr, k, settled) {
    const p = addr.periods.find((x) => x.y === k.y) || { start: "", end: "", from: "" };
    const today = iso(new Date());
    const state = k.final ? "definitief (afrekening ingevoerd)" : p.end < today ? "afgelopen, nog geen afrekening" : p.start > today ? "nog niet begonnen" : "lopend";
    const lines = [`Vloeroppervlak: ${num(k.area_dm2)} dm² (${nf2.format(k.area_dm2 / 100)} m²)${k.area_dm2 < 1000 ? " — klopt dat? 71 m² is 7.100 dm²" : ""}`];
    if (k.final) {
      const f = k.final;
      const unit = f.units_complex ? f.variable_energy / f.units_complex : null;
      const area = f.area_complex_dm2 ? (f.fixed_energy + f.other + f.services) / f.area_complex_dm2 : null;
      lines.push(`Afrekening: ${f.total ? eur(f.total) : "ingevoerd"}${unit != null ? ` · € ${nf6.format(unit)} per eenheid, € ${nf6.format(area)} per dm²` : ""}`);
    } else if (k.unit_price != null || k.area_price != null) {
      lines.push(`Prijzen: ${k.unit_price != null ? `€ ${nf6.format(k.unit_price)} per eenheid` : "per eenheid van de vorige afrekening"}, ${k.area_price != null ? `€ ${nf6.format(k.area_price)} per dm²` : "per dm² van de vorige afrekening"}`);
    } else {
      lines.push(`Prijzen: van de vorige afrekening${k.correction_pct ? `, ${k.correction_pct > 0 ? "+" : ""}${k.correction_pct}%` : ""}`);
    }
    // once settled the estimate no longer counts; shown only to compare with what was billed
    if (k.estimate_units) lines.push(k.final
      ? `Geschat jaarverbruik was ${num(k.estimate_units)} eenheden${k.final.units ? `, afgerekend ${num(k.final.units)}` : ""} (telt niet mee in de afrekening)`
      : `Verwacht jaarverbruik: ${num(k.estimate_units)} eenheden (gaat voor op vorig jaar)`);
    const amounts = k.advance || [];
    lines.push(amounts.length
      ? `Voorschot: ${amounts.map((a) => `${eur(a.amount)} per maand${amounts.length > 1 ? ` vanaf ${longDate.format(parseDate(a.from))}` : ""}`).join(", ")}`
      : "Geen voorschot");
    const outcome = settledText(settled);
    if (outcome) lines.push(h("strong", {}, outcome));
    const edit = h("button", { class: "btn", type: "button", onclick: () => openForm(addr, k) }, "Bewerken");
    const del = h("button", { class: "btn danger", type: "button" }, "Verwijderen");
    del.addEventListener("click", async () => {
      if (!confirm(`Contract voor afrekenperiode ${periodName(p)} verwijderen?`)) return;
      try { await api("DELETE", `/api/ista/contracts/${k.y}?${q(addr)}`); reload(); } catch (err) { alert(err.message); }
    });
    return h("section", { class: "card contract" },
      h("div", { class: "contract-head" },
        h("div", {}, h("h3", {}, `Afrekenperiode ${periodName(p)}`),
          h("div", { class: "secondary" }, `Verwarming (ista) · ${p.start ? `${longDate.format(parseDate(p.start))} t/m ${longDate.format(parseDate(p.end))}` : ""} · ${state}`)),
        h("div", { class: "row" }, edit, del)),
      h("ul", { class: "contract-lines" }, ...lines.map((l) => h("li", {}, l))));
  }

  function openForm(addr, k, contracts = []) {
    let initial = k;
    if (!initial) {
      // the newest period without a contract, with the area and advance of the latest contract
      const free = sortedPeriods(addr).filter((p) => !contracts.some((x) => x.y === p.y));
      const today = iso(new Date());
      const p = free.find((x) => today >= x.start && today <= x.end) || free[0];
      const last = contracts[contracts.length - 1];
      initial = { y: p.y, area_dm2: last?.area_dm2, advance: [], _new: true, _free: free };
    }
    const form = contractForm(addr, initial, () => { formSlot.replaceChildren(); reload(); }, () => formSlot.replaceChildren());
    formSlot.replaceChildren(form);
    form.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  function contractForm(addr, k, onSaved, onCancel) {
    const isNew = !!k._new;
    const f = k.final || {};
    const opt = (v) => numberInput(v ?? "", { min: 0 });
    const periodSelect = h("select", { class: "select", required: true },
      ...(isNew ? k._free : addr.periods.filter((p) => p.y === k.y)).map((p) => h("option", { value: String(p.y), selected: p.y === k.y }, `${periodName(p)} (${longDate.format(parseDate(p.start))} t/m ${longDate.format(parseDate(p.end))})`)));
    periodSelect.disabled = !isNew;
    const periodOf = () => addr.periods.find((p) => String(p.y) === periodSelect.value);
    const area = numberInput(k.area_dm2 ?? "", { min: 0, required: true });
    const areaHint = h("small", { class: "secondary" });
    const showArea = () => {
      const v = Number(area.value);
      areaHint.textContent = !area.value ? "dm², staat op de afrekening (71 m² = 7.100 dm²)"
        : `= ${nf2.format(v / 100)} m²${v > 0 && v < 1000 ? ". Dat is wel erg klein: 71 m² is 7.100 dm²." : ""}`;
      areaHint.className = v > 0 && v < 1000 ? "form-error" : "secondary";
    };
    area.addEventListener("input", showArea);
    showArea();
    const unitPrice = opt(k.unit_price), areaPrice = opt(k.area_price);
    const corr = numberInput(k.correction_pct || "");
    const estimate = opt(k.estimate_units || "");
    // the advance editor of the other contracts, with ista's {from, amount}
    const startProxy = { get value() { return periodOf()?.start || ""; } };
    const adv = advanceEditor({ advance: { amounts: (k.advance || []).map((a) => ({ from: a.from, eur: a.amount })) } }, startProxy);
    const finalOn = h("input", { type: "checkbox", checked: !!k.final });
    const fv = {
      variable_energy: opt(f.variable_energy), fixed_energy: opt(f.fixed_energy), other: opt(f.other), services: opt(f.services),
      units_complex: opt(f.units_complex), area_complex_dm2: opt(f.area_complex_dm2), units: opt(f.units), total: opt(f.total),
    };
    // the prices that follow from the complex's totals, as the gateway computes them
    const finalPrices = h("p", { class: "hint", "aria-live": "polite" });
    const showFinalPrices = () => {
      const n = (key) => Number(fv[key].value || 0);
      const units = n("units_complex"), areaC = n("area_complex_dm2");
      const unitP = units > 0 && n("variable_energy") > 0 ? n("variable_energy") / units : null;
      const fixed = n("fixed_energy") + n("other") + n("services");
      const areaP = areaC > 0 && fixed > 0 ? fixed / areaC : null;
      const warn = [];
      if (unitP == null || areaP == null) {
        finalPrices.textContent = "Daaruit rekent de app de prijs per eenheid en per dm² uit; vul daarvoor de bedragen, de eenheden en het vloeroppervlak van het complex in.";
      } else {
        finalPrices.textContent = `Daaruit volgt: € ${nf6.format(unitP)} per eenheid, € ${nf6.format(areaP)} per dm².`;
        // a price far below a cent means prices were typed where totals belong
        if (unitP < 0.01 || areaP < 0.001) warn.push("Die prijzen zijn wel erg laag: vul bij de kosten de totaalbedragen van het complex in, niet de prijzen.");
      }
      if (areaC > 0 && Number(area.value) > 0 && areaC < Number(area.value) * 2) warn.push("Het vloeroppervlak van het complex is het totaal van alle woningen, niet alleen dat van jouw woning.");
      if (warn.length) finalPrices.textContent += " " + warn.join(" ");
      finalPrices.className = warn.length ? "form-error" : "hint";
    };
    for (const el of Object.values(fv)) el.addEventListener("input", showFinalPrices);
    area.addEventListener("input", showFinalPrices);
    showFinalPrices();
    const finalBox = h("div", { hidden: !k.final },
      h("p", { class: "secondary" }, "Neem over wat op de afrekening van ista staat onder \"Berekening eenheidsprijzen\": de totaalbedragen, eenheden en het vloeroppervlak van het hele complex (alle woningen samen), niet de prijzen per eenheid of per dm². Heb je alleen die prijzen? Vul ze in bij Prijzen hierboven en laat dit uit."),
      h("div", { class: "form-grid" },
        field("Variabele energiekosten", fv.variable_energy, "€, totaal hele complex"),
        field("Vaste energiekosten", fv.fixed_energy, "€, totaal hele complex"),
        field("Overige kosten", fv.other, "€, totaal hele complex"),
        field("Diensten ista", fv.services, "€, totaal hele complex"),
        field("Eenheden complex", fv.units_complex, "totaal eenheden van alle woningen"),
        field("Vloeroppervlak complex", fv.area_complex_dm2, "dm², alle woningen samen"),
        field("Jouw eenheden", fv.units, "afrekenverbruik; leeg = gemeten"),
        field("Totaal op de afrekening", fv.total, "€, jouw bedrag, ter controle")),
      finalPrices);
    finalOn.addEventListener("change", () => { finalBox.hidden = !finalOn.checked; });
    const error = h("div", { class: "form-error", role: "alert", hidden: true });
    const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
    const cancel = h("button", { class: "btn", type: "button", onclick: onCancel }, "Annuleren");
    const optNum = (el) => (el.value === "" ? undefined : Number(el.value));

    const form = h("form", { class: "card contract-form" },
      h("h3", {}, isNew ? "Nieuw verwarmingscontract" : "Verwarmingscontract bewerken"),
      h("div", { class: "form-grid" },
        field("Afrekenperiode", periodSelect, "van ista, 1 juli t/m 30 juni"),
        h("label", { class: "field" }, h("span", {}, "Vloeroppervlak woning"), area, areaHint)),
      h("h4", {}, "Prijzen"),
      h("p", { class: "secondary" }, "Staan op de jaarafrekening van ista onder \"Berekening eenheidsprijzen\". Leeg laten: de app rekent met de prijzen van de laatste ingevoerde afrekening, eventueel met een correctie."),
      h("div", { class: "form-grid" },
        field("Prijs per eenheid", unitPrice, "€; leeg = vorige afrekening"),
        field("Prijs per dm²", areaPrice, "€; leeg = vorige afrekening"),
        field("Correctie op vorige prijzen", corr, "procent, bijv. 10 bij duurder gas")),
      h("h4", {}, "Voorschot"),
      h("label", { class: "check" }, adv.toggle, " Ik betaal een maandelijks voorschot voor stookkosten (vaak in de servicekosten)"),
      adv.node,
      h("details", { open: !!k.estimate_units }, h("summary", {}, "Verwacht jaarverbruik (voor de prognose, optioneel)"),
        h("p", { class: "secondary" }, "Ingevuld gaat het voor op je verbruik van vorig jaar: de rest van de periode volgt dan dit jaarverbruik, verdeeld met het stookpatroon van het gebouw. Leeg laten: de prognose volgt je verbruik van vorig jaar, of anders het gebouw."),
        h("div", { class: "form-grid" }, field("Verwarming", estimate, "eenheden per afrekenperiode"))),
      h("h4", {}, "Afrekening"),
      h("label", { class: "check" }, finalOn, " Afrekening ontvangen: maak deze periode definitief"),
      finalBox, error,
      h("div", { class: "row form-actions" }, save, cancel));

    form.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      error.hidden = true;
      const y = Number(periodSelect.value);
      const body = {
        area_dm2: Number(area.value), unit_price: optNum(unitPrice), area_price: optNum(areaPrice),
        correction_pct: optNum(corr), estimate_units: optNum(estimate),
        advance: (adv.read()?.amounts || []).filter((a) => a.from && a.eur > 0).map((a) => ({ from: a.from, amount: a.eur })),
      };
      if (k.note) body.note = k.note;
      if (finalOn.checked) body.final = Object.fromEntries(Object.entries(fv).map(([key, el]) => [key, Number(el.value || 0)]));
      for (const key of Object.keys(body)) if (body[key] === undefined) delete body[key];
      save.disabled = true;
      try {
        await api("PUT", `/api/ista/contracts/${y}?${q(addr)}`, body);
        onSaved();
      } catch (err) {
        error.textContent = err.message;
        error.hidden = false;
      } finally {
        save.disabled = false;
      }
    });
    return form;
  }

  return {
    node,
    address: current,
    ready: () => overview != null,
    load,
    show() {
      if (!unsubscribe) {
        unsubscribe = subscribe((change) => {
          if (change.type === "ista") loadOverview().then(() => onChange?.());
        });
      }
      if (!overview) loadOverview().then(() => onChange?.());
    },
    hide() {
      if (unsubscribe) { unsubscribe(); unsubscribe = null; }
      clearTimeout(statusTimer);
      seq++;
    },
  };
}

/** A dashboard card: this season so far against last year and the building, and the expected costs. */
export function heatingCard() {
  const node = h("section", { class: "card today-costs heating-costs", hidden: true, "aria-label": "Verwarming" });
  let seq = 0;
  async function load() {
    if (!hasHeating()) { node.hidden = true; return; }
    const my = ++seq;
    let ov, c, season, p, addr;
    try {
      ov = await api("GET", "/api/ista");
      addr = ov.addresses[0];
      if (!addr || !addr.last_measured) { node.hidden = true; return; }
      const today = iso(new Date());
      p = addr.periods.find((x) => today >= x.start && today <= x.end) || addr.periods[addr.periods.length - 1];
      // only the days ista measured, never its estimates
      if (addr.last_measured < p.from) { node.hidden = true; return; }
      [c, season] = await Promise.all([
        api("GET", `/api/ista/costs?cuid=${encodeURIComponent(addr.cuid)}&y=${p.y}`).catch(() => null),
        api("GET", `/api/ista/usage?cuid=${encodeURIComponent(addr.cuid)}&from=${p.from}&to=${addr.last_measured}&bucket=period`),
      ]);
    } catch {
      node.hidden = true;
      return;
    }
    if (my !== seq) return;
    const t = season.total;
    const unit = season.unit || "eenheden";
    // the same rows as "Kosten vandaag": colour key, label, amount, the units
    // as a hint. Last year is priced at this period's
    // price per unit; without a price the units are the amount.
    const rows = [];
    const row = (label, key, amount, hint) => h("div", { class: "today-costs-row" },
      h("span", { class: "label" }, h("span", { class: `key ${key}` }), label),
      h("span", { class: "amount" }, amount), h("span", { class: "hint" }, hint || ""));
    const units = (n) => `${num(n)} ${unit}`;
    const price = c?.unit_price;
    const unitsRow = (label, key, n, eurValue) => row(label, key,
      eurValue != null ? eur(eurValue) : price != null ? eur(n * price) : units(n),
      eurValue != null || price != null ? units(n) : "");
    const costSoFar = c?.cost_so_far;
    rows.push(unitsRow("Eenheden (netto)", "export", t.total, c?.variable_so_far));
    // the building average is units only, never priced
    if (t.building != null) rows.push(row("Gebouwgemiddelde", "import", units(t.building)));
    if (c?.fixed_so_far != null) rows.push(row("Vaste kosten", "fixed", eur(c.fixed_so_far), "deel tot nu toe"));
    if (t.last_year != null) rows.push(unitsRow("Zelfde moment vorig jaar", "gas", t.last_year));
    node.hidden = false;
    node.replaceChildren(
      h("div", { class: "today-costs-head" }, h("h3", {}, "Verwarming dit seizoen")),
      h("div", { class: "today-costs-body" },
        h("div", { class: "today-costs-total" },
          h("div", { class: "label secondary" }, `Sinds ${longDate.format(parseDate(p.from))}`),
          h("div", { class: "label secondary" }, "Totaal tot nu toe"),
          // without an ista contract there is no amount: the units then
          h("div", { class: "value" }, ...(costSoFar != null ? [eur(costSoFar)] : [num(t.total), h("small", {}, ` ${unit}`)]))),
        h("div", { class: "today-costs-rows" }, ...rows)),
      h("p", { class: "hint" }, staleSince(addr)
        ? `ista, gemeten t/m ${longDate.format(parseDate(staleSince(addr)))}; daarna nog niet uitgelezen.`
        : `ista, stand van ${longDate.format(parseDate(t.end))}.`));
  }
  const unsubscribe = subscribe((change) => {
    if (["ista", "snapshot", "plugins"].includes(change.type)) load();
  });
  load();
  return { node, stop() { unsubscribe(); seq++; } };
}
