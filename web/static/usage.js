// "Verbruik en Kosten": one tab for usage, costs and grid quality. The
// choices on top are shared: Energie, Water or Verwarming, the device (for
// heating the room), the period and the view (Verbruik, Kosten, Net). The views are panels of history.js (Verbruik, Net),
// costs.js (Kosten) and heating.js (Verwarming, from ista); this module only
// chooses what they show. For heating a contract period is an ista
// settlement period and today is not offered. A contract period is a contract from its start to its end, with
// contracts that continue one another (a tariff change at the same
// supplier) taken together; ‹ and › go to the previous or next one.
import { state, subscribe } from "./app.js";
import { historyPanel } from "./history.js";
import { costsPanel } from "./costs.js";
import { heatingPanel, hasHeating, heatingRange, roomName } from "./heating.js";
import { h, api, dates } from "./util.js";
import { downloadReport } from "./reports.js";

const PERIODS = [
  { id: "today", label: "Vandaag" },
  { id: "yesterday", label: "Gisteren" },
  { id: "week", label: "7 dagen" },
  { id: "month", label: "Maand" },
  { id: "year", label: "Jaar" },
  { id: "contract", label: "Contractperiode" },
  { id: "all", label: "Alles" },
];
// a saved view "weken" (heating, removed) falls back to Verbruik
const VIEWS = [["verbruik", "Verbruik"], ["kosten", "Kosten"], ["net", "Net"]];
const HEATING_PERIODS = new Set(["yesterday", "week", "month", "year", "contract", "all"]);
const COST_KINDS = new Set(["p1", "socket", "kwh"]); // and water
const NET_KINDS = new Set(["p1", "kwh"]);

const prefs = {
  get() { try { return JSON.parse(localStorage.getItem("hw.usage") || "{}"); } catch { return {}; } },
  set(v) { try { localStorage.setItem("hw.usage", JSON.stringify({ ...this.get(), ...v })); } catch { /* private mode */ } },
};

const longDate = new Intl.DateTimeFormat("nl-NL", { day: "numeric", month: "long", year: "numeric" });
const startOfDay = (d) => { const x = new Date(d); x.setHours(0, 0, 0, 0); return x; };
const addDays = (d, n) => { const x = new Date(d); x.setDate(x.getDate() + n); return x; };
const parseDate = (s) => { const [y, m, d] = s.split("-").map(Number); return new Date(y, m - 1, d); };
const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

/**
 * Contract periods, newest first: each contract on its own, except that a
 * contract that continues the one before (a tariff change) joins its period.
 * end is the last day (inclusive) or "" while it runs.
 */
export function contractPeriods(contracts) {
  const cs = [...contracts].sort((a, b) => a.start.localeCompare(b.start));
  const out = [];
  cs.forEach((c, i) => {
    const following = cs[i + 1];
    const end = c.end || (following ? iso(addDays(parseDate(following.start), -1)) : "");
    const last = out[out.length - 1];
    if (c.continues && last) {
      last.parts.push(c);
      last.end = end;
    } else {
      out.push({ start: c.start, end, parts: [c] });
    }
  });
  return out.reverse();
}

/**
 * The period of a range id, `offset` periods back. bucket is what the usage
 * charts use (quarter hours for a day), costBucket what the costs use (hours
 * for a day); rate: the power line by the minute; yearly: months folded into
 * years.
 */
