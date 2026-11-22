// Fills a running gateway (localhost:8080, hwsim devices) with demo history
// for the screenshots: imports generated HomeWizard CSV exports and sets
// device names, contracts and a scenario. Deterministic random.
//   node scripts/screenshots/demo-data.mjs [all|import|setup]
const API = 'http://localhost:8080/api';
const hdr = { 'Content-Type': 'application/json', Origin: 'http://localhost:8080' };
const call = async (method, path, body, raw) => {
  const r = await fetch(API + path, { method, headers: raw ? { Origin: hdr.Origin, 'Content-Type': 'text/csv' } : hdr, body: raw ? body : body && JSON.stringify(body) });
  const t = await r.text();
  if (!r.ok) console.log(method, path, r.status, t.slice(0, 300));
  return t ? JSON.parse(t) : null;
};
let seed = 42;
const rnd = () => { seed = (seed * 1103515245 + 12345) & 0x7fffffff; return seed / 0x7fffffff; };

const P1 = '5c2fafa1b2c3', SOLAR = '5c2fafa1c001', FRIDGE = '3c39e7a1b001', WASH = '3c39e7a1b002', WATER = '5c2fafa1d001';

const fmt = new Intl.DateTimeFormat('sv-SE', { timeZone: 'Europe/Amsterdam', year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false });
const parts = new Intl.DateTimeFormat('en-GB', { timeZone: 'Europe/Amsterdam', hour: 'numeric', minute: 'numeric', weekday: 'short', month: 'numeric', day: 'numeric', hour12: false });
const local = d => fmt.format(d).replace('T', ' ');

const start = new Date('2025-12-31T23:00:00Z');
const end = new Date(Math.floor((Date.now() - 20 * 60e3) / 900e3) * 900e3);

// monthly mean temperature NL and solar peak kW (4.2 kWp)
const temp = [3.5, 4, 6.5, 9.5, 13, 16, 18, 18, 15, 11, 7, 4.5];
const solarPeak = [0.9, 1.5, 2.4, 3.2, 3.6, 3.8, 3.7, 3.3, 2.7, 1.9, 1.1, 0.7];
const sunrise = [8.6, 7.9, 7.0, 6.9, 6.0, 5.4, 5.6, 6.4, 7.3, 8.1, 7.9, 8.6];
const sunset = [16.8, 17.7, 18.6, 20.4, 21.2, 21.9, 21.8, 21.0, 19.8, 18.6, 17.0, 16.5];

let it1 = 4210.5, it2 = 5120.2, et1 = 980.4, et2 = 2310.8, gas = 3120.4, water = 410000, fridge = 812.3, wash = 340.1, solarE = 6120.7;
const elec = ['time,Import T1 kWh,Import T2 kWh,Export T1 kWh,Export T2 kWh,L1 max W,L2 max W,L3 max W'];
const gasCsv = ['time,Total gas used'], waterCsv = ['time,water usage dl'];
const fr = ['time,Import kWh,Export kWh'], wa = ['time,Import kWh,Export kWh'], so = ['time,Import kWh,Export kWh'];
let cloud = 0.5, dayKey = '', washQ = -1, dishQ = -1, tempDay = 0;
for (let t = start.getTime(); t <= end.getTime(); t += 900e3) {
  const d = new Date(t);
  const p = Object.fromEntries(parts.formatToParts(d).map(x => [x.type, x.value]));
  const h = (+p.hour % 24) + +p.minute / 60, m = +p.month - 1, wd = p.weekday;
  const weekend = wd === 'Sat' || wd === 'Sun';
  const key = p.month + '-' + p.day;
  if (key !== dayKey) {
    dayKey = key; cloud = Math.min(1, Math.max(0.05, 0.3 + rnd() * 0.8 - (m >= 3 && m <= 8 ? 0.15 : 0)));
    tempDay = temp[m] + (rnd() - 0.5) * 6;
    washQ = rnd() < (weekend ? 0.8 : 0.35) ? Math.floor(36 + rnd() * 40) : -1;
    dishQ = rnd() < 0.8 ? Math.floor(76 + rnd() * 12) : -1;
  }
  const q = Math.round(h * 4);
  elec.push(`${local(d)},${it1.toFixed(3)},${it2.toFixed(3)},${et1.toFixed(3)},${et2.toFixed(3)},,,`);
  gasCsv.push(`${local(d)},${gas.toFixed(3)}`);
  waterCsv.push(`${local(d)},${Math.round(water)}`);
  fr.push(`${local(d)},${fridge.toFixed(3)},0.000`);
  wa.push(`${local(d)},${wash.toFixed(3)},0.000`);
  so.push(`${local(d)},0.000,${solarE.toFixed(3)}`);
  // consumption kW
  let load = 0.18 + rnd() * 0.06;
  if (h >= 7 && h < 8.5) load += 0.4 + rnd() * 0.5;
  if (h >= 17.5 && h < 19) load += 0.8 + rnd() * 1.2; // cooking
  if (h >= 19 && h < 23) load += 0.25 + rnd() * 0.15;
  if (weekend && h >= 10 && h < 17) load += 0.2 * rnd();
  const fridgeKw = (rnd() < 0.45 ? 0.09 : 0.004);
  let washKw = 0;
  if (washQ >= 0 && q >= washQ && q < washQ + 6) washKw = q < washQ + 2 ? 2.0 : 0.25;
  if (dishQ >= 0 && q >= dishQ && q < dishQ + 5) load += q < dishQ + 2 ? 1.9 : 0.15;
  load += fridgeKw + washKw;
  let sol = 0;
  if (h > sunrise[m] && h < sunset[m]) {
    const x = (h - sunrise[m]) / (sunset[m] - sunrise[m]);
    sol = solarPeak[m] * Math.pow(Math.sin(Math.PI * x), 1.5) * (1 - 0.75 * cloud) * (0.85 + rnd() * 0.3);
  }
  const net = (load - sol) / 4;
  const low = weekend || h < 7 || h >= 23;
  if (net > 0) { if (low) it1 += net; else it2 += net; } else { if (low) et1 -= net; else et2 -= net; }
  fridge += fridgeKw / 4; wash += washKw / 4; solarE += sol / 4;
  // gas m3: heating by degree, thermostat 7-22, hot water
  const hd = Math.max(0, 17 - tempDay);
  const heatOn = h >= 6.5 && h < 22.5;
  let g = hd * (heatOn ? 0.032 : 0.012) * (0.8 + rnd() * 0.4) / 4 * 4;
  if ((h >= 7 && h < 7.5) || (h >= 21 && h < 21.25)) g += 0.12 + rnd() * 0.1;
  g += 0.004;
  gas += g / 4;
  // water dl
  let w = rnd() < 0.15 ? 15 + rnd() * 40 : 0;
  if ((h >= 7 && h < 7.5) || (h >= 21 && h < 21.25)) w += 350 + rnd() * 250;
  if (washKw > 1) w += 250;
  if (dishQ >= 0 && q === dishQ) w += 120;
  if (h >= 18 && h < 18.5) w += 60 + rnd() * 60;
  water += w;
}

