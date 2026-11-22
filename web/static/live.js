// Dashboard: net power hero, today's totals and costs (and heating from
// ista this season), the user's own plugin
// tiles (tiles.js), prices, and one card per device (sockets and kWh meters
// with what their usage cost today).
import { state, subscribe, applyDevice, applyControl, controlStatus, controlBusy, controlNote, rateKey } from "./app.js";
import { sparkline } from "./charts.js";
import { priceCard } from "./prices.js";
import { myTiles } from "./tiles.js";
import { heatingCard } from "./heating.js";
import { eur, electricity, gasCost, credit, waterCost } from "./costs.js";
import { h, power, energy, gas, liters, flow, valueUnit, val, today, statusPill, onOffSwitch, api, ago, fmt, dates } from "./util.js";

// short: the tile already says "Huidig tarief", and a phone has half a tile for it
const TARIFF = { 1: "Dal", 2: "Normaal", 3: "Tarief 3", 4: "Tarief 4" };

export function renderLive(view) {
  const hero = h("section", { class: "card hero", "aria-label": "Nu" });
  const tiles = h("section", { class: "tiles", "aria-label": "Vandaag" });
  const grid = h("section", { class: "grid", "aria-label": "Apparaten" });
  const empty = h("div", { class: "notice", hidden: true });
  const price = priceCard();
  const mine = myTiles(); // the user's own plugin tiles; nothing until they make one
  const costs = todayCostsCard(ENERGY_COSTS);
  const waterCosts = todayCostsCard(WATER_COSTS);
  const heating = heatingCard(); // ista: hidden without heating data
  // side by side on a wide screen, stacked on a phone
  const costPair = h("div", { class: "today-costs-pair" }, costs.node, waterCosts.node, heating.node);
  view.append(hero, tiles, costPair, mine.node, price.node, h("h2", { class: "section-title" }, "Apparaten"), grid, empty);
  price.start();
  mine.start();

  const cards = new Map(); // id -> {node, update, setCost}
  const usageCosts = usageCostLoader(cards);

  const renderAll = () => {
    const devices = [...state.devices.values()];
    renderHero(hero, tiles);
    costs.setDevice(p1Device()?.id);
    waterCosts.setDevice(waterDevice()?.id);
    // keep card order in sync with the gateway order
    const ids = devices.map((d) => d.id);
    for (const [id, c] of cards) if (!ids.includes(id)) { c.node.remove(); cards.delete(id); }
    devices.forEach((d) => {
      let c = cards.get(d.id);
      if (!c) { c = deviceCard(d); cards.set(d.id, c); }
      c.update(d);
      grid.appendChild(c.node); // appendChild moves existing nodes into order
    });
    renderEmpty(empty, devices.length);
    usageCosts.devices(devices.filter((d) => USAGE_KINDS.has(d.kind)).map((d) => d.id));
  };

  const unsubscribe = subscribe((change) => {
    if (change.type === "plugin" || change.type === "plugins") {
      // plugin devices are shown on #/klimaat
    } else if (change.type === "device" && cards.has(change.id)) {
      cards.get(change.id).update(state.devices.get(change.id));
      renderHero(hero, tiles);
    } else {
      renderAll();
    }
  });
  renderAll();
  return () => { unsubscribe(); mine.stop(); price.stop(); costs.stop(); waterCosts.stop(); heating.stop(); usageCosts.stop(); };
}

// Devices whose usage can be priced on its own (Calculator.Usage).
const USAGE_KINDS = new Set(["socket", "kwh"]);

