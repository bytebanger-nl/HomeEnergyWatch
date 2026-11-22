// "Klimaat Apparaten": tiles of the user's own on the dashboard with values,
// charts and controls of plugin devices (/api/dashboard/tiles). There are
// none until the user makes one (on Klimaat: "Dashboardtegel maken" or "Op
// dashboard" on a device); without tiles the dashboard shows nothing of it.
//
// A tile has a name, a size (small or wide), a form (style: value, list,
// grid, big, spark; STYLES) and items:
//   value   a metric's current value, nothing else unless asked for: the
//           device's name, what it is set to (target), the change over the
//           last hour, today's lowest and highest
//   status  a device's state as a label ("Verwarmen · 22 °C")
//   chart   1-4 numeric metrics, of one or several devices, over 6 h, 24 h
//           or 7 days, with their current value in the legend
//   control one control of the device (only when control is allowed), or a
//           manual action (an automation started by hand, actions.js): its
//           switch or button, what it is doing and, when it regulates, its
//           target
import { state, subscribe } from "./app.js";
import { lineChart, sparkline } from "./charts.js";
import { controlPanel, fmtMetric } from "./klimaat.js";
import { actionControl, manualActions, refreshRules, watchRules } from "./actions.js";
import { runStatus, tuneRow } from "./rules.js";
import { h, api, fmt, dates, metricTone, metricParts, metricText } from "./util.js";

const REFRESH_MS = 5 * 60e3; // history behind values and charts
const COLORS = ["var(--export)", "var(--import)", "var(--gas)", "var(--accent)"];
const PERIODS = [[6, "6 uur"], [24, "24 uur"], [168, "7 dagen"]];
const MAX_ITEMS = 12, MAX_SERIES = 4;

export const tilesApi = {
  list: () => api("GET", "/api/dashboard/tiles"),
  save: (t) => {
    const body = { name: t.name, size: t.size, style: t.style || "value", items: t.items };
    return t.id ? api("PUT", `/api/dashboard/tiles/${t.id}`, body) : api("POST", "/api/dashboard/tiles", body);
  },
  remove: (id) => api("DELETE", `/api/dashboard/tiles/${id}`),
  order: (ids) => api("PUT", "/api/dashboard/tiles/order", { ids }),
};

const device = (id) => state.plugins.get(id);
const deviceName = (id) => { const d = device(id); return d ? d.name || d.address : "?"; };
const metricOf = (id, key) => device(id)?.reading?.metrics?.find((m) => m.key === key);
const metricLabel = (id, key) => metricOf(id, key)?.label || key;

async function history(id, metric, from, to, points) {
  const res = await api("GET", `/api/plugins/devices/${encodeURIComponent(id)}/history?metric=${encodeURIComponent(metric)}&from=${from.toISOString()}&to=${to.toISOString()}&points=${points}`);
  const stepMs = res.step_s * 1000;
  return { stepMs, points: res.points.map((p) => ({ t: new Date(p.t).getTime() + stepMs / 2, v: p.avg, min: p.min, max: p.max })) };
}

function legendItem(color, label) {
  const key = h("span", { class: "key" });
  key.style.background = color;
  return h("span", {}, key, label);
}

// ---- the section on the dashboard ----