function period(id, offset, periods, firstYear) {
  const now = new Date();
  const today0 = startOfDay(now);
  const day = (from) => ({ from, to: addDays(from, 1), bucket: "quarter", costBucket: "hour", rate: true, label: dates.day(from) });
  switch (id) {
    case "yesterday": return day(addDays(today0, -1 - offset));
    case "week": {
      const to = addDays(today0, 1 - 7 * offset);
      const from = addDays(to, -7);
      return { from, to, bucket: "day", costBucket: "day", rate: true, label: `${dates.day(from)} – ${dates.day(addDays(to, -1))}` };
    }
    case "month": {
      const from = new Date(now.getFullYear(), now.getMonth() - offset, 1);
      return { from, to: new Date(from.getFullYear(), from.getMonth() + 1, 1), bucket: "day", costBucket: "day", label: dates.month(from) };
    }
    case "year": {
      const from = new Date(now.getFullYear() - offset, 0, 1);
      return { from, to: new Date(from.getFullYear() + 1, 0, 1), bucket: "month", costBucket: "month", label: String(from.getFullYear()) };
    }
    case "contract": {
      const c = periods[offset];
      if (!c) return null;
      const from = parseDate(c.start);
      const to = c.end ? addDays(parseDate(c.end), 1) : addDays(today0, 1);
      const short = to - from <= 62 * 86400e3; // a short contract per day
      return { from, to, bucket: short ? "day" : "month", costBucket: short ? "day" : "month",
        label: `Contract ${periods.length - offset} van ${periods.length}`, contract: c };
    }
    case "all": {
      const from = new Date(Math.min(firstYear, now.getFullYear()), 0, 1);
      return { from, to: new Date(now.getFullYear() + 1, 0, 1), bucket: "month", yearly: true, costBucket: "year",
        label: `${from.getFullYear()} – ${now.getFullYear()}` };
    }
    default: return day(addDays(today0, -offset));
  }
}

/**
 * The tab. opts (from the router, for old links) may preset the view and
 * the kind; #/verbruik?view=kosten&kind=water&device=<id>&period=year does
 * the same.
 */