// usageCostLoader fetches what each socket and kWh meter's import cost today
// (contract prices with tax and VAT, no fixed costs, per finished quarter)
// and puts it on its card; refreshed every minute like the cost cards.
function usageCostLoader(cards) {
  let ids = [];
  let timer = null;
  let seq = 0;
  const load = async () => {
    const n = ++seq;
    const now = new Date();
    const to = new Date(now.getTime() + 24 * 3600e3);
    await Promise.all(ids.map(async (id) => {
      let b = null;
      try {
        const res = await api("GET", `/api/costs?scope=usage&device=${encodeURIComponent(id)}&from=${now.toISOString()}&to=${to.toISOString()}&bucket=day`);
        b = res.result.buckets[0] || null;
      } catch { /* no cost model or contract (503): no amount */ }
      if (n === seq) cards.get(id)?.setCost(b);
    }));
  };
  return {
    devices(next) {
      const key = next.join(",");
      if (key === ids.join(",")) return;
      ids = next;
      clearInterval(timer);
      timer = ids.length ? setInterval(load, COSTS_REFRESH_MS) : null;
      if (ids.length) load();
    },
    stop() { clearInterval(timer); timer = null; seq++; },
  };
}

// The cost cards on Live: energy (P1 meter) and water (water meter), the
// same card with their own API, rows and explanation without a contract.
const ENERGY_COSTS = {
  title: "Kosten vandaag", api: "/api/costs",
  noContract: [
    "Er is geen energiecontract voor vandaag. ",
    ["#/verbruik?view=kosten&kind=energy", "Voeg je contract toe onder Verbruik en Kosten › Kosten"],
    " om hier te zien wat stroom, gas en vaste kosten je vandaag tot nu toe kosten.",
  ],
  rows(b, device) {
    const exported = credit(b);
    const rows = [
      costRow("Stroom (netto)", eur(electricity(b) - exported), "import",
        exported ? `${eur(electricity(b))} afname − ${eur(exported)} teruglevering` : `${energy(b.import_kwh).join(" ")} afname`),
    ];
    const hasGas = val(device, "gas_m3") != null;
    if (hasGas || b.gas_m3 || b.gas_eur || b.gas_tax_eur) rows.push(costRow("Gas", eur(gasCost(b)), "gas", gas(b.gas_m3).join(" ")));
    return rows;
  },
};

const WATER_COSTS = {
  title: "Waterkosten vandaag", api: "/api/water/costs",
  noContract: [
    "Er is geen watercontract voor vandaag. ",
    ["#/verbruik?view=kosten&kind=water", "Voeg je watercontract toe onder Verbruik en Kosten › Water › Kosten"],
    " om hier te zien wat water en vaste kosten je vandaag tot nu toe kosten.",
  ],
  rows(b) {
    const used = liters(b.water_m3 * 1000).join(" ");
    return [costRow("Water", eur(waterCost(b)), "gas", b.water_tax_eur ? `${used}, waarvan ${eur(b.water_tax_eur)} belasting` : used)];
  },
};

const COSTS_REFRESH_MS = 60e3; // quarter hours are recomputed every minute

// todayCostsCard shows what today has cost so far according to the contract
// of kind k (ENERGY_COSTS or WATER_COSTS): the usage-dependent rows (for
// energy electricity net of export and net metering, and gas; for water the
// water) and the share of the fixed costs, plus the total. Hidden without the
// meter or cost model; an explanation without a contract for today.
function todayCostsCard(k) {
  const node = h("section", { class: "card today-costs", hidden: true, "aria-label": k.title, "aria-live": "polite" });
  let device = null;
  let timer = null;
  let seq = 0;

  const load = async () => {
    const id = device;
    if (!id) return;
    const n = ++seq;
    // the gateway floors 'from' to its own local midnight and stops at now
    const now = new Date();
    const to = new Date(now.getTime() + 24 * 3600e3);
    try {
      const res = await api("GET", `${k.api}?device=${encodeURIComponent(id)}&from=${now.toISOString()}&to=${to.toISOString()}&bucket=day`);
      if (n !== seq) return;
      draw(res.result.buckets[0]);
    } catch {
      if (n !== seq) return;
      node.hidden = true; // no cost model (503) or no such meter (404); retried on the next tick
    }
  };

  const draw = (b) => {
    if (!b) { node.hidden = true; return; }
    node.hidden = false;
    const head = h("div", { class: "today-costs-head" }, h("h3", {}, k.title));
    if (b.missing.contract_days) {
      node.replaceChildren(head, h("p", { class: "hint" },
        ...k.noContract.map((part) => (Array.isArray(part) ? h("a", { href: part[0] }, part[1]) : part))));
      return;
    }
    const provisional = !b.complete;
    if (provisional) head.append(h("span", { class: "badge provisional", title: incompleteText(b.missing) }, "Voorlopig"));
    const rows = k.rows(b, state.devices.get(device));
    rows.push(costRow("Vaste kosten", eur(b.fixed_eur), "fixed", "dagdeel tot nu toe"));
    node.replaceChildren(
      head,
      h("div", { class: "today-costs-body" },
        h("div", { class: "today-costs-total" },
          h("div", { class: "label secondary" }, "Totaal tot nu toe"),
          h("div", { class: "value" }, eur(b.total_eur))),
        h("div", { class: "today-costs-rows" }, ...rows)),
      h("p", { class: "hint" },
        provisional ? `Voorlopig: ${incompleteText(b.missing)}. ` : "",
        `Per afgerond kwartier, bijgewerkt ${dates.short(new Date())}.`),
    );
  };

  const stop = () => { clearInterval(timer); timer = null; seq++; };
  return {
    node,
    stop,
    setDevice(id) {
      if (id === device) return;
      device = id || null;
      stop();
      if (!device) { node.hidden = true; return; }
      load();
      timer = setInterval(load, COSTS_REFRESH_MS);
    },
  };
}