export function myTiles() {
  const grid = h("div", { class: "grid my-tiles" });
  const editBtn = h("button", { class: "btn-link", type: "button", onclick: () => { editing = !editing; render(); } }, "Aanpassen");
  const addBtn = h("button", { class: "btn-ghost", type: "button", hidden: true, onclick: () => { editorFor = 0; render(); } }, "Tegel toevoegen");
  const node = h("section", { class: "my-tiles-section", "aria-label": "Klimaat Apparaten", hidden: true },
    h("div", { class: "section-head" }, h("h2", { class: "section-title" }, "Klimaat Apparaten"), h("div", { class: "row" }, addBtn, editBtn)),
    grid);
  let tiles = [];
  let max = 20;
  let editing = false;
  let editorFor = null; // id of the tile being edited, 0 for a new one
  let editor = null;
  const views = new Map(); // tile id -> view

  const load = async () => {
    try {
      const res = await tilesApi.list();
      tiles = res.tiles || [];
      max = res.max || max;
    } catch {
      tiles = []; // e.g. a gateway without plugins
    }
    render();
  };

  const move = async (i, dir) => {
    const j = i + dir;
    if (j < 0 || j >= tiles.length) return;
    [tiles[i], tiles[j]] = [tiles[j], tiles[i]];
    render();
    try { await tilesApi.order(tiles.map((t) => t.id)); } catch { load(); }
  };

  function render() {
    // nothing at all until there is a tile
    node.hidden = tiles.length === 0 && editorFor == null;
    editBtn.textContent = editing ? "Klaar" : "Aanpassen";
    addBtn.hidden = !editing || tiles.length >= max || editorFor != null;
    const ids = new Set(tiles.map((t) => t.id));
    for (const [id, v] of views) if (!ids.has(id)) { v.stop(); v.node.remove(); views.delete(id); }
    const children = [];
    tiles.forEach((t, i) => {
      if (editorFor === t.id) { children.push(editorNode(t)); return; }
      let v = views.get(t.id);
      if (!v || v.tile !== t) { v?.stop(); v = tileView(t); views.set(t.id, v); }
      v.setEditing(editing, {
        up: i > 0 ? () => move(i, -1) : null,
        down: i < tiles.length - 1 ? () => move(i, 1) : null,
        edit: () => { editorFor = t.id; render(); },
      });
      children.push(v.node);
    });
    if (editorFor === 0) children.push(editorNode(null));
    if (editorFor == null) editor = null;
    grid.replaceChildren(...children);
  }

  function editorNode(t) {
    if (!editor || editor.for !== editorFor) {
      editor = { for: editorFor, node: tileEditor(t, { onDone: () => { editorFor = null; load(); }, onCancel: () => { editorFor = null; render(); } }) };
    }
    return editor.node;
  }

  const unsubscribe = subscribe((change) => {
    if (change.type === "plugin") { for (const v of views.values()) v.update(change.id); }
    else if (change.type === "plugins" || change.type === "snapshot") { for (const v of views.values()) v.update(); }
  });

  return {
    node,
    start: load,
    stop: () => { unsubscribe(); for (const v of views.values()) v.stop(); },
  };
}

// The forms of a tile (tile.style): how its values are laid out.
export const STYLES = [
  ["value", "Alleen de waarde"],
  ["list", "Lijst"],
  ["grid", "Raster"],
  ["big", "Eén grote waarde"],
  ["spark", "Waarde met lijntje"],
];

function tileView(tile) {
  const style = STYLES.some(([id]) => id === tile.style) ? tile.style : "value";
  const actions = h("div", { class: "row my-tile-edit", hidden: true });
  const items = (tile.items || []).map((it) => itemView(it, style));
  const body = h("div", { class: `my-tile-body style-${style}` }, ...items.map((i) => i.node));
  const node = h("article", { class: `card my-tile${tile.size === "wide" ? " wide" : ""}` },
    h("header", { class: "my-tile-head" }, h("strong", {}, tile.name), actions),
    items.length ? body : h("div", { class: "muted small" }, "Deze tegel is nog leeg. Kies Aanpassen → Bewerken."));
  return {
    tile,
    node,
    update: (id) => { for (const i of items) i.update(id); },
    stop: () => { for (const i of items) i.stop(); },
    setEditing(on, { up, down, edit }) {
      actions.hidden = !on;
      actions.replaceChildren(
        h("button", { class: "btn-ghost", type: "button", "aria-label": "Tegel naar voren", disabled: !up, onclick: up }, "↑"),
        h("button", { class: "btn-ghost", type: "button", "aria-label": "Tegel naar achteren", disabled: !down, onclick: down }, "↓"),
        h("button", { class: "btn-ghost", type: "button", onclick: edit }, "Bewerken"));
    },
  };
}

function itemView(it, style) {
  if (it.type === "chart") return chartItem(it);
  if (it.type === "control") return controlItem(it);
  if (it.type === "status") return statusItem(it);
  return valueItem(it, style);
}