export function renderUsage(view, opts = {}) {
  const saved = prefs.get();
  const query = new URLSearchParams(location.hash.split("?")[1] || "");
  const pick = (key, ok) => [query.get(key), opts[key], saved[key]].find((v) => v != null && ok(v));
  const ui = {
    kind: pick("kind", (v) => v === "energy" || v === "water" || v === "heating") || "energy",
    device: pick("device", (v) => typeof v === "string" && v !== "") || "",
    period: pick("period", (v) => PERIODS.some((p) => p.id === v)) || "today",
    view: pick("view", (v) => VIEWS.some(([id]) => id === v)) || "verbruik",
    offset: 0,
  };
  // an old link to the costs of a meter (#/kosten?device=…) or to water
  if (query.get("device") || opts.view) prefs.set({ device: ui.device, view: ui.view, kind: ui.kind });

  const segmented = (label, items, onPick) => {
    const g = h("div", { class: "segmented", role: "group", "aria-label": label });
    for (const [id, text] of items) g.append(h("button", { type: "button", "data-id": id, onclick: () => onPick(id) }, text));
    return g;
  };
  const kindGroup = segmented("Soort", [["energy", "Energie"], ["water", "Water"], ["heating", "Verwarming"]], (id) => { ui.kind = id; ui.device = ""; ui.offset = 0; save(); draw(); });
  const deviceSelect = h("select", { class: "select", "aria-label": "Apparaat", onchange: () => { ui.device = deviceSelect.value; save(); draw(); } });
  const periodGroup = segmented("Periode", PERIODS.map((p) => [p.id, p.label]), (id) => setPeriod(id));
  periodGroup.classList.add("period-buttons");
  const periodSelect = h("select", { class: "select period-select", "aria-label": "Periode", onchange: () => setPeriod(periodSelect.value) },
    ...PERIODS.map((p) => h("option", { value: p.id }, p.label)));
  const prev = h("button", { class: "btn", type: "button" }, "‹");
  const next = h("button", { class: "btn", type: "button" }, "›");
  const label = h("span", { class: "period-label" });
  const viewGroup = segmented("Weergave", VIEWS, (id) => { ui.view = id; save(); draw(); });
  const viewHint = h("span", { class: "secondary view-hint" });
  // a PDF report over the period shown (energy or water, not heating)
  const pdf = h("button", { class: "btn-ghost pdf-button", type: "button", title: "Rapport over deze periode als PDF" }, "PDF");
  let pdfRange = null;
  pdf.addEventListener("click", async () => {
    if (!pdfRange) return;
    pdf.disabled = true;
    pdf.textContent = "Maken…";
    try {
      await downloadReport(pdfRange);
    } catch (e) {
      notice.hidden = false;
      notice.replaceChildren(h("p", {}, e.message));
    } finally {
      pdf.disabled = false;
      pdf.textContent = "PDF";
    }
  });
  const contractInfo = h("div", { class: "contract-info", hidden: true });
  const notice = h("div", { class: "notice", hidden: true });

  const history = historyPanel();
  const heating = heatingPanel(() => { if (ui.kind === "heating") draw(); });
  // stored ista data counts too: the account may have no devices added
  let istaData = false, istaChecked = false;
  api("GET", "/api/ista").then((r) => { istaData = (r.addresses || []).length > 0; })
    .catch(() => {}).finally(() => { istaChecked = true; draw(); });
  const costs = { energy: costsPanel("energy"), water: costsPanel("water") };
  const contracts = { energy: [], water: [] };
  for (const [kind, panel] of Object.entries(costs)) {
    // from the details of what is missing: the usage of that day
    panel.onDay((d) => {
      ui.view = "verbruik";
      ui.period = "today";
      ui.offset = Math.max(0, Math.round((startOfDay(new Date()) - startOfDay(d)) / 86400e3));
      save();
      draw();
      window.scrollTo({ top: 0, behavior: "smooth" });
    });
    panel.onContracts((list) => {
      contracts[kind] = contractPeriods(list);
      if (kind === ui.kind) draw();
    });
  }

  const periodRow = h("div", { class: "row" }, periodGroup, periodSelect, h("div", { class: "row period-nav" }, prev, label, next));
  const viewRow = h("div", { class: "row" }, viewGroup, viewHint, pdf);
  view.append(
    h("section", { class: "card usage-filters", "aria-label": "Keuzes" },
      h("div", { class: "row" }, kindGroup, deviceSelect),
      periodRow,
      contractInfo,
      viewRow),
    notice, history.node, costs.energy.node, costs.water.node, heating.node,
  );

  const save = () => prefs.set({ kind: ui.kind, device: ui.device, period: ui.period, view: ui.view });
  function setPeriod(id) { ui.period = id; ui.offset = 0; save(); draw(); }
  prev.addEventListener("click", () => { ui.offset++; draw(); });
  next.addEventListener("click", () => { if (ui.offset > 0) { ui.offset--; draw(); } });

  const press = (group, id) => { for (const b of group.querySelectorAll("button")) b.setAttribute("aria-pressed", String(b.dataset.id === id)); };
  const hasWater = () => [...state.devices.values()].some((d) => d.kind === "water");
  const hasIsta = () => istaData || hasHeating();

  /** The devices of the chosen kind: the P1 meter as the whole connection first. */
  function devicesOf(kind) {
    const all = [...state.devices.values()];
    if (kind === "water") return all.filter((d) => d.kind === "water");
    const p1 = all.filter((d) => d.kind === "p1");
    const rest = all.filter((d) => ["socket", "kwh", "battery"].includes(d.kind))
      .sort((a, b) => (a.name || "").localeCompare(b.name || "", "nl"));
    return [...p1, ...rest];
  }

  function draw() {
    if (ui.kind === "water" && !hasWater() && state.loaded) ui.kind = "energy";
    if (ui.kind === "heating" && !hasIsta() && state.loaded && istaChecked) ui.kind = "energy";
    kindGroup.hidden = !hasWater() && !hasIsta() && ui.kind === "energy";
    for (const b of kindGroup.querySelectorAll("button")) {
      if (b.dataset.id === "water") b.hidden = !hasWater();
      if (b.dataset.id === "heating") b.hidden = !hasIsta();
    }
    press(kindGroup, ui.kind);
    const heat = ui.kind === "heating";
    heating.node.hidden = !heat;
    pdf.hidden = heat;
    if (heat) {
      drawHeating();
      return;
    }
    heating.hide();
    for (const b of periodGroup.querySelectorAll("button")) b.hidden = false;
    for (const o of periodSelect.options) o.hidden = false;
    const list = devicesOf(ui.kind);
    if (!list.length) {
      deviceSelect.replaceChildren();
      for (const n of [history.node, costs.energy.node, costs.water.node]) n.hidden = true;
      notice.hidden = false;
      notice.replaceChildren(h("h2", {}, state.loaded ? "Nog geen apparaten" : "Laden…"), h("p", {}, state.loaded ? "Voeg eerst een apparaat toe onder Apparaten." : ""));
      return;
    }
    const device = list.find((d) => d.id === ui.device) || list[0];
    ui.device = device.id;
    deviceSelect.setAttribute("aria-label", "Apparaat");
    deviceSelect.replaceChildren(...list.map((d) => h("option", { value: d.id, selected: d.id === device.id },
      d.kind === "p1" ? `Hele aansluiting (${d.name})` : d.name)));

    // the views this device has
    const can = { verbruik: true, kosten: ui.kind === "water" || COST_KINDS.has(device.kind), net: ui.kind === "energy" && NET_KINDS.has(device.kind) };
    for (const b of viewGroup.querySelectorAll("button")) b.hidden = !can[b.dataset.id];
    const viewId = can[ui.view] ? ui.view : "verbruik";
    press(viewGroup, viewId);
    viewHint.textContent = {
      verbruik: device.kind === "p1" ? "Wat je huis gebruikte: stroom en gas." : ui.kind === "water" ? "Wat er door de watermeter ging." : "Wat dit apparaat gebruikte.",
      kosten: ui.kind === "water" ? "Volgens je watercontract, inclusief btw." : device.kind === "p1" ? "Volgens je energiecontract, inclusief btw." : "Afname van dit apparaat tegen je contractprijs, zonder vaste kosten.",
      net: "Kwaliteit van de aansluiting: spanning, stroomsterkte en vermogen per fase.",
    }[viewId];

    // the period; a contract period only with contracts of this kind
    const periods = contracts[ui.kind];
    for (const b of periodGroup.querySelectorAll("button")) b.hidden = b.dataset.id === "contract" && !periods.length;
    for (const o of periodSelect.options) o.hidden = o.value === "contract" && !periods.length;
    if (ui.period === "contract" && !periods.length) ui.period = "today";
    if (ui.period === "contract") ui.offset = Math.min(ui.offset, periods.length - 1);
    press(periodGroup, ui.period);
    periodSelect.value = ui.period;
    const firstYears = [...contracts.energy, ...contracts.water].map((c) => Number(c.start.slice(0, 4)));
    const firstYear = firstYears.length ? Math.min(...firstYears) : new Date().getFullYear() - 4;
    const p = period(ui.period, ui.offset, periods, firstYear);
    label.textContent = p.label;
    prev.hidden = next.hidden = ui.period === "all";
    next.disabled = ui.offset === 0;
    prev.disabled = ui.period === "contract" && ui.offset >= periods.length - 1;
    prev.setAttribute("aria-label", ui.period === "contract" ? "Vorig contract" : "Vorige periode");
    next.setAttribute("aria-label", ui.period === "contract" ? "Volgend contract" : "Volgende periode");
    drawContract(p.contract);
    // the report: whole days up to today; water reports on water only
    const today1 = addDays(startOfDay(new Date()), 1);
    pdfRange = { from: p.from, to: p.to > today1 ? today1 : p.to,
      sections: ui.kind === "water" ? ["summary", "water"] : [] };
    if (p.from >= pdfRange.to) pdfRange = null;
    pdf.hidden = !pdfRange;

    notice.hidden = true;
    history.node.hidden = viewId === "kosten";
    costs.energy.node.hidden = !(viewId === "kosten" && ui.kind === "energy");
    costs.water.node.hidden = !(viewId === "kosten" && ui.kind === "water");
    if (viewId === "kosten") {
      const panel = costs[ui.kind];
      const whole = ui.kind === "water" || device.kind === "p1";
      panel.setExtras(whole);
      if (!whole) {
        notice.hidden = false;
        notice.replaceChildren(h("p", {}, "Contracten, voorschot en vergelijken horen bij de hele aansluiting: kies bij Apparaat de P1-meter."));
      }
      panel.load(whole ? "" : device.id, p);
    } else {
      history.load(device, p, viewId, view.clientWidth);
    }
  }

  /** Heating: the room in the place of the device, the periods ista has days for, and the views Verbruik and Kosten. */
  function drawHeating() {
    contractInfo.hidden = true;
    notice.hidden = true;
    for (const n of [history.node, costs.energy.node, costs.water.node]) n.hidden = true;
    heating.show();
    const addr = heating.address();
    deviceSelect.setAttribute("aria-label", "Kamer");
    const rooms = addr?.rooms || [];
    if (ui.device && !rooms.some((r) => String(r.id) === ui.device)) ui.device = "";
    deviceSelect.replaceChildren(h("option", { value: "" }, "Alle kamers"),
      ...rooms.map((r) => h("option", { value: String(r.id), selected: String(r.id) === ui.device }, roomName(r, addr))));
    deviceSelect.value = ui.device;

    const periods = addr?.periods || [];
    for (const b of periodGroup.querySelectorAll("button")) b.hidden = !HEATING_PERIODS.has(b.dataset.id) || (b.dataset.id === "contract" && !periods.length);
    for (const o of periodSelect.options) o.hidden = !HEATING_PERIODS.has(o.value);
    if (!HEATING_PERIODS.has(ui.period)) { ui.period = "month"; ui.offset = 0; }
    if (ui.period === "contract") ui.offset = Math.min(ui.offset, Math.max(0, periods.length - 1));
    press(periodGroup, ui.period);
    periodSelect.value = ui.period;
    const range = addr ? heatingRange(ui.period, ui.offset, addr) : null;
    label.textContent = range?.label || "";
    prev.hidden = next.hidden = ui.period === "all";
    next.disabled = ui.offset === 0;
    prev.disabled = !range || (ui.period === "contract" ? ui.offset >= periods.length - 1 : range.from <= range.first);
    prev.setAttribute("aria-label", ui.period === "contract" ? "Vorige afrekenperiode" : "Vorige periode");
    next.setAttribute("aria-label", ui.period === "contract" ? "Volgende afrekenperiode" : "Volgende periode");
    if (range?.period) {
      contractInfo.hidden = false;
      contractInfo.replaceChildren(h("div", {}, h("strong", {}, "ista"), ` · afrekenperiode ${longDate.format(parseDate(range.period.start))} – ${longDate.format(parseDate(range.period.end))}`
        + (range.period.from !== range.period.start ? ` · bewoond vanaf ${longDate.format(parseDate(range.period.from))}` : "")));
    }

    for (const b of viewGroup.querySelectorAll("button")) b.hidden = b.dataset.id === "net";
    const viewId = ui.view === "net" ? "verbruik" : ui.view;
    press(viewGroup, viewId);
    viewHint.textContent = {
      verbruik: ui.period === "yesterday" ? "Verbruik per kamer van die dag, volgens ista." : ui.device ? "Wat de meter op deze radiator telde." : "Wat je verwarming gebruikte, per kamer, volgens ista.",
      kosten: "Volgens je verwarmingscontract en de afrekening van ista.",
    }[viewId];
    if (!heating.ready()) return; // drawn again once the addresses are in
    heating.load({ room: ui.device, view: viewId, range });
  }

  function drawContract(c) {
    contractInfo.hidden = !c;
    if (!c) return;
    const until = c.end ? longDate.format(parseDate(c.end)) : "heden";
    const head = h("div", {}, h("strong", {}, c.parts[0].name), ` · ${longDate.format(parseDate(c.start))} – ${until}`);
    if (c.parts.length > 1) head.append(h("span", { class: "secondary" }, ` · ${c.parts.length} aaneengesloten contracten, getoond als één periode`));
    contractInfo.replaceChildren(head,
      ...(c.parts.length > 1 ? c.parts.map((x, i) => h("div", { class: "contract-part" },
        `${longDate.format(parseDate(x.start))}: ${x.name}${i ? " (tariefwijziging)" : ""}`)) : []));
  }

  // devices may arrive after the tab: redraw when the list changes, not on readings
  let key = "";
  const devicesKey = () => [...state.devices.values()].map((d) => `${d.id}:${d.kind}:${d.name}`).join(",");
  const unsubscribe = subscribe((change) => {
    if (change.type === "device" && devicesKey() === key) return;
    if (change.type === "live" || change.type === "conn") return;
    const k = devicesKey();
    if (k === key && change.type !== "snapshot") return;
    key = k;
    draw();
  });
  key = devicesKey();
  draw();
  return () => { unsubscribe(); heating.hide(); };
}