function costRow(label, amount, key, hint) {
  return h("div", { class: "today-costs-row" },
    h("span", { class: "label" }, h("span", { class: `key ${key}` }), label),
    h("span", { class: "amount" }, amount),
    h("span", { class: "hint" }, hint));
}

function incompleteText(m) {
  const n = (count, one, many) => `${count} ${count === 1 ? one : many}`;
  const parts = [];
  if (m.quarters) parts.push(`${n(m.quarters, "kwartier", "kwartieren")} nog zonder meting`);
  if (m.partial) parts.push(`${n(m.partial, "kwartier", "kwartieren")} geschat`);
  if (m.anomalous) parts.push(`${n(m.anomalous, "kwartier", "kwartieren")} met een meterafwijking`);
  if (m.prices) parts.push(`${n(m.prices, "kwartier", "kwartieren")} zonder stroomprijs`);
  if (m.gas_prices) parts.push(`${n(m.gas_prices, "kwartier", "kwartieren")} zonder gasprijs`);
  return parts.join(", ");
}

function renderEmpty(node, count) {
  if (!state.loaded && state.conn === "down") {
    node.hidden = false;
    node.replaceChildren(
      h("h2", {}, "Geen verbinding met de gateway"),
      h("p", {}, "Deze app werkt alleen binnen je eigen netwerk. Ben je thuis? Controleer dan of de gateway draait."),
    );
  } else if (state.loaded && count === 0) {
    node.hidden = false;
    node.replaceChildren(
      h("h2", {}, "Nog geen apparaten"),
      h("p", {}, "De gateway zoekt automatisch naar HomeWizard-apparaten in je netwerk. Zet in de HomeWizard Energy-app per apparaat 'Lokale API' aan, of ",
        h("a", { href: "#/apparaten" }, "voeg een apparaat toe op IP-adres"), "."),
    );
  } else {
    node.hidden = true;
  }
}

function waterDevice() {
  const all = [...state.devices.values()];
  return all.find((d) => d.kind === "water" && d.status === "online") || all.find((d) => d.kind === "water");
}

function p1Device() {
  const all = [...state.devices.values()];
  return all.find((d) => d.kind === "p1" && d.status === "online") || all.find((d) => d.kind === "p1");
}