// What an item shows when its device is not (yet) known. Before the
// gateway's first snapshot the plugin devices are simply not loaded; only
// after it is a missing device really gone. The item keeps its own elements
// and only hides them, so it fills in once the device arrives (replacing
// them made a tile drawn before the snapshot stay "gone").
function absent() {
  const el = h("div", { class: "muted small", hidden: true });
  return {
    node: el,
    /** Shows the notice when the device is missing; reports whether it is there. */
    check(present, parts) {
      el.hidden = present;
      if (!present) el.textContent = state.loaded ? "Dit apparaat bestaat niet meer." : "Laden…";
      for (const p of parts) p.hidden = !present;
      return present;
    },
  };
}

/** A metric's value split in number and unit, so the unit can be smaller. */
function valueParts(m) {
  if (!m) return ["–", ""];
  if (m.kind === "number" && typeof m.value === "number") return metricParts(m.value, m.unit);
  return [fmtMetric(m), ""];
}

/**
 * A value: by default only the number, under the item's own name. On
 * request (the item's options) also the device's name, what the device is
 * set to (target), the change over the last hour and today's lowest and
 * highest; in the form "spark" a small line of the last hours.
 */
function valueItem(it, style) {
  const label = h("div", { class: "my-tile-label" });
  const num = h("span", {});
  const unit = h("span", { class: "my-tile-unit" });
  const target = h("span", { class: "my-tile-target", hidden: true });
  const value = h("div", { class: "my-tile-value" }, num, unit, target);
  const spark = h("div", { class: "my-tile-spark", hidden: style !== "spark" });
  const extra = h("div", { class: "muted small" });
  const missing = absent();
  const main = h("div", { class: "my-tile-main" }, label, value, extra);
  const node = h("div", { class: "my-tile-item" }, missing.node, main, spark);
  const wantsHistory = style === "spark" || it.show_trend || it.show_range;
  let hist = null; // {ago, min, max, points} from the history, numbers only
  let timer = null;
  let loaded = false; // history fetched with the device known

  const draw = () => {
    if (!missing.check(!!device(it.device), [main, spark])) return;
    spark.hidden = style !== "spark";
    label.textContent = (it.label || metricLabel(it.device, it.metric)) + (it.show_device ? ` · ${deviceName(it.device)}` : "");
    const m = metricOf(it.device, it.metric);
    const [n, u] = valueParts(m);
    num.textContent = n;
    unit.textContent = u ? ` ${u}` : "";
    value.dataset.tone = metricTone(m); // e.g. tado's heat demand: green at 0, warm above
    const t = it.target ? metricOf(it.device, it.target) : null;
    target.hidden = !t;
    if (t) target.textContent = ` → ${fmtMetric(t)}`;
    const parts = [];
    if (m?.kind === "number" && typeof m.value === "number" && hist) {
      if (it.show_trend && hist.ago != null) {
        const diff = m.value - hist.ago;
        const arrow = Math.abs(diff) < 0.05 ? "→" : diff > 0 ? "↑" : "↓";
        parts.push(`${arrow} ${diff > 0 ? "+" : ""}${metricText(diff, m.unit)} in een uur`);
      }
      // a range only when the value moved today (ista's units change once a day)
      if (it.show_range && hist.min != null && hist.max !== hist.min) {
        const [lo] = metricParts(hist.min, m.unit);
        parts.push(`vandaag ${lo} – ${metricText(hist.max, m.unit)}`);
      }
    }
    extra.textContent = parts.join(" · ");
    extra.hidden = parts.length === 0;
    if (style === "spark" && hist?.points?.length) {
      requestAnimationFrame(() => sparkline(spark, hist.points, { from: hist.from, to: hist.to, height: 44, color: "var(--import)" }));
    }
  };

  const load = async () => {
    clearTimeout(timer);
    const m = metricOf(it.device, it.metric);
    if (wantsHistory && device(it.device) && (!m || m.kind === "number")) {
      loaded = true;
      const to = new Date();
      const midnight = new Date(to);
      midnight.setHours(0, 0, 0, 0);
      const from = new Date(Math.min(midnight.getTime(), to.getTime() - 6 * 3600e3));
      try {
        const res = await history(it.device, it.metric, from, to, 200);
        const today = res.points.filter((p) => p.t >= midnight.getTime());
        const hourAgo = to.getTime() - 3600e3;
        let ago = null, best = Infinity;
        for (const p of res.points) {
          const d = Math.abs(p.t - hourAgo);
          if (d < best && d <= 20 * 60e3) { best = d; ago = p.v; }
        }
        const sixAgo = to.getTime() - 6 * 3600e3;
        hist = {
          ago,
          min: today.length ? Math.min(...today.map((p) => p.min ?? p.v)) : null,
          max: today.length ? Math.max(...today.map((p) => p.max ?? p.v)) : null,
          points: res.points.filter((p) => p.t >= sixAgo).map((p) => ({ t: p.t, v: p.v })),
          from: sixAgo, to: to.getTime(),
        };
      } catch {
        hist = null;
      }
      draw();
    }
    timer = setTimeout(load, REFRESH_MS);
  };

  draw();
  load();
  return {
    node,
    update: (id) => {
      if (id && id !== it.device) return;
      draw();
      // the device arrived after the tile was drawn: fetch its history now
      if (wantsHistory && !loaded && device(it.device)) load();
    },
    stop: () => clearTimeout(timer),
  };
}

