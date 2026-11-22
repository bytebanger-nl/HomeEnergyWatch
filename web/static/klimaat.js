// Klimaat view: one tile per plugin device (Tado, MHI, Tuya, ...). The tab
// is only shown in the nav when there is at least one plugin device.
//
// A plugin device whose type allows it (can_link_meter: an air conditioner,
// an electric radiator; not a tado room) can be linked to one socket or kWh
// meter. One meter may measure several devices, e.g. air conditioner units
// on one group; the tile then says the meter is shared. The tile shows the
// meter's live power and today's usage and costs, and the history shows that
// power next to a metric of the device such as the room temperature. Gas
// per room is not measured (the P1 meter only sees the whole house), so it
// is never shown here.
//
// When control is allowed for the plugin (Instellingen → Plugins), the
// device view carries `controls` and the tile gets buttons built from them:
// a switch (the same as a socket's on the dashboard, util.js onOffSwitch), a
// setpoint with − and +, a select, or an action button. States are shown in
// the same tones as on the dashboard (util.js metricTone).
//
// "Dashboardtegel maken" and "Op dashboard" (per device) open the editor of
// the user's own dashboard tiles (tiles.js), the only place to make the
// first one.
import { state, subscribe, controlStatus, controlBusy, controlNote, applyControl } from "./app.js";
import { lineChart } from "./charts.js";
import { eur } from "./costs.js";
import { tileEditor, tilesApi } from "./tiles.js";
import { h, statusPill, ago, fmt, power, energy, join, api, dates, onOffSwitch, metricTone, toneChip, metricText } from "./util.js";
import { outageLine } from "./outages.js";

const COST_REFRESH_MS = 60e3;

// sub is the id of one of the user's own tabs; without it the view shows
// the devices on no own tab (all of them when there are no own tabs).
export function renderKlimaat(view, sub) {
  const tabId = Number(sub) || 0;
  const grid = h("section", { class: "grid plugin-tiles", "aria-label": "Plugin-apparaten" });
  const empty = h("div", { class: "notice", hidden: true },
    tabId ? "Dit tabblad heeft geen apparaten. Kies het tabblad van een apparaat onder " : "Er zijn hier geen plugin-apparaten. Voeg ze toe onder ",
    h("a", { href: "#/instellingen" }, "Instellingen → Plugins"), ".");
  const dashBox = h("div", { class: "dash-editor", hidden: true });
  const bar = h("div", { class: "row klimaat-bar", hidden: true },
    h("button", { class: "btn-ghost", type: "button", onclick: () => openDashboardEditor(dashBox, []) }, "Dashboardtegel maken"),
    h("span", { class: "muted small" }, "Zet meetwaarden, grafieken of bediening op het dashboard."));
  view.append(bar, dashBox, grid, empty);

  const tiles = new Map(); // plugin device id -> tile

  const render = () => {
    const devices = [...state.plugins.values()].filter((d) => (d.tab_id || 0) === tabId);
    empty.hidden = !state.loaded || devices.length > 0;
    bar.hidden = state.plugins.size === 0;
    const ids = devices.map((d) => d.id);
    for (const [id, t] of tiles) if (!ids.includes(id)) { t.stop(); t.node.remove(); tiles.delete(id); }
    for (const d of devices) {
      let t = tiles.get(d.id);
      if (!t) { t = pluginTile(d); tiles.set(d.id, t); }
      t.update(d);
      grid.appendChild(t.node); // moves existing nodes into order
    }
  };
  const unsubscribe = subscribe((change) => {
    if (change.type === "plugin" && tiles.has(change.id)) tiles.get(change.id).update(state.plugins.get(change.id));
    else if (change.type === "device") { for (const t of tiles.values()) t.meterChanged(change.id); }
    else if (change.type === "plugin" || change.type === "plugins" || change.type === "snapshot" || change.type === "removed") render();
  });
  render();
  return () => { unsubscribe(); for (const t of tiles.values()) t.stop(); };
}

/**
 * Opens the dashboard tile editor in box: a new tile or one of the existing
 * ones (chosen in the editor), with the preset items added.
 */
