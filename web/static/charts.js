// Small SVG chart helpers: sparkline, time line chart with crosshair, and a
// column chart with import above and export below one zero baseline.
// Labels are always inserted with textContent.

const SVG = "http://www.w3.org/2000/svg";

function el(name, attrs = {}, parent) {
  const node = document.createElementNS(SVG, name);
  for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
  if (parent) parent.appendChild(node);
  return node;
}

function text(parent, x, y, value, anchor = "middle", baseline = "auto") {
  const t = el("text", { x, y, "text-anchor": anchor, "dominant-baseline": baseline }, parent);
  t.textContent = value;
  return t;
}

/** Nice round tick values covering [min, max]. */
export function niceTicks(min, max, count = 4) {
  if (min === max) { max = min + 1; }
  const span = max - min;
  const raw = span / count;
  const mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => span / s <= count) || 10 * mag;
  const lo = Math.floor(min / step) * step;
  const hi = Math.ceil(max / step) * step;
  const ticks = [];
  for (let v = lo; v <= hi + step / 2; v += step) ticks.push(Math.abs(v) < step / 1e6 ? 0 : v);
  return ticks;
}

// ---------- tooltip ----------
const tip = () => document.getElementById("tooltip");

export function showTooltip(clientX, clientY, title, rows) {
  const t = tip();
  if (!t) return;
  t.replaceChildren();
  const h = document.createElement("div");
  h.className = "tt-title";
  h.textContent = title;
  t.appendChild(h);
  for (const r of rows) {
    const row = document.createElement("div");
    row.className = "tt-row";
    const key = document.createElement("i");
    key.className = "line";
    key.style.background = r.color;
    const b = document.createElement("b");
    b.textContent = r.value;
    const s = document.createElement("span");
    s.textContent = r.label;
    row.append(key, b, s);
    t.appendChild(row);
  }
  t.hidden = false;
  const pad = 14;
  const w = t.offsetWidth, hgt = t.offsetHeight;
  let x = clientX + pad, y = clientY - hgt - pad;
  if (x + w > window.innerWidth - 8) x = clientX - w - pad;
  if (y < 8) y = clientY + pad;
  t.style.left = `${Math.max(8, x)}px`;
  t.style.top = `${y}px`;
}

export function hideTooltip() {
  const t = tip();
  if (t) t.hidden = true;
}

// keep charts responsive: re-render when the container width changes
const observers = new WeakMap();
function observe(container, render) {
  let last = container.clientWidth;
  const prev = observers.get(container);
  if (prev) prev.disconnect();
  const ro = new ResizeObserver(() => {
    if (Math.abs(container.clientWidth - last) > 2) {
      last = container.clientWidth;
      render();
    }
  });
  ro.observe(container);
  observers.set(container, ro);
}

// ---------- sparkline ----------
/**
 * points: [{t: ms, v: number}], opts: {from, to, color, height, zeroLine}
 */
export function sparkline(container, points, opts = {}) {
  const width = container.clientWidth;
  if (!width) return; // not laid out yet; the next update draws it
  const height = opts.height || 44;
  const svg = el("svg", { viewBox: `0 0 ${width} ${height}`, width, height, "aria-hidden": "true" });
  container.replaceChildren(svg);
  if (points.length < 2) return;
  const from = opts.from ?? points[0].t;
  const to = opts.to ?? points[points.length - 1].t;
  let min = Math.min(...points.map((p) => p.v));
  let max = Math.max(...points.map((p) => p.v));
  if (opts.zeroLine) { min = Math.min(min, 0); max = Math.max(max, 0); }
  if (max - min < 1e-9) { max += 1; min -= 1; }
  const pad = 4;
  const x = (t) => ((t - from) / (to - from || 1)) * (width - pad * 2) + pad;
  const y = (v) => height - pad - ((v - min) / (max - min)) * (height - pad * 2);
  if (opts.zeroLine && min < 0 && max > 0) {
    el("line", { x1: 0, x2: width, y1: y(0), y2: y(0), stroke: "var(--axis)", "stroke-width": 1 }, svg);
  }
  const d = points.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join("");
  const base = opts.zeroLine && min < 0 ? y(0) : height - pad;
  el("path", {
    d: `${d}L${x(points[points.length - 1].t).toFixed(1)},${base}L${x(points[0].t).toFixed(1)},${base}Z`,
    fill: opts.color || "var(--accent)", "fill-opacity": 0.1, stroke: "none",
  }, svg);
  el("path", { d, fill: "none", stroke: opts.color || "var(--accent)", "stroke-width": 2, "stroke-linejoin": "round", "stroke-linecap": "round" }, svg);
  const last = points[points.length - 1];
  el("circle", { cx: x(last.t), cy: y(last.v), r: 4, fill: opts.color || "var(--accent)", stroke: "var(--surface)", "stroke-width": 2 }, svg);
}