/** A device's state as a label: "Verwarmen · 22 °C", "Aan", "Schema · 14 °C". */
function statusItem(it) {
  const label = h("span", { class: "my-tile-status-name" });
  const chip = h("span", { class: "tone-chip" });
  const missing = absent();
  const row = h("div", { class: "my-tile-status" }, label, chip);
  const node = h("div", { class: "my-tile-item" }, missing.node, row);
  const draw = () => {
    if (!missing.check(!!device(it.device), [row])) return;
    label.textContent = it.label || deviceName(it.device);
    const m = metricOf(it.device, it.metric);
    const m2 = it.metric2 ? metricOf(it.device, it.metric2) : null;
    const first = !m ? "–" : m.kind === "bool" ? (m.value ? "Aan" : "Uit") : fmtMetric(m);
    chip.textContent = m2 ? `${first} · ${fmtMetric(m2)}` : first;
    chip.className = `tone-chip ${metricTone(m)}`;
  };
  draw();
  return { node, update: (id) => { if (!id || id === it.device) draw(); }, stop: () => {} };
}

function chartItem(it) {
  const title = h("div", { class: "muted small" });
  const legend = h("div", { class: "legend" });
  const chart = h("div", { class: "chart" });
  const node = h("div", { class: "my-tile-item" }, title, chart, legend);
  let timer = null;
  let missingAtLoad = true; // a series' device was not known at the last load
  const series = it.series || [];
  const period = PERIODS.find(([hrs]) => hrs === it.hours)?.[1] || `${it.hours} uur`;
  let lastNames = [];
  // each line with its current value, e.g. "Binnen 18,2 °C"
  const drawLegend = () => {
    legend.replaceChildren(...series.map((s, i) => {
      const m = metricOf(s.device, s.metric);
      return legendItem(COLORS[i % COLORS.length], m ? `${lastNames[i] || ""} ${fmtMetric(m)}` : lastNames[i] || "");
    }));
  };

  const load = async () => {
    clearTimeout(timer);
    // the device's name only where two lines would otherwise have the same name
    const base = series.map((s) => (it.label && series.length === 1 ? it.label : metricLabel(s.device, s.metric)));
    const names = base.map((n, i) => (base.indexOf(n) !== base.lastIndexOf(n) ? `${n} · ${deviceName(series[i].device)}` : n));
    title.textContent = `${it.label || (series.length === 1 ? names[0] : series.map((s) => metricLabel(s.device, s.metric)).join(" en "))} · ${period}`;
    lastNames = names;
    drawLegend();
    const to = new Date();
    const from = new Date(to.getTime() - it.hours * 3600e3);
    missingAtLoad = series.some((s) => !device(s.device));
    const units = new Set(series.map((s) => metricOf(s.device, s.metric)?.unit || ""));
    const unit = units.size === 1 ? [...units][0] : "";
    try {
      const all = await Promise.all(series.map((s) => (device(s.device)
        ? history(s.device, s.metric, from, to, 288)
        : Promise.resolve({ stepMs: 0, points: [] }))));
      const stepMs = all.find((r) => r.stepMs)?.stepMs || 5 * 60e3;
      lineChart(chart, {
        label: title.textContent, height: 160, zero: false, area: false,
        series: all.map((r, i) => ({ name: names[i], color: COLORS[i % COLORS.length], points: r.points })),
        from: from.getTime(), to: to.getTime(),
        format: (v) => (unit ? `${fmt.n1(v)} ${unit}` : fmt.n1(v)), axisFormat: (v) => fmt.n1(v),
        title: (t) => dates.dayTime(new Date(t - stepMs / 2)), stepMs, gapMs: Math.max(stepMs * 3, 15 * 60e3),
      });
    } catch (e) {
      chart.replaceChildren(h("div", { class: "muted small" }, e.message));
    }
    timer = setTimeout(load, REFRESH_MS);
  };

  load();
  return {
    node,
    // devices that were not known yet (before the first snapshot): load again
    update: () => {
      if (missingAtLoad && series.every((s) => device(s.device))) load();
      else drawLegend();
    },
    stop: () => clearTimeout(timer),
  };
}