async function openDashboardEditor(box, preset) {
  const close = () => { box.hidden = true; box.replaceChildren(); };
  let list;
  try {
    list = (await tilesApi.list()).tiles || [];
  } catch (e) {
    box.replaceChildren(h("div", { class: "small error-text" }, e.message));
    box.hidden = false;
    return;
  }
  box.replaceChildren(tileEditor(null, {
    choose: list, preset, onCancel: close,
    onDone: (t) => box.replaceChildren(h("div", { class: "notice" },
      t ? `Tegel "${t.name}" staat op het ` : "De tegel is verwijderd. ",
      t ? h("a", { href: "#/dashboard" }, "dashboard") : null, t ? ". " : null,
      h("button", { class: "btn-link", type: "button", onclick: close }, "Sluiten"))),
  }));
  box.hidden = false;
}

/** The item "Op dashboard" starts with: the device's temperature, else its first metric, else a control. */
function dashboardPreset(d) {
  const metrics = d.reading?.metrics || [];
  const m = metrics.find((x) => x.kind === "number" && x.unit === "°C") || metrics[0];
  if (m) return [{ type: "value", device: d.id, metric: m.key }];
  const c = (d.controls || []).find((x) => !x.hidden);
  if (c) return [{ type: "control", device: d.id, control: c.key }];
  return [{ type: "value", device: d.id, metric: "" }];
}

/** Sockets and kWh meters: the HomeWizard devices that can measure a plugin device. */
function meterCandidates() {
  return [...state.devices.values()].filter((d) => d.kind === "socket" || d.kind === "kwh");
}