// ---------- time axis ----------
const DAY = 86400000;
function timeTicks(from, to, width) {
  const span = to - from;
  const maxTicks = Math.max(2, Math.floor(width / 60));
  const steps = [15 * 60e3, 30 * 60e3, 3600e3, 2 * 3600e3, 3 * 3600e3, 4 * 3600e3, 6 * 3600e3, 12 * 3600e3, DAY, 2 * DAY, 3 * DAY, 7 * DAY, 14 * DAY];
  const step = steps.find((s) => span / s <= maxTicks) || 30 * DAY;
  const ticks = [];
  const start = new Date(from);
  if (step >= DAY) {
    start.setHours(0, 0, 0, 0);
  } else {
    start.setMinutes(0, 0, 0);
  }
  for (let t = start.getTime(); t <= to; t += step) {
    // re-align to local midnight across DST changes
    const d = new Date(t);
    if (step >= DAY && d.getHours() !== 0) { d.setHours(d.getHours() < 12 ? 0 : 24, 0, 0, 0); t = d.getTime(); }
    if (t >= from) ticks.push(t);
  }
  const fmt = step >= DAY
    ? new Intl.DateTimeFormat("nl-NL", { weekday: span <= 8 * DAY ? "short" : undefined, day: "numeric", month: span > 8 * DAY ? "short" : undefined })
    : new Intl.DateTimeFormat("nl-NL", { hour: "2-digit", minute: "2-digit" });
  return ticks.map((t) => ({ t, label: fmt.format(t) }));
}

// ---------- line chart ----------
/**
 * opts: {series: [{name, color, points: [{t, v}], dash}], from, to, format(v) -> string,
 *        axisFormat(v), title(t) -> string, height, area: bool,
 *        spans: [{from, to, color}] (shaded time ranges behind the lines)}
 * A series with dash (an SVG dash array, e.g. "4 4") is drawn dashed,
 * without area and never broken at gaps (a target or a limit).
 */
export function lineChart(container, opts) {
  const render = () => drawLine(container, opts);
  render();
  observe(container, render);
}