function actionItem(it) {
  const node = h("div", { class: "my-tile-item" });
  let last = ""; // the rule as drawn: a poll that changed nothing keeps the − and + being tapped
  let msg = "";
  const draw = (rules) => {
    const r = rules.find((x) => x.id === it.rule);
    const key = JSON.stringify([r, msg]);
    if (key === last) return;
    last = key;
    if (!r || !r.manual) {
      node.replaceChildren(h("div", { class: "muted small" }, "Deze actie bestaat niet meer."));
      return;
    }
    const s = r.state || {};
    const reg = r.actions.find((a) => a.type === "regulate");
    let status = "";
    if (s.run) status = runStatus(r, s.run);
    else if (s.last_result && (s.active || s.momentary)) status = s.last_result;
    const fail = (m) => { msg = m; draw(rules); };
    node.replaceChildren(...[
      h("div", { class: "plugin-control" },
        h("span", { class: "muted" }, it.label || r.name),
        actionControl(r, { onDone: () => { msg = ""; }, onError: fail })),
      // a switched-off action cannot be started: say where to switch it on
      !r.enabled ? h("div", { class: "small error-text" }, "Deze actie is uitgeschakeld. Schakel hem in onder ",
        h("a", { href: "#/automatisering" }, "Automatisering"), " (Inschakelen op de kaart).") : null,
      status && r.enabled ? h("div", { class: "muted small" }, status) : null,
      reg && s.active ? tuneRow(reg, async (target, band) => {
        try { await api("PATCH", `/api/rules/${r.id}/regulate`, { target, band }); msg = ""; } catch (e) { msg = e.message; }
        refreshRules();
      }, { band: false }) : null,
      msg ? h("div", { class: "small error-text" }, msg) : null,
    ].filter(Boolean));
  };
  node.replaceChildren(h("div", { class: "muted small" }, "Laden…"));
  const stop = watchRules(draw);
  return { node, update: () => {}, stop };
}

function controlItem(it) {
  if (it.rule) return actionItem(it);
  // the tile's own name, else the control's; "Modus · Airco" with the device name
  const panel = controlPanel(it.control, {
    label: (c) => (it.label || c.label) + (it.show_device ? ` · ${deviceName(it.device)}` : ""),
  });
  const hint = h("div", { class: "muted small", hidden: true });
  const missing = absent();
  const node = h("div", { class: "my-tile-item" }, missing.node, panel.node, hint);
  const draw = () => {
    const d = device(it.device);
    if (!missing.check(!!d, [hint])) { panel.node.hidden = true; return; }
    panel.draw(d);
    hint.hidden = !panel.node.hidden;
    hint.replaceChildren(`${deviceName(it.device)}: bediening staat uit of is er niet (`,
      h("a", { href: "#/instellingen" }, "Instellingen → Plugins"), ").");
  };
  draw();
  return { node, update: (id) => { if (!id || id === it.device) draw(); }, stop: () => {} };
}

// ---- the editor (dashboard and Klimaat) ----

/**
 * The form for one tile. tile null makes a new one. opts: onDone(saved),
 * onCancel(), and for Klimaat: choose (the existing tiles, so the user picks
 * the tile to add to) and preset (items to add).
 */