function pluginTile(initial) {
  let d = initial;
  const head = h("header", { class: "plugin-tile-head" });
  const body = h("div", { class: "plugin-tile-body" });
  const usage = h("div", { class: "plugin-usage", hidden: true });
  const seen = h("div", { class: "muted small" });
  const editor = h("div", { class: "plugin-meter-editor", hidden: true });
  const historyBox = h("div", { class: "plugin-history", hidden: true });
  const meterBtn = h("button", { class: "btn-link", type: "button", onclick: () => toggleEditor() }, "Meter koppelen");
  const historyBtn = h("button", { class: "btn-link", type: "button", onclick: () => toggleHistory() }, "Historie");
  const dashBox = h("div", { class: "dash-editor plugin-dash", hidden: true });
  const dashBtn = h("button", { class: "btn-link", type: "button",
    onclick: () => { if (dashBox.hidden) openDashboardEditor(dashBox, dashboardPreset(d)); else { dashBox.hidden = true; dashBox.replaceChildren(); } } }, "Op dashboard");
  const actions = h("div", { class: "plugin-tile-actions" }, meterBtn, historyBtn, dashBtn);
  const controls = controlPanel();
  // how often it did not respond (shared with Apparaten); loaded again when it goes offline or comes back
  const outages = outageLine(initial.id);
  let wasOnline = !!(initial.reading && initial.reading.online);
  const node = h("article", { class: "card plugin-tile" }, head, body, controls.node, usage, seen, outages.node, actions, editor, historyBox, dashBox);

  let meterKey = "";
  let today = null; // {kwh, eur, complete} of the linked meters, null while unknown
  let costTimer = null;
  let costSeq = 0;

  // the linked meter as a list (empty or one), for the usage and history code
  const meters = () => (d.meter && state.devices.has(d.meter) ? [d.meter] : []);

  function update(next) {
    d = next;
    const online = !!(d.reading && d.reading.online);
    if (online !== wasOnline) { wasOnline = online; outages.refresh(); }
    head.replaceChildren(
      h("div", {},
        h("strong", {}, d.name || d.address),
        h("div", { class: "muted small" }, d.plugin_name)),
      statusPill(online ? "online" : "offline"));
    body.replaceChildren(...metricRows(d.reading));
    controls.draw(d);
    seen.textContent = d.last_seen ? "bijgewerkt " + ago(d.last_seen) : "";
    const key = d.meter || "";
    if (key !== meterKey) {
      meterKey = key;
      today = null;
      loadCosts();
      if (!historyBox.hidden) drawHistory();
    }
    drawUsage();
  }

  function meterChanged(id) {
    if (d.meter === id) drawUsage();
  }

  // live power of the linked meters, and today's usage and costs
  function drawUsage() {
    const ids = meters();
    usage.hidden = ids.length === 0;
    meterBtn.textContent = ids.length ? "Meter wijzigen" : "Meter koppelen";
    meterBtn.hidden = !d.can_link_meter || (meterCandidates().length === 0 && ids.length === 0);
    if (!ids.length) { usage.replaceChildren(); return; }
    let w = 0, known = false;
    for (const id of ids) {
      const v = state.devices.get(id)?.reading?.values?.power_w;
      if (v != null) { w += v; known = true; }
    }
    const names = ids.map((id) => state.devices.get(id)?.name || id).join(" + ");
    // other plugin devices on the same meter: its power is theirs together
    const sharing = [...state.plugins.values()].filter((p) => p.id !== d.id && p.meter && p.meter === d.meter).map((p) => p.name || p.address);
    // filtered: replaceChildren would print a null as text
    usage.replaceChildren(...[
      h("div", { class: "plugin-tile-row" }, h("span", { class: "muted" }, "Vermogen nu"), h("span", {}, known ? join(power(w)) : "–")),
      h("div", { class: "plugin-tile-row" }, h("span", { class: "muted" }, "Vandaag"),
        h("span", {}, today ? `${join(energy(today.kwh))} · ${eur(today.eur)}${today.complete ? "" : " (onvolledig)"}` : "–")),
      h("div", { class: "muted small" }, "gemeten door ", names, " · ",
        h("a", { href: `#/verbruik?view=kosten&kind=energy&device=${encodeURIComponent(ids[0])}` }, "kosten")),
      sharing.length ? h("div", { class: "muted small" }, `Deze meter meet ook ${sharing.join(", ")}: vermogen en kosten zijn van allemaal samen.`) : null,
    ].filter(Boolean));
  }

  // today's usage costs of each linked meter (Calculator.Usage), summed
  async function loadCosts() {
    clearTimeout(costTimer);
    const n = ++costSeq;
    const ids = meters();
    if (!ids.length) return;
    const now = new Date();
    const to = new Date(now.getTime() + 24 * 3600e3);
    try {
      const results = await Promise.all(ids.map((id) =>
        api("GET", `/api/costs?device=${encodeURIComponent(id)}&scope=usage&from=${now.toISOString()}&to=${to.toISOString()}&bucket=day`)
          .then((res) => res.result.buckets[0] || res.result.total)
          .catch(() => null)));
      if (n !== costSeq) return;
      const ok = results.filter(Boolean);
      today = ok.length ? {
        kwh: ok.reduce((s, b) => s + (b.import_kwh || 0), 0),
        eur: ok.reduce((s, b) => s + (b.total_eur || 0), 0),
        complete: ok.length === results.length && ok.every((b) => b.complete),
      } : null;
      drawUsage();
    } finally {
      if (n === costSeq) costTimer = setTimeout(loadCosts, COST_REFRESH_MS);
    }
  }

  // choose the meter (one per device; one meter may measure several devices)
  function toggleEditor() {
    if (!editor.hidden) { editor.hidden = true; return; }
    const candidates = meterCandidates();
    const sel = h("select", { class: "select", "aria-label": "Meter" },
      h("option", { value: "" }, "Geen meter"),
      ...candidates.map((m) => h("option", { value: m.id }, m.name || m.id)));
    sel.value = d.meter && candidates.some((m) => m.id === d.meter) ? d.meter : "";
    const msg = h("div", { class: "muted small" });
    const save = h("button", { class: "btn", type: "button" }, "Opslaan");
    save.addEventListener("click", async () => {
      save.disabled = true;
      try {
        await api("PUT", `/api/plugins/devices/${encodeURIComponent(d.id)}/meter`, { meter: sel.value });
        editor.hidden = true; // the "plugins" event brings the new links
      } catch (e) {
        msg.textContent = e.message;
      } finally {
        save.disabled = false;
      }
    });
    editor.replaceChildren(
      h("div", { class: "muted small" }, "Welke tussenstekker of kWh-meter meet dit apparaat? Zitten meerdere apparaten op één groep, kies dan bij elk dezelfde meter."),
      candidates.length ? sel : h("div", { class: "muted small" }, "Er zijn geen tussenstekkers of kWh-meters."),
      h("div", { class: "row" }, save, h("button", { class: "btn-ghost", type: "button", onclick: () => { editor.hidden = true; } }, "Annuleren")),
      msg);
    editor.hidden = false;
  }

  // history: a numeric metric of the device (e.g. room temperature) and the
  // power of the linked meters, over the last 24 hours
  let metric = "";
  function toggleHistory() {
    historyBox.hidden = !historyBox.hidden;
    if (!historyBox.hidden) drawHistory();
  }

  async function drawHistory() {
    const numeric = (d.reading?.metrics || []).filter((m) => m.kind === "number");
    if (!numeric.some((m) => m.key === metric)) metric = (numeric.find((m) => m.unit === "°C") || numeric[0])?.key || "";
    const select = h("select", { class: "select", "aria-label": "Meting" },
      ...numeric.map((m) => h("option", { value: m.key, selected: m.key === metric }, m.label)));
    select.addEventListener("change", () => { metric = select.value; drawHistory(); });
    const metricChart = h("div", { class: "chart" });
    const powerChart = h("div", { class: "chart" });
    const ids = meters();
    historyBox.replaceChildren(...[
      numeric.length ? select : h("div", { class: "muted small" }, "Dit apparaat heeft geen meetwaarden met een getal."),
      numeric.length ? metricChart : null,
      ids.length ? h("div", { class: "muted small" }, "Vermogen van de gekoppelde meter") : null,
      ids.length ? powerChart : null].filter(Boolean));

    const to = new Date();
    const from = new Date(to.getTime() - 24 * 3600e3);
    const range = `from=${from.toISOString()}&to=${to.toISOString()}&points=288`;
    const m = numeric.find((x) => x.key === metric);
    if (m) {
      try {
        const res = await api("GET", `/api/plugins/devices/${encodeURIComponent(d.id)}/history?metric=${encodeURIComponent(m.key)}&${range}`);
        const stepMs = res.step_s * 1000;
        const f = (v) => (m.unit ? `${fmt.n1(v)} ${m.unit}` : fmt.n1(v));
        lineChart(metricChart, {
          label: m.label, height: 160, zero: false, area: false,
          series: [{ name: m.label, color: "var(--export)", points: res.points.map((p) => ({ t: new Date(p.t).getTime() + stepMs / 2, v: p.avg, min: p.min, max: p.max })) }],
          from: from.getTime(), to: to.getTime(), format: f, axisFormat: (v) => fmt.n1(v),
          title: (t) => dates.dayTime(new Date(t - stepMs / 2)), stepMs, gapMs: Math.max(stepMs * 3, 15 * 60e3),
        });
      } catch (e) {
        metricChart.replaceChildren(h("div", { class: "muted small" }, e.message));
      }
    }
    if (ids.length) {
      try {
        const all = await Promise.all(ids.map((id) => api("GET", `/api/history/rate?device=${encodeURIComponent(id)}&${range}`)));
        const stepMs = all[0].step_s * 1000;
        lineChart(powerChart, {
          label: "Vermogen", height: 160,
          series: all.map((res, i) => ({
            name: state.devices.get(ids[i])?.name || ids[i],
            color: ["var(--import)", "var(--gas)", "var(--accent)"][i % 3],
            points: res.points.map((p) => ({ t: new Date(p.t).getTime() + stepMs / 2, v: p.avg, min: p.min, max: p.max })),
          })),
          from: from.getTime(), to: to.getTime(), format: (v) => join(power(v)),
          axisFormat: (v) => (Math.abs(v) >= 10000 ? `${fmt.n0(v / 1000)}k` : fmt.n0(v)),
          title: (t) => dates.dayTime(new Date(t - stepMs / 2)), stepMs, gapMs: Math.max(stepMs * 3, 5 * 60e3),
        });
      } catch (e) {
        powerChart.replaceChildren(h("div", { class: "muted small" }, e.message));
      }
    }
  }

  function stop() { clearTimeout(costTimer); costSeq++; }

  update(d);
  return { node, update, meterChanged, stop };
}