function drawLine(container, opts) {
  const width = Math.max(container.clientWidth, 200);
  const height = opts.height || 220;
  const m = { top: 10, right: 12, bottom: 26, left: 48 };
  const svg = el("svg", { viewBox: `0 0 ${width} ${height}`, role: "img", "aria-label": opts.label || "grafiek" });
  container.replaceChildren(svg);
  const all = opts.series.flatMap((s) => s.points);
  if (all.length === 0) {
    container.replaceChildren(Object.assign(document.createElement("div"), { className: "chart-empty", textContent: "Nog geen gegevens in deze periode." }));
    return;
  }
  // zero: false scales to the data only (voltage sits around 230 V)
  const floor = opts.zero === false ? [] : [0];
  let min = Math.min(...floor, ...all.map((p) => p.v));
  let max = Math.max(...floor, ...all.map((p) => p.v));
  const ticks = niceTicks(min, max, 4);
  min = ticks[0]; max = ticks[ticks.length - 1];
  const pw = width - m.left - m.right, ph = height - m.top - m.bottom;
  const x = (t) => m.left + ((t - opts.from) / (opts.to - opts.from)) * pw;
  const y = (v) => m.top + ph - ((v - min) / (max - min || 1)) * ph;

  for (const v of ticks) {
    el("line", { x1: m.left, x2: width - m.right, y1: y(v), y2: y(v), class: v === 0 ? "baseline" : "gridline" }, svg);
    text(svg, m.left - 8, y(v), (opts.axisFormat || opts.format)(v), "end", "middle");
  }
  for (const tk of timeTicks(opts.from, opts.to, pw)) {
    text(svg, x(tk.t), height - 6, tk.label);
  }
  for (const sp of opts.spans || []) {
    const a = x(Math.max(sp.from, opts.from)), b = x(Math.min(sp.to, opts.to));
    if (b > a) el("rect", { x: a, y: m.top, width: b - a, height: ph, fill: sp.color, "fill-opacity": 0.12 }, svg);
  }

  const gapLimit = opts.gapMs || Infinity;
  for (const s of opts.series) {
    if (!s.points.length) continue;
    let d = "";
    let segStart = null, prev = null;
    let area = "";
    const closeArea = () => {
      if (segStart !== null && prev !== null) area += `L${x(prev.t).toFixed(1)},${y(0).toFixed(1)}L${x(segStart.t).toFixed(1)},${y(0).toFixed(1)}Z`;
    };
    for (const p of s.points) {
      const gap = prev && !s.dash && p.t - prev.t > gapLimit;
      if (!prev || gap) {
        if (gap) closeArea();
        d += `M${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`;
        area += `M${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`;
        segStart = p;
      } else {
        d += `L${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`;
        area += `L${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`;
      }
      prev = p;
    }
    closeArea();
    if (opts.area !== false && !s.dash) el("path", { d: area, fill: s.color, "fill-opacity": 0.1 }, svg);
    el("path", { d, fill: "none", stroke: s.color, "stroke-width": s.dash ? 1.5 : 2, "stroke-linejoin": "round", "stroke-linecap": "round", ...(s.dash ? { "stroke-dasharray": s.dash } : {}) }, svg);
  }

  // crosshair + tooltip
  const cross = el("line", { y1: m.top, y2: m.top + ph, class: "crosshair", visibility: "hidden" }, svg);
  const dots = opts.series.map((s) => el("circle", { r: 4, fill: s.color, stroke: "var(--surface)", "stroke-width": 2, visibility: "hidden" }, svg));
  const hit = el("rect", { x: m.left, y: m.top, width: pw, height: ph, fill: "transparent", tabindex: 0 }, svg);
  const nearest = (pts, t) => {
    let lo = 0, hi = pts.length - 1;
    while (lo < hi) { const mid = (lo + hi) >> 1; if (pts[mid].t < t) lo = mid + 1; else hi = mid; }
    if (lo > 0 && Math.abs(pts[lo - 1].t - t) < Math.abs(pts[lo].t - t)) lo--;
    return pts[lo];
  };
  const ref = opts.series.reduce((a, s) => (s.points.length > a.points.length ? s : a), opts.series[0]);
  const move = (clientX, clientY) => {
    const box = svg.getBoundingClientRect();
    const px = ((clientX - box.left) / box.width) * width;
    const t = opts.from + ((px - m.left) / pw) * (opts.to - opts.from);
    const p = nearest(ref.points, t);
    if (!p) return;
    cross.setAttribute("x1", x(p.t)); cross.setAttribute("x2", x(p.t));
    cross.setAttribute("visibility", "visible");
    const rows = [];
    opts.series.forEach((s, i) => {
      const q = s.points.length ? nearest(s.points, p.t) : null;
      if (q && Math.abs(q.t - p.t) <= (opts.stepMs || 60e3)) {
        dots[i].setAttribute("cx", x(q.t)); dots[i].setAttribute("cy", y(q.v)); dots[i].setAttribute("visibility", "visible");
        rows.push({ color: s.color, value: opts.format(q.v, q), label: s.name });
      } else {
        dots[i].setAttribute("visibility", "hidden");
      }
    });
    showTooltip(clientX, clientY, opts.title(p.t), rows);
  };
  const leave = () => {
    cross.setAttribute("visibility", "hidden");
    dots.forEach((d) => d.setAttribute("visibility", "hidden"));
    hideTooltip();
  };
  hit.addEventListener("pointermove", (e) => move(e.clientX, e.clientY));
  hit.addEventListener("pointerdown", (e) => move(e.clientX, e.clientY));
  hit.addEventListener("pointerleave", leave);
  hit.addEventListener("blur", leave);
  hit.addEventListener("focus", () => {
    const last = ref.points[ref.points.length - 1];
    const box = svg.getBoundingClientRect();
    move(box.left + (x(last.t) / width) * box.width, box.top + 20);
  });
}