function renderHero(hero, tiles) {
  const p1 = p1Device();
  if (!p1) {
    hero.hidden = true;
    tiles.hidden = true;
    return;
  }
  hero.hidden = false;
  tiles.hidden = false;
  const w = val(p1, "power_w");
  const exporting = w != null && w < 0;
  const label = w == null ? "Netvermogen" : exporting ? "Je levert nu terug aan het net" : "Je neemt nu af van het net";
  const spark = h("div", { class: "hero-spark" });
  const sub = p1.status === "online"
    ? `${p1.name} · bijgewerkt ${ago(p1.last_seen)}`
    : `${p1.name} · ${p1.message || "niet bereikbaar"}`;
  hero.replaceChildren(
    h("div", { class: "hero-main" },
      h("div", { class: "hero-label" }, h("span", { class: `key ${exporting ? "export" : "import"}` }), label),
      h("div", { class: "hero-value" }, ...valueUnit(power(w == null ? null : Math.abs(w)))),
      h("div", { class: "hero-sub" }, sub),
      phaseLine(p1),
    ),
    spark,
  );
  // the hero chart is signed: above zero = afname, below zero = teruglevering
  requestAnimationFrame(() => {
    const pts = state.live.get(p1.id) || [];
    const now = Date.now();
    sparkline(spark, pts, { from: now - 10 * 60e3, to: now, height: 120, zeroLine: true, color: "var(--import)" });
    spark.setAttribute("title", "Laatste 10 minuten");
  });

  const t = (k) => today(p1, k);
  const tariff = p1.reading?.tariff;
  const tileList = [
    tile("Afname vandaag", energy(t("energy_import_kwh")), "import"),
    tile("Teruglevering vandaag", energy(t("energy_export_kwh")), "export"),
  ];
  if (val(p1, "gas_m3") != null) tileList.push(tile("Gas vandaag", gas(t("gas_m3")), "gas", p1.reading?.gas_time ? `meterstand ${ago(p1.reading.gas_time)}` : null));
  if (tariff) tileList.push(tile("Huidig tarief", [TARIFF[tariff] || `Tarief ${tariff}`, ""]));
  const peak = val(p1, "monthly_power_peak_w");
  if (peak != null) tileList.push(tile("Maandpiek (kwartier)", power(peak), null, val(p1, "average_power_15m_w") != null ? `nu ${power(val(p1, "average_power_15m_w")).join(" ")} gemiddeld` : null));
  tiles.replaceChildren(...tileList);
}

function phaseLine(d) {
  const phases = [1, 2, 3].filter((i) => val(d, `power_l${i}_w`) != null || val(d, `current_l${i}_a`) != null);
  if (phases.length < 2) return null;
  return h("div", { class: "phases", "aria-label": "Per fase" },
    ...phases.flatMap((i) => [
      h("span", {}, `L${i}`),
      h("span", {}, [val(d, `voltage_l${i}_v`) != null ? `${fmt.n0(val(d, `voltage_l${i}_v`))} V` : "", val(d, `current_l${i}_a`) != null ? `${fmt.n1(val(d, `current_l${i}_a`))} A` : ""].filter(Boolean).join(" · ")),
      h("span", { class: "num" }, val(d, `power_l${i}_w`) != null ? power(val(d, `power_l${i}_w`)).join(" ") : ""),
    ]));
}

function tile(label, value, key, hint) {
  return h("div", { class: "card tile" },
    h("div", { class: "label" }, key ? h("span", { class: `key ${key}` }) : null, label),
    h("div", { class: "value" }, ...valueUnit(value)),
    hint ? h("div", { class: "hint" }, hint) : null);
}