/**
 * The controls of a plugin device, built from its `controls`: a switch, a
 * setpoint with − and +, a select or an action button. only limits it to the
 * control with that key (a dashboard tile); opts.label(c) may give that
 * control another name (the tile's own label), used for its text, its
 * button and what a screen reader says. draw(d) redraws with the device's
 * latest view; the panel is hidden when there is nothing to control.
 */
export function controlPanel(only, opts = {}) {
  const node = h("div", { class: "plugin-controls", hidden: true });
  const pending = new Map(); // control key -> setpoint being adjusted, not sent yet
  let busy = false;
  let controlMsg = "";
  let d = null;

  function draw(next) {
    if (next) d = next;
    const list = (d?.controls || []).filter((c) => !c.hidden && (!only || c.key === only));
    node.hidden = list.length === 0;
    if (!list.length) { node.replaceChildren(); return; }
    // while a command is being sent, its control shows the value it sends
    const status = (c) => controlStatus(`plugin:${d.id}`, c.key);
    const current = (c) => {
      const st = status(c);
      if (controlBusy(st) && st.value != null) return st.value;
      return (d.reading?.metrics || []).find((m) => m.key === c.metric)?.value;
    };
    const notes = [];
    const rows = list.map((control) => {
      const c = { ...control, label: opts.label?.(control) || control.label };
      const label = h("span", { class: "muted" }, c.label);
      const cur = current(c);
      const note = controlNote(status(c));
      if (note) notes.push(h("div", { class: "small " + (note.error ? "error-text" : "muted") }, list.length > 1 ? `${c.label}: ${note.text}` : note.text));
      if (c.kind === "switch") {
        // the same switch as a socket on the dashboard
        const sw = onOffSwitch(c.label, (on) => send(c, on));
        sw.set(cur === true, { disabled: busy || !d.reading?.online });
        return h("div", { class: "plugin-control" }, label, sw.node);
      }
      if (c.kind === "setpoint") {
        const base = typeof cur === "number" ? cur : (c.min + c.max) / 2;
        const v = pending.has(c.key) ? pending.get(c.key) : base;
        const step = (dir) => {
          const next = Math.min(c.max, Math.max(c.min, Math.round((v + dir * c.step) / c.step) * c.step));
          pending.set(c.key, next);
          draw();
        };
        const changed = pending.has(c.key) && pending.get(c.key) !== cur;
        return h("div", { class: "plugin-control" }, label,
          h("span", { class: "plugin-setpoint" },
            h("button", { class: "btn-ghost", type: "button", "aria-label": c.label + " lager", disabled: busy || v <= c.min, onclick: () => step(-1) }, "−"),
            h("span", { class: "plugin-setpoint-value" }, `${fmt.n1(v)}${c.unit ? " " + c.unit : ""}`),
            h("button", { class: "btn-ghost", type: "button", "aria-label": c.label + " hoger", disabled: busy || v >= c.max, onclick: () => step(1) }, "+"),
            changed ? h("button", { class: "btn", type: "button", disabled: busy, onclick: () => send(c, v) }, "Instellen") : null));
      }
      if (c.kind === "select") {
        const sel = h("select", { class: "select", "aria-label": c.label, disabled: busy },
          ...(c.options || []).map((o) => h("option", { value: o.value, selected: o.value === cur || o.label === cur }, o.label)));
        sel.addEventListener("change", () => send(c, sel.value));
        return h("div", { class: "plugin-control" }, label, sel);
      }
      return h("div", { class: "plugin-control" }, h("span"),
        h("button", { class: "btn-ghost", type: "button", disabled: busy,
          onclick: () => { if (!c.confirm || confirm(c.confirm)) send(c, null); } }, c.label));
    });
    node.replaceChildren(...rows, ...notes, controlMsg ? h("div", { class: "small error-text" }, controlMsg) : h("span"));
  }

  async function send(c, value) {
    busy = true;
    controlMsg = "";
    draw();
    try {
      // the gateway sends it in the background, checks it and tries again
      const st = await api("POST", `/api/plugins/devices/${encodeURIComponent(d.id)}/control`, { key: c.key, value });
      pending.delete(c.key);
      if (st) applyControl(st);
    } catch (e) {
      controlMsg = e.message;
    } finally {
      busy = false;
      draw();
    }
  }

  return { node, draw };
}

function metricRows(r) {
  if (r && r.metrics) {
    // states (on/off, mode, tado's heat demand) in the same chips as on the dashboard
    return r.metrics.map((m) => {
      const tone = metricTone(m);
      return h("div", { class: "plugin-tile-row" },
        h("span", { class: "muted" }, m.label),
        tone ? toneChip(m.kind === "bool" ? (m.value ? "Aan" : "Uit") : fmtMetric(m), tone) : h("span", {}, fmtMetric(m)));
    });
  }
  if (r && r.error) return [h("div", { class: "plugin-tile-row error-text" }, r.error)];
  return [h("div", { class: "muted small" }, "Nog geen meting")];
}

export function fmtMetric(m) {
  if (m.kind === "number" && typeof m.value === "number") return metricText(m.value, m.unit);
  const v = m.kind === "bool" ? (m.value ? "ja" : "nee") : m.value;
  return m.unit ? `${v} ${m.unit}` : `${v}`;
}