// ---------- column chart ----------
/**
 * opts: {buckets: [{start, values: {key: number|null}}], series: [{key, name, color, sign: 1|-1}],
 *        format(v), axisFormat(v), title(bucket), tickLabel(bucket, i, n) -> string|null, height}
 * Series with sign -1 are drawn below the baseline (e.g. teruglevering).
 * markIncomplete dims the bars of buckets with complete: false and puts a
 * ring on the baseline, so an incomplete bucket differs from a real zero.
 * A series with total: true is drawn in its own lane to the right of the
 * others, with its signed value (a negative total goes below the baseline).
 * Optional markerIndex draws a line at that bucket (with markerLabel above
 * it) and, with markerColor, draws that bucket's bars in that colour;
 * dimBeforeMarker fades the bars before it (e.g. quarter hours that passed).
 */
export function columnChart(container, opts) {
  const render = () => drawColumns(container, opts);
  render();
  observe(container, render);
}

function drawColumns(container, opts) {
  const width = Math.max(container.clientWidth, 200);
  const height = opts.height || 220;
  const m = { top: 10, right: 12, bottom: 26, left: 48 };
  const n = opts.buckets.length;
  if (n === 0) {
    container.replaceChildren(Object.assign(document.createElement("div"), { className: "chart-empty", textContent: "Nog geen gegevens in deze periode." }));
    return;
  }
  const svg = el("svg", { viewBox: `0 0 ${width} ${height}`, role: "img", "aria-label": opts.label || "grafiek" });
  container.replaceChildren(svg);
  let max = 0, min = 0;
  for (const b of opts.buckets) {
    for (const s of opts.series) {
      const v = b.values[s.key];
      if (v == null) continue;
      if (s.sign < 0 && !s.total) min = Math.min(min, -v); else { max = Math.max(max, v); min = Math.min(min, v); }
    }
  }
  if (max === 0 && min === 0) max = 1;
  const ticks = niceTicks(min, max, 4);
  min = ticks[0]; max = ticks[ticks.length - 1];
  const pw = width - m.left - m.right, ph = height - m.top - m.bottom;
  const y = (v) => m.top + ph - ((v - min) / (max - min || 1)) * ph;
  for (const v of ticks) {
    el("line", { x1: m.left, x2: width - m.right, y1: y(v), y2: y(v), class: v === 0 ? "baseline" : "gridline" }, svg);
    text(svg, m.left - 8, y(v), (opts.axisFormat || opts.format)(opts.signedAxis ? v : Math.abs(v)), "end", "middle");
  }
  const band = pw / n;
  // optional marker, e.g. the current quarter hour
  if (opts.markerIndex != null && opts.markerIndex >= 0 && opts.markerIndex < n) {
    const mx = m.left + band * opts.markerIndex + band / 2;
    el("line", { x1: mx, x2: mx, y1: m.top, y2: m.top + ph, class: "crosshair" }, svg);
    if (opts.markerLabel) text(svg, mx, m.top - 1, opts.markerLabel);
  }
  const up = opts.series.filter((s) => s.sign >= 0 && !s.total);
  const down = opts.series.filter((s) => s.sign < 0 && !s.total);
  const totals = opts.series.filter((s) => s.total);
  const base = Math.max(up.length, down.length, 1);
  const lanes = base + totals.length;
  const barW = Math.max(1, Math.min(24, (band - 2) / lanes - (band > 6 ? 2 : 0.5)));
  const r = Math.min(4, barW / 2);
  // column with rounded data end, square at the baseline
  const column = (cx, v, color) => {
    const y0 = y(0), y1 = y(v);
    const h = Math.abs(y0 - y1);
    if (h < 0.5) return null;
    const rr = Math.min(r, h);
    const x0 = cx - barW / 2, x1 = cx + barW / 2;
    const d = v >= 0
      ? `M${x0},${y0}V${y1 + rr}Q${x0},${y1} ${x0 + rr},${y1}H${x1 - rr}Q${x1},${y1} ${x1},${y1 + rr}V${y0}Z`
      : `M${x0},${y0}V${y1 - rr}Q${x0},${y1} ${x0 + rr},${y1}H${x1 - rr}Q${x1},${y1} ${x1},${y1 - rr}V${y0}Z`;
    return el("path", { d, fill: color, class: "bar" }, svg);
  };
  const labelEvery = opts.labelEvery || Math.max(1, Math.ceil(n / Math.max(2, Math.floor(pw / 46))));
  opts.buckets.forEach((b, i) => {
    const center = m.left + band * i + band / 2;
    const marks = [];
    // lane slots: the up and down series centred in the first base slots,
    // the totals after them
    const place = (list, sign, first) => {
      list.forEach((s, j) => {
        const v = b.values[s.key];
        if (v == null) return;
        const offset = (first + j - (lanes - 1) / 2) * (barW + 2);
        const current = i === opts.markerIndex && opts.markerColor;
        const mark = column(center + offset, sign * v, current ? opts.markerColor : s.color);
        if (mark) {
          if (s.total) mark.classList.add("total");
          if (opts.markIncomplete && b.complete === false) mark.classList.add("incomplete");
          if (current) mark.classList.add("current");
          else if (opts.dimBeforeMarker && i < opts.markerIndex) mark.classList.add("past");
          marks.push(mark);
        }
      });
    };
    place(up, 1, (base - up.length) / 2);
    place(down, -1, (base - down.length) / 2);
    place(totals, 1, base);
    if (opts.markIncomplete && b.complete === false) el("circle", { cx: center, cy: y(0) - 5, r: 3, class: "incomplete-mark" }, svg);
    const label = opts.tickLabel ? opts.tickLabel(b, i, n) : null;
    if (label && i % labelEvery === 0) text(svg, center, height - 6, label);
    const hit = el("rect", { x: m.left + band * i, y: m.top, width: band, height: ph, fill: "transparent", tabindex: 0 }, svg);
    const rows = opts.series.filter((s) => b.values[s.key] != null).map((s) => ({ color: s.color, value: opts.format(b.values[s.key]), label: s.name }));
    const show = (cx, cy) => { marks.forEach((mk) => mk.classList.add("hover")); showTooltip(cx, cy, opts.title(b), rows); };
    const hide = () => { marks.forEach((mk) => mk.classList.remove("hover")); hideTooltip(); };
    hit.addEventListener("pointermove", (e) => show(e.clientX, e.clientY));
    hit.addEventListener("pointerdown", (e) => show(e.clientX, e.clientY));
    hit.addEventListener("pointerleave", hide);
    hit.addEventListener("focus", () => { const bx = hit.getBoundingClientRect(); show(bx.left + bx.width / 2, bx.top + 20); });
    hit.addEventListener("blur", hide);
  });
}