function deviceCard(initial) {
  const name = h("div", { class: "device-name" });
  const model = h("div", { class: "device-model" });
  const pill = h("span");
  const value = h("div", { class: "device-value" });
  const meta = h("div", { class: "device-meta" });
  const extra = h("div");
  const spark = h("div", { class: "spark" });
  const counters = h("dl", { class: "counters", hidden: true, "aria-label": "Meterstanden" });
  // today's usage cost (sockets and kWh meters) on a line of its own, so
  // every card looks the same; "(voorlopig)" opens the reason, also on a
  // phone where there is no tooltip
  const cost = h("div", { class: "device-cost", hidden: true });
  const costWhy = h("div", { class: "device-cost-why", hidden: true });
  let whyOpen = false;
  const msg = h("div", { class: "device-msg" });
  const sw = onOffSwitch("Aan/uit", async (on) => {
    toggle.disabled = true;
    try {
      // the gateway switches in the background, checks it and tries again
      const st = await api("PUT", `/api/devices/${encodeURIComponent(current.id)}/power`, { on });
      if (st) applyControl(st);
    } catch (e) {
      toggle.checked = !on;
      msg.textContent = e.message;
    } finally {
      toggle.disabled = false;
    }
  });
  const toggle = sw.input;
  const switchLabel = sw.node;
  switchLabel.hidden = true;
  const pairBtn = h("button", { class: "btn primary", type: "button", hidden: true }, "Koppelen");
  const node = h("article", { class: "card device" },
    h("div", { class: "device-head" }, h("div", {}, name, model), pill),
    value, meta, cost, costWhy, extra, spark, counters, msg, h("div", { class: "row" }, switchLabel, pairBtn));

  let current = initial;
  pairBtn.addEventListener("click", async () => {
    try { applyDevice(await api("POST", `/api/devices/${encodeURIComponent(current.id)}/pair`)); } catch (e) { msg.textContent = e.message; }
  });

  const update = (d) => {
    current = d;
    name.textContent = d.name;
    model.textContent = d.model || d.product_type || "";
    pill.replaceChildren(statusPill(d.status));
    const key = rateKey(d);
    const v = val(d, key);
    value.replaceChildren(...valueUnit(d.kind === "water" ? flow(v) : power(v)));
    value.hidden = v == null;
    meta.replaceChildren(...metaFor(d));
    extra.replaceChildren(...extraFor(d));
    const rows = d.show_counters ? countersFor(d) : [];
    counters.replaceChildren(...rows.flatMap(([label, text]) => [h("dt", {}, label), h("dd", {}, text)]));
    counters.hidden = !rows.length;
    // a command being sent shows its value; its progress or failure shows
    // with the device's own message
    const st = d.can_switch ? controlStatus(d.id, "power") : null;
    const note = controlNote(st);
    msg.replaceChildren(...[d.message, note && h("span", { class: note.error ? "error-text" : "" }, note.text)]
      .filter(Boolean).flatMap((x, i) => (i ? [" ", x] : [x])));
    msg.hidden = !d.message && !note;
    switchLabel.hidden = !d.can_switch;
    if (d.can_switch && document.activeElement !== toggle) {
      sw.set(controlBusy(st) ? st.value : d.reading?.power_on, {
        disabled: d.status !== "online" || !!d.reading?.switch_lock,
        label: d.reading?.switch_lock ? "Vergrendeld" : "",
      });
    }
    // only when the device cannot be read without pairing; optional pairing
    // (e.g. the P1 meter, which works fully over API v1) is under Apparaten
    pairBtn.hidden = d.status !== "needs_pairing";
    requestAnimationFrame(() => {
      const now = Date.now();
      const pts = state.live.get(d.id) || [];
      sparkline(spark, pts, { from: now - 10 * 60e3, to: now, zeroLine: key === "power_w", color: d.kind === "water" ? "var(--gas)" : "var(--import)" });
    });
  };
  // b: the usage bucket of today, or null without a cost model or contract
  const setCost = (b) => {
    const show = b && !b.missing.contract_days;
    cost.hidden = !show;
    if (!show) { costWhy.hidden = true; return; }
    const total = b.electricity_eur + b.electricity_tax_eur;
    const provisional = !b.complete;
    const parts = ["Kosten vandaag ", h("b", {}, eur(total))];
    if (provisional) {
      const why = h("button", { class: "link-btn", type: "button", "aria-expanded": String(whyOpen) }, "voorlopig");
      why.addEventListener("click", () => {
        whyOpen = !whyOpen;
        why.setAttribute("aria-expanded", String(whyOpen));
        costWhy.hidden = !whyOpen;
      });
      parts.push(" (", why, ")");
    }
    cost.replaceChildren(...parts);
    cost.title = "Afname tegen je contractprijs, met energiebelasting en btw, zonder vaste kosten; per afgerond kwartier.";
    costWhy.textContent = provisional ? `Nog niet definitief: ${incompleteText(b.missing)}.` : "";
    costWhy.hidden = !provisional || !whyOpen;
  };
  update(initial);
  return { node, update, setCost };
}