export function tileEditor(tile, opts = {}) {
  const node = h("article", { class: "card my-tile my-tile-editor wide" });
  const fresh = (t) => (t ? structuredClone({ id: t.id, name: t.name, size: t.size, style: t.style || "value", items: t.items || [] }) : { name: "", size: "small", style: "value", items: [] });
  let draft = fresh(tile);
  draft.items.push(...structuredClone(opts.preset || []));
  let msg = "";
  let busy = false;
  let actions = []; // manual actions, for a control item
  api("GET", "/api/rules").then((res) => { actions = manualActions(res.rules); draw(); }).catch(() => {});

  const plugins = () => [...state.plugins.values()].sort((a, b) => (a.name || "").localeCompare(b.name || ""));
  const deviceSelect = (value, onchange, filter = () => true) => {
    const list = plugins().filter(filter);
    const sel = h("select", { class: "select", "aria-label": "Apparaat" },
      h("option", { value: "" }, "Kies een apparaat"),
      ...list.map((d) => h("option", { value: d.id, selected: d.id === value }, `${d.name || d.address} · ${d.plugin_name}`)),
      value && !list.some((d) => d.id === value) ? h("option", { value, selected: true }, "(bestaat niet meer)") : null);
    sel.addEventListener("change", () => onchange(sel.value));
    return sel;
  };
  const metricSelect = (id, value, onchange, numeric) => {
    const list = (device(id)?.reading?.metrics || []).filter((m) => !numeric || m.kind === "number");
    const sel = h("select", { class: "select", "aria-label": "Meetwaarde", disabled: !id },
      h("option", { value: "" }, list.length || !id ? "Kies een meetwaarde" : "Nog geen meetwaarden"),
      ...list.map((m) => h("option", { value: m.key, selected: m.key === value }, m.unit ? `${m.label} (${m.unit})` : m.label)),
      value && !list.some((m) => m.key === value) ? h("option", { value, selected: true }, value) : null);
    sel.addEventListener("change", () => onchange(sel.value));
    return sel;
  };
  const labelInput = (it, placeholder) => {
    const inp = h("input", { type: "text", maxlength: 60, value: it.label || "", placeholder, "aria-label": "Eigen naam (optioneel)" });
    inp.addEventListener("input", () => { it.label = inp.value; });
    return h("label", { class: "field" }, "Eigen naam (optioneel)", inp);
  };
  const smallBtn = (text, label, onclick, disabled) => h("button", { class: "btn-ghost", type: "button", "aria-label": label, disabled, onclick }, text);

  function itemEditor(it, i) {
    const n = draft.items.length;
    const move = (dir) => { const j = i + dir; [draft.items[i], draft.items[j]] = [draft.items[j], draft.items[i]]; draw(); };
    const head = h("div", { class: "my-tile-item-head" },
      h("strong", { class: "small" }, { value: "Waarde", status: "Status", chart: "Grafiek", control: "Bediening" }[it.type]),
      h("span", { class: "row" },
        smallBtn("↑", "Omhoog", () => move(-1), i === 0),
        smallBtn("↓", "Omlaag", () => move(1), i === n - 1),
        smallBtn("✕", "Verwijderen", () => { draft.items.splice(i, 1); draw(); })));
    const fields = [];
    const check = (key, text) => {
      const box = h("input", { type: "checkbox", checked: !!it[key] });
      box.addEventListener("change", () => { it[key] = box.checked; });
      return h("label", { class: "check" }, box, text);
    };
    if (it.type === "value") {
      // what the device is set to, next to the value: "18,2 °C → 20,0 °C"
      const target = metricSelect(it.device, it.target, (v) => { it.target = v; }, true);
      target.options[0].textContent = "Niet tonen";
      fields.push(
        h("div", { class: "row" },
          deviceSelect(it.device, (v) => { it.device = v; it.metric = ""; it.target = ""; draw(); }),
          metricSelect(it.device, it.metric, (v) => { it.metric = v; })),
        labelInput(it, "bijv. Buiten"),
        h("label", { class: "field" }, "Ingesteld op (optioneel)", target),
        h("div", { class: "my-tile-options" },
          check("show_device", "Apparaatnaam tonen"),
          check("show_trend", "Verloop van het laatste uur"),
          check("show_range", "Laagste en hoogste van vandaag")));
    } else if (it.type === "status") {
      const second = metricSelect(it.device, it.metric2, (v) => { it.metric2 = v; }, true);
      second.options[0].textContent = "Geen";
      fields.push(
        h("div", { class: "row" },
          deviceSelect(it.device, (v) => { it.device = v; it.metric = ""; it.metric2 = ""; draw(); }),
          metricSelect(it.device, it.metric, (v) => { it.metric = v; })),
        h("label", { class: "field" }, "Met waarde (optioneel)", second),
        labelInput(it, "standaard de naam van het apparaat"));
    } else if (it.type === "chart") {
      it.series = it.series?.length ? it.series : [{ device: "", metric: "" }];
      it.series.forEach((s, k) => {
        fields.push(h("div", { class: "row" },
          h("span", { class: "key", style: `background:${COLORS[k % COLORS.length]}` }),
          deviceSelect(s.device, (v) => { s.device = v; s.metric = ""; draw(); }),
          metricSelect(s.device, s.metric, (v) => { s.metric = v; }, true),
          it.series.length > 1 ? smallBtn("✕", "Lijn verwijderen", () => { it.series.splice(k, 1); draw(); }) : null));
      });
      const period = h("select", { class: "select", "aria-label": "Periode" },
        ...PERIODS.map(([hrs, text]) => h("option", { value: hrs, selected: hrs === (it.hours || 24) }, text)));
      period.addEventListener("change", () => { it.hours = Number(period.value); });
      it.hours = it.hours || 24;
      fields.push(
        h("div", { class: "row" },
          it.series.length < MAX_SERIES ? h("button", { class: "btn-link", type: "button", onclick: () => { it.series.push({ device: "", metric: "" }); draw(); } }, "+ Lijn") : null,
          period),
        labelInput(it, "bijv. Binnen en buiten"));
    } else {
      // a device and one of its controls, or a manual action
      const pick = h("select", { class: "select", "aria-label": "Apparaat of actie" },
        h("option", { value: "" }, actions.length ? "Kies een apparaat of actie" : "Kies een apparaat"),
        h("optgroup", { label: "Apparaten" },
          ...plugins().filter((x) => (x.controls || []).length > 0 || x.id === it.device)
            .map((x) => h("option", { value: x.id, selected: !it.rule && x.id === it.device }, `${x.name || x.address} · ${x.plugin_name}`))),
        actions.length ? h("optgroup", { label: "Acties" },
          ...actions.map((r) => h("option", { value: `rule:${r.id}`, selected: r.id === it.rule }, r.name))) : null,
        it.rule && !actions.some((r) => r.id === it.rule) ? h("option", { value: `rule:${it.rule}`, selected: true }, "(actie bestaat niet meer)") : null,
        !it.rule && it.device && !device(it.device) ? h("option", { value: it.device, selected: true }, "(bestaat niet meer)") : null);
      pick.addEventListener("change", () => {
        const v = pick.value;
        if (v.startsWith("rule:")) { it.rule = Number(v.slice(5)); it.device = ""; it.control = ""; } else { delete it.rule; it.device = v; it.control = ""; }
        draw();
      });
      if (it.rule) {
        fields.push(h("div", { class: "row" }, pick),
          labelInput(it, "bijv. Woonkamer warm"),
          h("div", { class: "muted small" }, "Een actie maak je onder Automatisering (Starten: handmatig). Met acties bij uitzetten, een wachtstap of regelen is het een schakelaar, anders een knop."));
        return h("div", { class: "my-tile-item editing" }, head, ...fields);
      }
      const d = device(it.device);
      const controls = (d?.controls || []).filter((c) => !c.hidden);
      const sel = h("select", { class: "select", "aria-label": "Bediening", disabled: !it.device },
        h("option", { value: "" }, controls.length || !it.device ? "Kies een bediening" : "Bediening staat uit"),
        ...controls.map((c) => h("option", { value: c.key, selected: c.key === it.control }, c.label)),
        it.control && !controls.some((c) => c.key === it.control) ? h("option", { value: it.control, selected: true }, it.control) : null);
      sel.addEventListener("change", () => { it.control = sel.value; });
      fields.push(h("div", { class: "row" }, pick, sel),
        labelInput(it, "bijv. Airco woonkamer"),
        h("div", { class: "my-tile-options" }, check("show_device", "Apparaatnaam tonen")));
      if (!plugins().some((x) => (x.controls || []).length) && !actions.length) {
        fields.push(h("div", { class: "muted small" }, "Geen enkel apparaat heeft bediening aan. Zet die aan onder ", h("a", { href: "#/instellingen" }, "Instellingen → Plugins"), ", of maak een handmatige actie onder Automatisering."));
      }
    }
    return h("div", { class: "my-tile-item editing" }, head, ...fields);
  }

  async function save() {
    busy = true; msg = ""; draw();
    try {
      const saved = await tilesApi.save(draft);
      opts.onDone?.(saved);
    } catch (e) {
      busy = false; msg = e.message; draw();
    }
  }

  async function remove() {
    if (!confirm(`Tegel "${draft.name}" verwijderen?`)) return;
    busy = true; draw();
    try {
      await tilesApi.remove(draft.id);
      opts.onDone?.(null);
    } catch (e) {
      busy = false; msg = e.message; draw();
    }
  }

  function draw() {
    const name = h("input", { type: "text", maxlength: 60, value: draft.name, placeholder: "bijv. Klimaat", "aria-label": "Naam van de tegel" });
    name.addEventListener("input", () => { draft.name = name.value; });
    const size = h("select", { class: "select", "aria-label": "Grootte" },
      h("option", { value: "small", selected: draft.size !== "wide" }, "Smal"),
      h("option", { value: "wide", selected: draft.size === "wide" }, "Breed"));
    size.addEventListener("change", () => { draft.size = size.value; });
    const form = h("select", { class: "select", "aria-label": "Vorm" },
      ...STYLES.map(([id, text]) => h("option", { value: id, selected: id === (draft.style || "value") }, text)));
    form.addEventListener("change", () => { draft.style = form.value; });
    let target = null;
    if (opts.choose?.length) {
      target = h("select", { class: "select", "aria-label": "Tegel" },
        h("option", { value: "", selected: !draft.id }, "Nieuwe tegel"),
        ...opts.choose.map((t) => h("option", { value: t.id, selected: t.id === draft.id }, t.name)));
      target.addEventListener("change", () => {
        const t = opts.choose.find((x) => String(x.id) === target.value);
        draft = fresh(t);
        draft.items.push(...structuredClone(opts.preset || []));
        draw();
      });
    }
    const add = (type) => () => {
      const base = { type };
      if (type === "chart") { base.series = [{ device: "", metric: "" }]; base.hours = 24; }
      draft.items.push(base);
      draw();
    };
    const full = draft.items.length >= MAX_ITEMS;
    node.replaceChildren(...[
      h("strong", {}, draft.id ? "Tegel bewerken" : "Nieuwe dashboardtegel"),
      target ? h("label", { class: "field" }, "Toevoegen aan", target) : null,
      h("div", { class: "row" }, h("label", { class: "field grow" }, "Naam", name), h("label", { class: "field" }, "Grootte", size), h("label", { class: "field" }, "Vorm", form)),
      ...draft.items.map(itemEditor),
      draft.items.length ? null : h("div", { class: "muted small" }, "Voeg een waarde, status, grafiek of bediening toe."),
      h("div", { class: "row" },
        h("button", { class: "btn-ghost", type: "button", disabled: full, onclick: add("value") }, "+ Waarde"),
        h("button", { class: "btn-ghost", type: "button", disabled: full, onclick: add("status") }, "+ Status"),
        h("button", { class: "btn-ghost", type: "button", disabled: full, onclick: add("chart") }, "+ Grafiek"),
        h("button", { class: "btn-ghost", type: "button", disabled: full, onclick: add("control") }, "+ Bediening")),
      msg ? h("div", { class: "small error-text" }, msg) : null,
      h("div", { class: "row" },
        h("button", { class: "btn primary", type: "button", disabled: busy, onclick: save }, "Opslaan"),
        h("button", { class: "btn-ghost", type: "button", disabled: busy, onclick: () => opts.onCancel?.() }, "Annuleren"),
        draft.id ? h("button", { class: "btn-ghost danger", type: "button", disabled: busy, onclick: remove }, "Tegel verwijderen") : null),
    ].filter(Boolean));
  }

  draw();
  return node;
}