const imp = async (csv, dev, label) => {
  const r = await call('POST', `/import?device=${dev}`, csv.join('\n') + '\n', true);
  console.log(label, r && JSON.stringify(r).slice(0, 200));
};
const mode = process.argv[2] || 'all';
if (mode === 'all' || mode === 'import') {
  await imp(elec, P1, 'elec');
  await imp(gasCsv, P1, 'gas');
  await imp(waterCsv, WATER, 'water');
  await imp(fr, FRIDGE, 'fridge');
  await imp(wa, WASH, 'wash');
  await imp(so, SOLAR, 'solar');
}
if (mode === 'all' || mode === 'setup') {
  for (const [id, name] of [[P1, 'Slimme meter'], [SOLAR, 'Zonnepanelen'], [FRIDGE, 'Koelkast'], [WASH, 'Wasmachine'], [WATER, 'Watermeter']])
    await call('PATCH', `/devices/${id}`, { name, show_counters: id === P1 });
  const cs = await call('GET', '/contracts');
  for (const c of cs.contracts) await call('DELETE', `/contracts/${c.id}`);
  await call('POST', '/contracts', {
    name: 'Dynamisch (stroom en gas)', start: '2026-01-01', type: 'dynamic', vat_pct: 21,
    electricity: { markup_ct: 2.0, tax_ct: 11.08, export_ct: -1.5 },
    gas: { markup_ct: 7.5, tax_ct: 72.37 },
    water: {},
    monthly: [{ name: 'Vaste leveringskosten', eur: 6.99 }, { name: 'Netbeheer stroom', eur: 39.48 }, { name: 'Netbeheer gas', eur: 20.49 }, { name: 'Vermindering energiebelasting', eur: -52.62 }],
    advance: { amounts: [{ from: '2026-01-20', eur: 145 }] },
    estimate: { import_kwh: 2800, export_kwh: 2400, gas_m3: 1050 },
  });
  const wc = await call('GET', '/water/contracts');
  for (const c of wc.contracts) await call('DELETE', `/water/contracts/${c.id}`);
  await call('POST', '/water/contracts', {
    name: 'Waterbedrijf', start: '2026-01-01', type: 'water', vat_pct: 9, electricity: {}, gas: {},
    water: { rate_ct: 104.5, tax_ct: 45.98 }, monthly: [{ name: 'Vastrecht', eur: 5.42 }],
    advance: { amounts: [{ from: '2026-01-28', eur: 21 }] }, estimate: { water_m3: 110 },
  });
  await call('POST', '/scenarios', {
    name: 'Vast contract 3 jaar', type: 'fixed', vat_pct: 21,
    electricity: { normal_ct: 15.9, low_ct: 14.2, tax_ct: 11.08, export_ct: 4 },
    gas: { rate_ct: 57.5, tax_ct: 72.37 }, water: {},
    monthly: [{ name: 'Vaste leveringskosten', eur: 7.5 }, { name: 'Netbeheer stroom', eur: 39.48 }, { name: 'Netbeheer gas', eur: 20.49 }, { name: 'Vermindering energiebelasting', eur: -52.62 }],
  });
}
console.log('done');