function metaFor(d) {
  const items = [];
  const add = (label, text) => { if (text) items.push(h("span", {}, `${label} `, h("b", {}, text))); };
  const t = (k) => today(d, k);
  switch (d.kind) {
    case "p1":
      // today only; the meter readings are optional (Apparaten), below the chart
      add("Stroom vandaag", t("energy_import_kwh") != null ? energy(t("energy_import_kwh")).join(" ") : null);
      if (t("energy_export_kwh")) add("terug", energy(t("energy_export_kwh")).join(" "));
      add("Gas vandaag", t("gas_m3") != null ? gas(t("gas_m3")).join(" ") : null);
      break;
    case "water":
      add("Vandaag", t("water_m3") != null ? liters(t("water_m3") * 1000).join(" ") : null);
      break;
    case "battery":
      add("Vandaag geladen", t("energy_import_kwh") != null ? energy(t("energy_import_kwh")).join(" ") : null);
      add("ontladen", t("energy_export_kwh") != null ? energy(t("energy_export_kwh")).join(" ") : null);
      add("Cycli", val(d, "cycles") != null ? fmt.n0(val(d, "cycles")) : null);
      break;
    default:
      add("Vandaag", t("energy_import_kwh") != null ? energy(t("energy_import_kwh")).join(" ") : null);
      if (t("energy_export_kwh")) add("terug", energy(t("energy_export_kwh")).join(" "));
      if (d.kind !== "socket" && val(d, "voltage_v") != null) add("", `${fmt.n0(val(d, "voltage_v"))} V`);
  }
  if (d.status !== "online" && d.last_seen) add("Laatst gezien", ago(d.last_seen));
  return items;
}

const TARIFFS = [1, 2, 3, 4];

/** Meter readings as on the meter itself: per tariff, gas and water. */
function countersFor(d) {
  const rows = [];
  const kwh = (v) => `${fmt.n3(v)} kWh`;
  const m3 = (v) => `${fmt.n3(v)} m³`;
  const tariffs = (dir, label) => {
    const list = TARIFFS.filter((i) => val(d, `energy_${dir}_t${i}_kwh`) != null);
    // a single-tariff meter or one without tariffs: the total
    if (list.length < 2) {
      if (val(d, `energy_${dir}_kwh`) != null) rows.push([label, kwh(val(d, `energy_${dir}_kwh`))]);
      return;
    }
    for (const i of list) rows.push([`${label} T${i}`, kwh(val(d, `energy_${dir}_t${i}_kwh`))]);
  };
  if (d.kind === "water") {
    if (val(d, "water_m3") != null) rows.push(["Stand", m3(val(d, "water_m3"))]);
    return rows;
  }
  tariffs("import", "Afname");
  // feed-in only when the meter ever counted any
  if (val(d, "energy_export_kwh")) tariffs("export", "Teruglevering");
  if (val(d, "gas_m3") != null) rows.push(["Gas", m3(val(d, "gas_m3"))]);
  return rows;
}

function extraFor(d) {
  if (d.kind === "battery" && val(d, "state_of_charge_pct") != null) {
    const soc = val(d, "state_of_charge_pct");
    const bar = h("span");
    bar.style.width = `${Math.max(0, Math.min(100, soc))}%`;
    const w = val(d, "power_w");
    const dir = w == null || Math.abs(w) < 5 ? "Stand-by" : w > 0 ? "Laden" : "Ontladen";
    return [h("div", { class: "soc" },
      h("div", { class: "meter", role: "meter", "aria-valuenow": soc, "aria-valuemin": 0, "aria-valuemax": 100, "aria-label": "Laadtoestand" }, bar),
      h("span", {}, `${fmt.n0(soc)}% · ${dir}`))];
  }
  if (d.kind === "kwh" || d.kind === "p1") {
    return d.kind === "kwh" ? [phaseLine(d)].filter(Boolean) : [];
  }
  return [];
}
