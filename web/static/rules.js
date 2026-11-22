// Rules in two views. Meldingen: alerts that only send a push message, on
// any value (HomeWizard devices, today's usage, plugin devices, prices,
// time). Automatisering: rules that switch sockets and set plugin devices
// (air conditioner, tado, Tuya), with actions for when the conditions stop
// holding. Both share the conditions: compared with a number or with another
// value times a factor (e.g. 0.5 m³ × the gas price against 0.8 kWh × the
// electricity price). Plugin devices are "plugin:<id>". An automation can
// also be a manual action: no conditions, started by hand (actions.js).
import { state, subscribe } from "./app.js";
import { h, api, ago, dates, fmt } from "./util.js";
import { lineChart } from "./charts.js";
import { actionControl } from "./actions.js";

const PRICE = "price";
const TIME = "time"; // time window: days and from/to, in the gateway's time zone
const PLUGIN = "plugin:";
const DAYS = ["ma", "di", "wo", "do", "vr", "za", "zo"]; // ISO 1..7

const isPlugin = (id) => typeof id === "string" && id.startsWith(PLUGIN);
const pluginDevice = (id) => (isPlugin(id) ? state.plugins.get(id.slice(PLUGIN.length)) : null);
const pluginMetric = (id, key) => pluginDevice(id)?.reading?.metrics?.find((m) => m.key === key);
/** Plugin devices whose controls a rule may set (control allowed for the plugin). */
const controllable = () => [...state.plugins.values()].filter((d) => d.controls?.some((c) => !c.hidden));

// metrics offered per device kind; other values of the current reading are
// listed after these
const METRICS = {
  socket: ["power_w", "today_energy_import_kwh", "power_on", "online"],
  kwh: ["power_w", "today_energy_import_kwh", "today_energy_export_kwh", "voltage_v", "current_a", "online"],
  p1: ["power_w", "today_energy_import_kwh", "today_energy_export_kwh", "today_net_kwh", "today_gas_m3", "online"],
  water: ["water_lpm", "today_water_m3", "online"],
  battery: ["state_of_charge_pct", "power_w", "today_energy_import_kwh", "today_energy_export_kwh", "online"],
  other: ["online"],
};
const PRICE_METRICS = ["price_all_in", "price", "gas_price_all_in", "gas_price", "price_avg_today", "price_cheaper_hours"];
const METRIC_LABEL = {
  power_w: ["Vermogen", "W"],
  power_on: ["Aan", ""],
  online: ["Online", ""],
  voltage_v: ["Spanning", "V"],
  current_a: ["Stroom", "A"],
  water_lpm: ["Waterverbruik", "L/min"],
  state_of_charge_pct: ["Laadniveau", "%"],
  today_energy_import_kwh: ["Afname vandaag", "kWh"],
  today_energy_export_kwh: ["Teruglevering vandaag", "kWh"],
  today_net_kwh: ["Netto afname vandaag", "kWh"],
  today_gas_m3: ["Gas vandaag", "m³"],
  today_water_m3: ["Water vandaag", "m³"],
  gas_m3: ["Gasmeterstand", "m³"],
  water_m3: ["Watermeterstand", "m³"],
  energy_import_kwh: ["Meterstand afname", "kWh"],
  energy_export_kwh: ["Meterstand teruglevering", "kWh"],
  price: ["Stroomprijs kaal", "€/kWh"],
  price_all_in: ["Stroomprijs all-in", "€/kWh"],
  gas_price: ["Gasprijs kaal", "€/m³"],
  gas_price_all_in: ["Gasprijs all-in", "€/m³"],
  price_avg_today: ["Gem. stroomprijs vandaag (kaal)", "€/kWh"],
  price_cheaper_hours: ["Uren vandaag goedkoper", "uur"],
};
const BOOLEAN = new Set(["power_on", "online"]);
const PRICE_UNIT = new Set(["price", "price_all_in", "gas_price", "gas_price_all_in", "price_avg_today"]);
const OPS = { "<": "lager dan", "<=": "hooguit", ">": "hoger dan", ">=": "minstens", "=": "gelijk aan", "!=": "niet gelijk aan" };
const TEXT_OPS = { "=": "is", "!=": "is niet" };

// ---------- the two views ----------

const MODES = {
  alert: {
    kind: "alert",
    title: "Meldingen",
    intro: "Een melding stuurt een pushbericht als al zijn voorwaarden (of één ervan) een tijd lang gelden, bijvoorbeeld als het verbruik vandaag hoger is dan je wilt, het binnen te koud wordt of de stroomprijs negatief is. Hij gaat één keer af en pas weer nadat de voorwaarden een keer niet golden. Apparaten bedienen doe je onder Automatisering.",
    empty: "Nog geen meldingen. Begin met een voorbeeld hieronder of maak een nieuwe melding.",
    newLabel: "Nieuwe melding",
  },
  automation: {
    kind: "automation",
    title: "Automatisering",
    intro: "Een automatisering schakelt sockets en bedient apparaten (airco, tado, Tuya) als zijn voorwaarden een tijd lang gelden, en kan iets anders doen zodra ze niet meer gelden. Een nieuwe of gewijzigde automatisering kijkt meteen naar de situatie van dat moment. Vergelijk ook twee waarden met elkaar, bijvoorbeeld wat een half uur stoken met gas kost tegenover de airco. Of start hem zelf: kies bij Starten ‘handmatig’ en zet hem aan en uit op een dashboardtegel.",
    empty: "Nog geen automatiseringen. Begin met een voorbeeld hieronder of maak een nieuwe automatisering.",
    newLabel: "Nieuwe automatisering",
  },
};

export const renderAlerts = (view) => renderRules(view, MODES.alert);
export const renderAutomations = (view) => renderRules(view, MODES.automation);

// ---------- templates ----------

const ALERT_TEMPLATES = [
  {
    label: "Droger of wasmachine klaar",
    kind: "socket",
    rule: (dev) => ({
      name: `${dev?.name || "Apparaat"} klaar`, for_s: 180,
      conditions: [{ device: dev?.id || "", metric: "power_w", op: "<", value: 5 }],
    }),
    hint: "Meldt één keer als het vermogen 3 minuten onder 5 W blijft, nadat het apparaat heeft gedraaid.",
  },
  {
    label: "Veel stroom vandaag",
    kind: "p1",
    rule: (dev) => ({
      name: "Veel stroom verbruikt vandaag", for_s: 0,
      conditions: [{ device: dev?.id || "", metric: "today_energy_import_kwh", op: ">", value: 15 }],
    }),
    hint: "Meldt als de afname van vandaag boven 15 kWh komt.",
  },
  {
    label: "Veel gas vandaag",
    kind: "p1",
    available: () => [...state.devices.values()].some((d) => d.kind === "p1" && d.reading?.values?.gas_m3 != null),
    rule: (dev) => ({
      name: "Veel gas verbruikt vandaag", for_s: 0,
      conditions: [{ device: dev?.id || "", metric: "today_gas_m3", op: ">", value: 5 }],
    }),
    hint: "Meldt als het gasverbruik van vandaag boven 5 m³ komt.",
  },
  {
    label: "Water loopt lang",
    kind: "water",
    rule: (dev) => ({
      name: "Water loopt al lang", for_s: 1800,
      conditions: [{ device: dev?.id || "", metric: "water_lpm", op: ">", value: 0 }],
    }),
    hint: "Meldt als er een half uur onafgebroken water loopt, bijvoorbeeld bij een lek.",
  },
  {
    label: "Binnen te koud",
    available: () => indoorMetric(),
    rule: () => {
      const [d, m] = indoorMetric();
      return { name: `${d.name} te koud`, for_s: 900, conditions: [{ device: PLUGIN + d.id, metric: m.key, op: "<", value: 17 }] };
    },
    hint: "Meldt als de temperatuur binnen een kwartier onder 17 °C blijft.",
  },
  {
    label: "Vorst buiten",
    available: () => outdoorMetric(),
    rule: () => {
      const [d, m] = outdoorMetric();
      return { name: "Het vriest buiten", for_s: 1800, conditions: [{ device: PLUGIN + d.id, metric: m.key, op: "<", value: 0 }] };
    },
    hint: "Meldt als de buitentemperatuur een half uur onder 0 °C blijft.",
  },
  {
    label: "Negatieve stroomprijs",
    rule: () => ({ name: "Stroom is negatief geprijsd", for_s: 0, conditions: [{ device: PRICE, metric: "price", op: "<", value: 0 }] }),
    hint: "Meldt als de kale marktprijs onder nul gaat: een goed moment voor de wasmachine.",
  },
  {
    label: "Apparaat offline",
    kind: null,
    rule: (dev) => ({
      name: `${dev?.name || "Apparaat"} offline`, for_s: 600,
      conditions: [{ device: dev?.id || "", metric: "online", op: "=", value: 0 }],
    }),
    hint: "Meldt als een apparaat 10 minuten niet bereikbaar is.",
  },
  {
    label: "CV en airco tegelijk",
    available: () => pluginOf("tado") && pluginOf("mhi"),
    rule: () => ({
      name: "CV en airco verwarmen tegelijk", for_s: 900,
      conditions: [
        { device: PLUGIN + pluginOf("tado").id, metric: "heating", op: ">", value: 0 },
        { device: PLUGIN + pluginOf("mhi").id, metric: "power", op: "=", value: 1 },
        { device: PLUGIN + pluginOf("mhi").id, metric: "mode", op: "=", value: 0, text: "Verwarmen" },
      ],
    }),
    hint: "Meldt als de CV (tado) en de airco een kwartier tegelijk verwarmen.",
  },
  {
    label: "CV en elektrische radiator tegelijk",
    available: () => pluginOf("tado") && tuyaHeater(),
    rule: () => ({
      name: "CV en radiator verwarmen tegelijk", for_s: 900,
      conditions: [
        { device: PLUGIN + pluginOf("tado").id, metric: "heating", op: ">", value: 0 },
        { device: PLUGIN + tuyaHeater().id, metric: tuyaPower(tuyaHeater()).key, op: "=", value: 1 },
      ],
    }),
    hint: "Meldt als de CV (tado) en de elektrische radiator (Tuya) een kwartier tegelijk aan staan.",
  },
];

const AUTOMATION_TEMPLATES = [
  {
    label: "Verwarmen: eerst CV, dan airco",
    available: () => controlOf("tado", "target") && regulable().some((d) => d.plugin_type === "mhi"),
    rule: () => {
      const tado = PLUGIN + controlOf("tado", "target").id;
      const ac = PLUGIN + regulable().find((d) => d.plugin_type === "mhi").id;
      return {
        name: "Verwarmen naar 19", for_s: 0,
        conditions: [
          { device: TIME, metric: "window", op: "=", value: 1, days: [1, 2, 3, 4, 5, 6, 7], from: "06:30", to: "22:00" },
          { device: tado, metric: "temp", op: "<", value: 18.5 },
          { device: ac, metric: "power", op: "=", value: 0 },
        ],
        actions: [
          { type: "plugin_set", device: tado, key: "target", value: 19 },
          { type: "wait", until: { device: tado, metric: "temp", op: ">=", value: 19 }, max_s: 1800 },
          { type: "plugin_set", device: tado, key: "mode", value: "schedule" },
          { type: "plugin_set", device: ac, key: "mode", value: "HEAT" },
          { type: "plugin_set", device: ac, key: "power", value: true },
          { type: "plugin_set", device: ac, key: "preset", value: 21 },
          { type: "regulate", device: ac, key: "preset", sensor: { device: tado, metric: "temp" },
            target: 19, band: 0.2, step: 0.5, every_s: 900, offset: 2, below: 1, above: 4, stop_window: true, off_at_end: true },
        ],
      };
    },
    hint: "Is het onder 18,5 °C en staat de airco uit, dan warmt de CV voor tot 19 °C (hooguit een half uur). Daarna gaat tado terug naar schema en houdt de airco tado op 19 ± 0,2 °C: elk kwartier een halve graad omhoog of omlaag, tussen 18 en 23 °C. Om 22:00 gaat de airco uit. De airco meet aan het plafond, daarom staat hij op doel + 2 (21 °C).",
  },
  {
    label: "Airco: snel opwarmen, dan op temperatuur houden",
    available: () => regulable().some((d) => d.plugin_type === "mhi") && roomTemp(),
    rule: () => {
      const ac = PLUGIN + regulable().find((d) => d.plugin_type === "mhi").id;
      const room = roomTemp();
      return {
        name: "Airco: snel naar 19", for_s: 0,
        conditions: [
          { device: TIME, metric: "window", op: "=", value: 1, days: [1, 2, 3, 4, 5, 6, 7], from: "07:00", to: "22:00" },
          { device: PLUGIN + room[0].id, metric: room[1].key, op: "<", value: 18.5 },
        ],
        actions: [
          { type: "plugin_set", device: ac, key: "mode", value: "HEAT" },
          { type: "plugin_set", device: ac, key: "power", value: true },
          { type: "regulate", device: ac, key: "preset", sensor: { device: PLUGIN + room[0].id, metric: room[1].key },
            target: 19, band: 0.2, step: 0.5, every_s: 900, offset: 1, below: 2, above: 2, boost: true, stop_window: true, off_at_end: true },
        ],
      };
    },
    hint: "Is het tussen 7:00 en 22:00 onder 18,5 °C, dan gaat de airco aan op 21 °C (doel + 2) tot de kamer 18,8 °C is (doel − marge), en daarna naar 20 °C (doel + 1). Vanaf dan houdt hij de kamer op 19 ± 0,2 °C met stappen van een halve graad, tussen 17 en 21 °C. Om 22:00 gaat de airco uit.",
  },
  {
    label: "Goedkoopst verwarmen: airco of CV",
    available: () => controlOf("mhi", "power"),
    rule: () => {
      const ac = PLUGIN + controlOf("mhi", "power").id;
      const tado = controlOf("tado", "mode");
      return {
        name: "Airco verwarmt als dat goedkoper is dan gas", for_s: 600,
        conditions: [
          // 30 minutes of heating: 0.5 m³ gas against 0.8 kWh for the air conditioner
          { device: PRICE, metric: "gas_price_all_in", scale: 0.5, op: ">", value: 0, ref: { device: PRICE, metric: "price_all_in", scale: 0.8 } },
        ],
        actions: [
          { type: "plugin_set", device: ac, key: "mode", value: "HEAT" },
          { type: "plugin_set", device: ac, key: "power", value: true },
          ...(tado ? [{ type: "plugin_set", device: PLUGIN + tado.id, key: "mode", value: "off" }] : []),
        ],
        else_actions: [
          { type: "plugin_set", device: ac, key: "power", value: false },
          ...(tado ? [{ type: "plugin_set", device: PLUGIN + tado.id, key: "mode", value: "schedule" }] : []),
        ],
      };
    },
    hint: "Vergelijkt wat een half uur stoken kost: 0,5 m³ gas tegen 0,8 kWh voor de airco (pas de getallen aan op je eigen woning). Is de airco goedkoper, dan verwarmt die en gaat de CV uit; anders andersom.",
  },
  {
    label: "Socket aan bij goedkope stroom",
    available: () => [...state.devices.values()].some((d) => d.can_switch),
    rule: () => {
      const dev = [...state.devices.values()].find((d) => d.can_switch);
      return {
        name: `${dev.name} aan in de goedkoopste uren`, for_s: 0,
        conditions: [{ device: PRICE, metric: "price_cheaper_hours", op: "<", value: 4 }],
        actions: [{ type: "switch", device: dev.id, on: true }],
        else_actions: [{ type: "switch", device: dev.id, on: false }],
      };
    },
    hint: "Zet een socket (boiler, laadpaal) aan in de 4 goedkoopste uren van de dag en daarna weer uit.",
  },
  {
    label: "Teruglevering opmaken",
    available: () => p1() && [...state.devices.values()].some((d) => d.can_switch),
    rule: () => {
      const dev = [...state.devices.values()].find((d) => d.can_switch);
      return {
        name: `${dev.name} aan bij overschot zonnestroom`, for_s: 300,
        conditions: [{ device: p1().id, metric: "power_w", op: "<", value: -1500 }],
        actions: [{ type: "switch", device: dev.id, on: true }],
        else_actions: [{ type: "switch", device: dev.id, on: false }],
      };
    },
    hint: "Zet een socket aan als je 5 minuten meer dan 1500 W teruglevert, en uit zodra dat niet meer zo is.",
  },
  {
    label: "Thuisbatterij laden bij goedkope stroom",
    available: () => controlOf("homewizard_battery", "mode"),
    rule: () => {
      const bat = PLUGIN + controlOf("homewizard_battery", "mode").id;
      return {
        name: "Thuisbatterij laden in de goedkoopste uren", for_s: 0,
        conditions: [{ device: PRICE, metric: "price_cheaper_hours", op: "<", value: 4 }],
        actions: [{ type: "plugin_set", device: bat, key: "mode", value: "charge_only" }],
        else_actions: [{ type: "plugin_set", device: bat, key: "mode", value: "zero" }],
      };
    },
    hint: "Laat de thuisbatterij in de 4 goedkoopste uren van de dag alleen laden (niet ontladen), en daarna weer nul op de meter houden. Werkt het best met een dynamisch contract.",
  },
  {
    label: "Airco uit als het raam open staat",
    available: () => controlOf("mhi", "power") && windowMetric(),
    rule: () => {
      const [d, m] = windowMetric();
      return {
        name: "Airco uit bij open raam", for_s: 120,
        conditions: [{ device: PLUGIN + d.id, metric: m.key, op: "=", value: 1 }],
        actions: [{ type: "plugin_set", device: PLUGIN + controlOf("mhi", "power").id, key: "power", value: false }],
      };
    },
    hint: "Zet de airco uit als tado 2 minuten een open raam ziet.",
  },
];

function pluginOf(type) {
  return [...state.plugins.values()].find((d) => d.plugin_type === type);
}
/** A plugin device of a type whose control key may be set by rules. */
function controlOf(type, key) {
  return controllable().find((d) => d.plugin_type === type && d.controls.some((c) => c.key === key && !c.hidden));
}
function p1() {
  return [...state.devices.values()].find((d) => d.kind === "p1");
}
function findMetric(test) {
  for (const d of state.plugins.values()) {
    const m = d.reading?.metrics?.find((x) => test(d, x));
    if (m) return [d, m];
  }
  return null;
}
/** A room temperature: tado "temp" or the air conditioner's "indoor". */
const indoorMetric = () => findMetric((d, m) => m.unit === "°C" && (m.key === "temp" || m.key === "indoor"));
const outdoorMetric = () => findMetric((d, m) => m.unit === "°C" && m.key === "outdoor");
const windowMetric = () => findMetric((d, m) => m.key === "window" && m.kind === "bool");
/** The on/off metric of a Tuya device (the "switch" datapoint, labelled "Aan"). */
function tuyaPower(d) {
  return d?.reading?.metrics?.find((m) => m.label === "Aan" && m.kind === "bool");
}
/** A Tuya device with an on/off datapoint, such as an electric radiator. */
function tuyaHeater() {
  return [...state.plugins.values()].find((d) => d.plugin_type === "tuya" && tuyaPower(d));
}
/** Plugin devices whose temperature a regulate action may step: an air conditioner or an
 * electric radiator, never a thermostat such as tado (it regulates the central heating itself). */
const regulable = () => controllable().filter((d) => d.can_regulate && d.controls.some((c) => c.kind === "setpoint" && !c.hidden));
/** The room temperature of tado, else any room temperature. */
const roomTemp = () => findMetric((d, m) => d.plugin_type === "tado" && m.key === "temp") || indoorMetric();

// ---------- describing ----------

let prices = {}; // current values of the price metrics, from /api/rules/prices

function metricLabel(metric) {
  return METRIC_LABEL[metric] || [metric.replaceAll("_", " "), ""];
}
function deviceName(id) {
  if (id === PRICE) return "Prijzen";
  if (id === TIME) return "Tijd";
  if (isPlugin(id)) return pluginDevice(id)?.name || "Onbekend plugin-apparaat";
  return state.devices.get(id)?.name || "Onbekend apparaat";
}
/** Label and unit of a metric of a device (plugin metrics carry their own). */
function metricOf(device, metric) {
  if (isPlugin(device) && metric !== "online") {
    const m = pluginMetric(device, metric);
    return m ? [m.label, m.unit || ""] : [metric, ""];
  }
  return metricLabel(metric);
}
/** "bool", "text" or "number" for a metric of a device. */
function metricKind(device, metric) {
  if (BOOLEAN.has(metric)) return "bool";
  if (isPlugin(device)) return pluginMetric(device, metric)?.kind || "number";
  return "number";
}
const num = (v) => (Number.isInteger(v) ? fmt.n0(v) : Math.abs(v) < 10 ? fmt.n2(v).replace(/,?0+$/, "") : fmt.n1(v));
function formatValue(device, metric, v) {
  if (BOOLEAN.has(metric) || metricKind(device, metric) === "bool") return v >= 0.5 ? "ja" : "nee";
  const [, unit] = metricOf(device, metric);
  const s = PRICE_UNIT.has(metric) ? fmt.n3(v) : num(v);
  return unit ? `${s} ${unit}` : s;
}
/** The current value of a device's metric, or null. */
function currentValue(device, metric) {
  if (device === PRICE) return prices[metric] ?? null;
  if (isPlugin(device)) {
    const pd = pluginDevice(device);
    if (metric === "online") return pd ? (pd.reading?.online ? 1 : 0) : null;
    const m = pluginMetric(device, metric);
    if (!m) return null;
    if (m.kind === "bool") return m.value ? 1 : 0;
    return typeof m.value === "number" ? m.value : null;
  }
  const d = state.devices.get(device);
  if (!d) return null;
  if (metric === "online") return d.status === "online" ? 1 : 0;
  if (metric === "power_on") return d.reading?.power_on == null ? null : d.reading.power_on ? 1 : 0;
  if (metric === "today_net_kwh") {
    const t = d.today || {};
    return t.energy_import_kwh == null ? null : t.energy_import_kwh - (t.energy_export_kwh || 0);
  }
  if (metric.startsWith("today_")) return d.today?.[metric.slice(6)] ?? null;
  return d.reading?.values?.[metric] ?? null;
}
function describeDays(days) {
  if (days.length === 7) return "elke dag";
  const parts = [];
  for (let i = 0; i < days.length;) {
    let j = i;
    while (j + 1 < days.length && days[j + 1] === days[j] + 1) j++;
    if (j - i >= 2) parts.push(`${DAYS[days[i] - 1]}-${DAYS[days[j] - 1]}`);
    else for (let k = i; k <= j; k++) parts.push(DAYS[days[k] - 1]);
    i = j + 1;
  }
  return parts.join(", ");
}
function who(device, metric) {
  const [label] = metricOf(device, metric);
  return device === PRICE ? label.replace(/ \(.*\)$/, "") : `${deviceName(device)}: ${label.toLowerCase()}`;
}
const times = (scale) => (scale && scale !== 1 ? `${num(scale)} × ` : "");
function describeCondition(c) {
  if (c.device === TIME) {
    const when = c.from === c.to ? "hele dag" : `${c.from}-${c.to}`;
    return `het ${c.value ? "" : "niet "}${describeDays(c.days || [])} ${when} is`;
  }
  const left = times(c.scale) + who(c.device, c.metric);
  if (c.text) return `${left} ${TEXT_OPS[c.op] || "is"} ${c.text}`;
  if (metricKind(c.device, c.metric) === "bool") return `${left} is ${(c.op === "!=") !== (c.value >= 0.5) ? "ja" : "nee"}`;
  if (c.ref) {
    const offset = c.value ? ` ${c.value > 0 ? "+" : "−"} ${num(Math.abs(c.value))}` : "";
    return `${left} ${OPS[c.op]} ${times(c.ref.scale)}${who(c.ref.device, c.ref.metric)}${offset}`;
  }
  return `${left} ${OPS[c.op]} ${formatValue(c.device, c.metric, c.value)}`;
}
function describeDuration(s) {
  if (!s) return "meteen";
  if (s < 60) return `${s} seconden`;
  if (s < 3600) return `${fmt.n0(s / 60)} minuten`;
  const hours = Math.floor(s / 3600), min = Math.round((s % 3600) / 60);
  return min ? `${hours} uur ${min} min` : `${hours} uur`;
}
/** A regulate action's setpoint after a boost (target + offset) and its range around the target. */
function rangeOf(a) {
  const r2 = (v) => Math.round(v * 100) / 100;
  const mid = r2((a.target ?? 0) + (a.offset ?? 0));
  const t = a.target ?? 0;
  return { mid, min: r2(t - (a.below ?? 0)), max: r2(t + (a.above ?? 0)) };
}

function describeAction(a) {
  if (a.type === "notify") return "Pushmelding sturen";
  if (a.type === "wait") {
    return a.until ? `wachten tot ${describeCondition(a.until)}, hooguit ${describeDuration(a.max_s)}` : `${describeDuration(a.max_s)} wachten`;
  }
  if (a.type === "regulate") {
    const c = pluginDevice(a.device)?.controls?.find((x) => x.key === a.key);
    const unit = c?.unit ? ` ${c.unit}` : "";
    const name = deviceName(a.device);
    const end = a.stop_window ? `, of als het tijdvenster eindigt${a.off_at_end ? " (dan uit)" : ""}` : "";
    return `${name}: ${(c?.label || a.key).toLowerCase()} regelen zodat ${who(a.sensor?.device, a.sensor?.metric)} ${num(a.target)} ± ${num(a.band)} blijft; `
      + (a.boost ? `eerst snel naar het doel (${num(a.max)}${unit} bij te koud, ${num(a.min)}${unit} bij te warm), dan ${num(rangeOf(a).mid)}${unit}; `
        : `begint op ${num(rangeOf(a).mid)}${unit}; `)
      + `elke ${describeDuration(a.every_s)} ${num(a.step)}${unit} omhoog of omlaag, tussen ${num(a.min)} en ${num(a.max)}${unit}; stopt als ${name} uit gaat${end}`
      + (a.notify_steps ? "; pushmelding bij elke stap" : "");
  }
  if (a.type === "plugin_set") {
    const c = pluginDevice(a.device)?.controls?.find((x) => x.key === a.key);
    const label = c?.label || a.key;
    let v = "";
    if (typeof a.value === "boolean") v = a.value ? " aan" : " uit";
    else if (typeof a.value === "number") v = ` ${fmt.n1(a.value)}${c?.unit ? " " + c.unit : ""}`;
    else if (typeof a.value === "string") v = " " + (c?.options?.find((o) => o.value === a.value)?.label || a.value);
    return `${deviceName(a.device)}: ${label}${v}`;
  }
  return `${deviceName(a.device)} ${a.on ? "aanzetten" : "uitzetten"}`;
}

// ---------- view ----------

function renderRules(view, mode) {
  const automation = mode.kind === "automation";
  const pushCard = h("section", { class: "card" });
  const list = h("div", { class: "contract-list" });
  const formSlot = h("div");
  const newBtn = h("button", { class: "btn primary", type: "button" }, mode.newLabel);
  const templateRow = h("div", { class: "row" });
  const listError = h("div", { class: "form-error", role: "alert", hidden: true });

  view.append(...[
    automation ? null : h("h2", { class: "section-title" }, "Meldingen op dit apparaat"),
    automation ? null : pushCard,
    h("h2", { class: "section-title" }, mode.title),
    h("p", { class: "secondary contract-help" }, mode.intro),
    automation ? h("p", { class: "secondary contract-help" }, "Een pushbericht bij een automatisering komt aan op de apps waar meldingen aan staan (zie ", h("a", { href: "#/meldingen" }, "Meldingen"), ").") : null,
    list, listError, formSlot,
    h("div", { class: "row form-actions" }, newBtn),
    h("h3", { class: "section-title" }, "Voorbeelden"),
    templateRow,
  ].filter(Boolean));

  let rules = [];
  let regulated = {}; // rule id -> when it last regulated (ISO)
  let editing = null; // id, or "new"

  const openForm = (rule) => {
    editing = rule.id || "new";
    formSlot.replaceChildren(ruleForm(rule, mode, async () => { closeForm(); await load(); }, closeForm));
    formSlot.scrollIntoView({ behavior: "smooth", block: "start" });
  };
  const closeForm = () => { editing = null; formSlot.replaceChildren(); };
  const blank = () => ({
    kind: mode.kind, name: "", enabled: true, match: "all", for_s: 60,
    conditions: [{ device: "", metric: "power_w", op: "<", value: 0 }],
    actions: automation ? [] : [{ type: "notify" }], else_actions: [],
  });
  newBtn.addEventListener("click", () => openForm(blank()));
  const renderTemplates = () => {
    const templates = automation ? AUTOMATION_TEMPLATES : ALERT_TEMPLATES;
    const buttons = [];
    for (const t of templates) {
      if (t.available && !t.available()) continue;
      buttons.push(h("button", { class: "btn", type: "button", title: t.hint, onclick: () => {
        const dev = [...state.devices.values()].find((d) => !t.kind || d.kind === t.kind);
        openForm({ ...blank(), ...t.rule(dev), hint: t.hint });
      } }, t.label));
    }
    templateRow.replaceChildren(...(buttons.length ? buttons
      : [h("p", { class: "secondary" }, automation
        ? "Voorbeelden verschijnen zodra er een socket is die geschakeld kan worden of een plugin-apparaat met bediening aan."
        : "Nog geen voorbeelden voor je apparaten.")]));
  };

  const renderList = () => {
    const mine = rules.filter((r) => r.kind === mode.kind);
    if (!mine.length) {
      list.replaceChildren(h("div", { class: "notice" }, mode.empty));
      return;
    }
    list.replaceChildren(...mine.map((r) => ruleCard(r, regulated[r.id], {
      edit: () => openForm(structuredClone(r)),
      copy: () => {
        const { id: _id, state: _s, ...rule } = structuredClone(r);
        // off until switched on, so it does not steer the same device as the
        // original; a manual action only starts when switched on, so it stays on
        openForm(r.manual
          ? { ...rule, name: `${r.name} Kopie`, enabled: true, hint: `Kopie van ‘${r.name}’.` }
          : { ...rule, name: `${r.name} Kopie`, enabled: false,
            hint: `Kopie van ‘${r.name}’. Na opslaan staat de kopie uit; schakel hem in als hij moet gaan sturen (kies je bij Starten ‘handmatig’, dan staat hij meteen aan).` });
      },
      tune: (target, band) => run(() => api("PATCH", `/api/rules/${r.id}/regulate`, { target, band })),
      switched: () => { load(); setTimeout(load, 3000); }, // the gateway acts within seconds
      failed: (msg) => { listError.textContent = msg; listError.hidden = false; },
      toggle: async () => {
        const { state: _s, ...rule } = r;
        await save({ ...rule, enabled: !r.enabled });
      },
      remove: async () => {
        if (!confirm(`'${r.name}' verwijderen?`)) return;
        await run(() => api("DELETE", `/api/rules/${r.id}`));
        if (editing === r.id) closeForm();
      },
    })));
  };
  const run = async (fn) => {
    listError.hidden = true;
    try { await fn(); await load(); } catch (e) { listError.textContent = e.message; listError.hidden = false; }
  };
  const save = (rule) => run(() => api("PUT", `/api/rules/${rule.id}`, rule));
  const load = async () => {
    try {
      const [res, p] = await Promise.all([api("GET", "/api/rules"), api("GET", "/api/rules/prices").catch(() => ({}))]);
      rules = res.rules;
      regulated = res.regulated || {};
      prices = p;
      renderList();
    } catch (e) {
      list.replaceChildren(h("div", { class: "notice" }, `Regels niet geladen: ${e.message}`));
    }
  };

  if (!automation) renderPush(pushCard);
  renderTemplates();
  load();
  const timer = setInterval(load, 10000);
  // device names may arrive after the rules
  const unsubscribe = subscribe((change) => {
    if (change.type === "snapshot" || change.type === "plugins") { renderList(); renderTemplates(); }
  });
  return () => { clearInterval(timer); unsubscribe(); };
}

function ruleCard(r, regulatedAt, on) {
  const s = r.state || {};
  const automation = r.kind === "automation";
  let status;
  if (!r.enabled) status = "Uitgeschakeld";
  else if (r.manual) status = s.momentary ? (s.active ? "Bezig" : "Klaar om uit te voeren") : s.active ? "Aan" : "Uit";
  else if (s.active_since && s.armed) status = `Voorwaarden gelden sinds ${dates.short(new Date(s.active_since))}, gaat af na ${describeDuration(r.for_s)}`;
  else if (s.active_since) status = automation ? "Uitgevoerd; voorwaarden gelden nog" : "Voorwaarden gelden; wacht tot ze een keer niet gelden";
  else if (s.armed) status = "Klaar om af te gaan";
  else status = "Wacht tot de voorwaarden een keer niet gelden";
  if (r.enabled && s.run) status = runStatus(r, s.run);
  const joiner = r.match === "any" ? "of " : "en ";
  const lines = r.manual ? [
    "Handmatig, vanaf het dashboard of hier",
    ...r.actions.map((a) => "→ " + describeAction(a)),
    ...(r.else_actions || []).map((a) => "bij uitzetten → " + describeAction(a)),
  ] : [
    ...r.conditions.map((c, i) => (i ? joiner : "Als ") + describeCondition(c)),
    r.for_s ? `gedurende ${describeDuration(r.for_s)}` : "(meteen)",
    ...(automation ? r.actions : []).map((a) => "→ " + describeAction(a)),
    ...(automation ? r.else_actions || [] : []).map((a) => "anders → " + describeAction(a)),
  ];
  if (!automation) lines.push("→ Pushmelding sturen");
  const reg = automation ? r.actions.find((a) => a.type === "regulate") : null;
  // the chart only for a rule that regulated in its window, not for one that
  // shares the device with it
  const showChart = reg && regulatedAt && Date.now() - new Date(regulatedAt).getTime() < REG_HOURS * 3600e3;
  return h("article", { class: "card contract" },
    h("div", { class: "contract-head" },
      h("div", {},
        h("h3", {}, r.name),
        h("div", { class: "secondary" }, status),
        s.last_fired ? h("div", { class: "secondary" }, `Laatst afgegaan ${ago(s.last_fired)}`) : null,
        s.last_result ? h("div", { class: "secondary" }, s.last_result) : null),
      h("div", { class: "row" },
        r.manual && r.enabled ? actionControl(r, { onDone: on.switched, onError: on.failed }) : null,
        h("button", { class: "btn", type: "button", onclick: on.edit }, "Bewerken"),
        h("button", { class: "btn", type: "button", onclick: on.copy }, "Kopiëren"),
        h("button", { class: "btn", type: "button", onclick: on.toggle }, r.enabled ? "Uitschakelen" : "Inschakelen"),
        h("button", { class: "btn danger", type: "button", onclick: on.remove }, "Verwijderen"))),
    reg ? tuneRow(reg, on.tune) : null,
    showChart ? regulateChart(r.id, reg) : null,
    h("ul", { class: "contract-lines" }, lines.map((l) => h("li", {}, l))));
}

// ---------- the chart of a regulating rule ----------
// The measured value, the device's setpoint and the target ± margin over
// the last 24 hours, from the plugin history. The setpoint is only drawn
// while this rule regulated (shaded): another rule may set the same device
// at other times. The list is drawn again every
// 10 seconds, so a chart is kept per rule (and its open state with it) and
// fetches new data at most once a minute.
const regCharts = new Map(); // rule id -> {key, node, load, loaded}
const REG_HOURS = 24;

function regulateChart(id, a) {
  const key = JSON.stringify([a.device, a.key, a.sensor, a.target, a.band]);
  const kept = regCharts.get(id);
  if (kept && kept.key === key) {
    kept.label(); // device names may have arrived since
    if (kept.node.open && Date.now() - kept.loaded > 60e3) kept.load();
    return kept.node;
  }
  const chart = h("div", { class: "chart" });
  const legend = h("div", { class: "legend" });
  const node = h("details", { class: "rule-chart" }, h("summary", {}, "Grafiek van de laatste 24 uur"), legend, chart);
  const entry = { key, node, loaded: 0 };
  // looked up each time: the devices may not be known at the first draw
  const ctl = () => pluginDevice(a.device)?.controls?.find((c) => c.key === a.key);
  const sensorName = () => who(a.sensor?.device, a.sensor?.metric);
  const setName = () => `${deviceName(a.device)} ingesteld`;
  const colors = { sensor: "var(--export)", set: "var(--import)", target: "var(--muted)" };
  const item = (color, label, dash) => {
    const k = h("span", { class: "key" + (dash ? " dashed" : "") });
    k.style.background = dash ? "none" : color;
    if (dash) k.style.borderTop = `2px dashed ${color}`;
    return h("span", {}, k, label);
  };
  const shade = h("span", { class: "key" });
  shade.style.background = colors.set;
  shade.style.opacity = "0.25";
  entry.label = () => legend.replaceChildren(
    item(colors.sensor, sensorName()),
    item(colors.set, setName()),
    item(colors.target, `doel ${num(a.target)} ± ${num(a.band)}`, true),
    h("span", {}, shade, "deze automatisering regelde"));
  entry.label();
  const history = async (device, metric, from, to) => {
    if (!isPlugin(device)) return { stepMs: 0, points: [] };
    const res = await api("GET", `/api/plugins/devices/${encodeURIComponent(device.slice(PLUGIN.length))}/history?metric=${encodeURIComponent(metric)}&from=${from.toISOString()}&to=${to.toISOString()}&points=288`);
    const stepMs = res.step_s * 1000;
    return { stepMs, points: res.points.map((p) => ({ t: new Date(p.t).getTime() + stepMs / 2, v: p.avg })) };
  };
  entry.load = async () => {
    entry.loaded = Date.now();
    const to = new Date();
    const from = new Date(to.getTime() - REG_HOURS * 3600e3);
    try {
      const [s, set, res] = await Promise.all([history(a.sensor?.device, a.sensor?.metric, from, to), history(a.device, ctl()?.metric || a.key, from, to),
        api("GET", `/api/rules/${id}/periods?from=${from.toISOString()}&to=${to.toISOString()}`)]);
      const spans = res.periods.map((p) => ({ from: new Date(p.start).getTime(), to: p.end ? new Date(p.end).getTime() : to.getTime(), color: colors.set }));
      const mine = (t) => spans.some((p) => t >= p.from && t <= p.to);
      const stepMs = s.stepMs || set.stepMs || 5 * 60e3;
      const flat = (v) => [{ t: from.getTime(), v }, { t: to.getTime(), v }];
      const unit = pluginMetric(a.sensor?.device, a.sensor?.metric)?.unit || ctl()?.unit || "";
      lineChart(chart, {
        label: "Regelen, laatste 24 uur", height: 200, zero: false, area: false,
        series: [
          { name: sensorName(), color: colors.sensor, points: s.points },
          { name: setName(), color: colors.set, points: set.points.filter((p) => mine(p.t)) },
          { name: "Doel", color: colors.target, points: flat(a.target), dash: "6 4" },
          { name: "Marge", color: colors.target, points: flat(a.target - a.band), dash: "2 4" },
          { name: "Marge", color: colors.target, points: flat(a.target + a.band), dash: "2 4" },
        ],
        from: from.getTime(), to: to.getTime(), spans,
        format: (v) => `${fmt.n1(v)}${unit ? " " + unit : ""}`, axisFormat: (v) => fmt.n1(v),
        title: (t) => dates.dayTime(new Date(t - stepMs / 2)), stepMs, gapMs: Math.max(stepMs * 3, 15 * 60e3),
      });
    } catch (e) {
      chart.replaceChildren(h("div", { class: "muted small" }, e.message));
    }
  };
  node.addEventListener("toggle", () => { if (node.open) entry.load(); });
  regCharts.set(id, entry);
  return node;
}

/**
 * The target and margin of a regulate action a, with − and +; tune(target, band)
 * saves them and the regulation goes on. opts.band false shows only the target
 * (a dashboard tile: the margin is set up on Automatisering).
 */
export function tuneRow(a, tune, opts = {}) {
  const unit = pluginDevice(a.device)?.controls?.find((x) => x.key === a.key)?.unit || "°C";
  const round = (v) => Math.round(v * 10) / 10;
  let target = a.target, band = a.band;
  const tv = h("span", { class: "rule-tune-value" });
  const bv = h("span", { class: "rule-tune-value" });
  const show = () => { tv.textContent = `${fmt.n1(target)} ${unit}`; bv.textContent = `± ${fmt.n1(band)}`; };
  show();
  let timer = null;
  const change = (dt, db) => {
    const nb = round(band + db);
    if (nb < 0.1 || nb > 5) return;
    target = round(target + dt);
    band = nb;
    show();
    // a few taps in a row are saved once
    clearTimeout(timer);
    timer = setTimeout(() => tune(target, band), 800);
  };
  const btn = (label, aria, fn) => h("button", { class: "btn rule-tune-btn", type: "button", "aria-label": aria, onclick: fn }, label);
  return h("div", { class: "rule-tune" },
    h("span", { class: "rule-tune-item" }, h("span", { class: "secondary" }, "Doel"),
      btn("−", "Doel lager", () => change(-0.1, 0)), tv, btn("+", "Doel hoger", () => change(0.1, 0))),
    opts.band === false ? null : h("span", { class: "rule-tune-item" }, h("span", { class: "secondary" }, "Marge"),
      btn("−", "Marge kleiner", () => change(0, -0.1)), bv, btn("+", "Marge groter", () => change(0, 0.1))));
}

/** What a running automation is doing: waiting (until when) or regulating (values now). */
export function runStatus(r, run) {
  const a = r.actions[run.step - 1];
  const at = (iso) => dates.short(new Date(iso));
  if (run.type === "wait") {
    return `Wacht (actie ${run.step})${a?.until ? " tot " + describeCondition(a.until) : ""}, uiterlijk ${at(run.until)}`;
  }
  const parts = [];
  if (a && run.sensor != null) parts.push(`${who(a.sensor.device, a.sensor.metric)} ${formatValue(a.sensor.device, a.sensor.metric, run.sensor)}`);
  if (a && run.setpoint != null) {
    const unit = pluginDevice(a.device)?.controls?.find((x) => x.key === a.key)?.unit;
    parts.push(`${deviceName(a.device)} ${fmt.n1(run.setpoint)}${unit ? " " + unit : ""}`);
  }
  if (run.phase === "opwarmen") parts.push("snel opwarmen tot doel − marge");
  if (run.phase === "afkoelen") parts.push("snel afkoelen tot doel + marge");
  if (run.last_step_at) parts.push(`laatste stap ${at(run.last_step_at)} ${run.last_step}`);
  if (run.next_check) parts.push(`volgende controle ${at(run.next_check)}`);
  return `Regelt${parts.length ? ": " + parts.join(", ") : ""}`;
}

function field(text, input, hint) {
  return h("label", { class: "field" }, h("span", {}, text), input, hint ? h("small", { class: "secondary" }, hint) : null);
}

function select(options, value, attrs = {}) {
  const el = h("select", attrs, options.map(([v, label]) => h("option", { value: v }, label)));
  const known = options.some(([v]) => String(v) === String(value));
  if (known) el.value = String(value);
  else if (options.length) el.value = options[0][0];
  return el;
}

/** Metrics of a device; numericOnly leaves out yes/no and text metrics (a second value). */
function metricOptions(deviceId, numericOnly = false) {
  const label = (k, [l, unit]) => [k, unit ? `${l} (${unit})` : l];
  if (deviceId === PRICE) return PRICE_METRICS.map((k) => label(k, metricLabel(k)));
  if (deviceId === TIME) return [["window", "Tijdvenster"]];
  if (isPlugin(deviceId)) {
    const ms = (pluginDevice(deviceId)?.reading?.metrics || []).filter((m) => !numericOnly || m.kind === "number");
    return [...ms.map((m) => [m.key, m.unit ? `${m.label} (${m.unit})` : m.label]), ...(numericOnly ? [] : [["online", "Online"]])];
  }
  const d = state.devices.get(deviceId);
  const keys = [...(METRICS[d?.kind] || METRICS.other)];
  for (const k of Object.keys(d?.reading?.values || {})) if (!keys.includes(k)) keys.push(k);
  return keys.filter((k) => !numericOnly || !BOOLEAN.has(k)).map((k) => label(k, metricLabel(k)));
}

/** Device choices: HomeWizard devices, plugin devices, prices and (for a condition) time. */
function deviceOptions(current, withTime) {
  const devices = [...state.devices.values()].map((d) => [d.id, d.name]);
  const plugins = [...state.plugins.values()].map((d) => [PLUGIN + d.id, `${d.name} (${d.plugin_name})`]);
  if (isPlugin(current) && !plugins.some(([id]) => id === current)) plugins.push([current, "Onbekend plugin-apparaat"]);
  return [...devices, ...plugins, [PRICE, "Prijzen (stroom en gas)"], ...(withTime ? [[TIME, "Tijd (dagen en uren)"]] : [])];
}

function numberInput(value, attrs) {
  return h("input", { type: "number", step: "any", inputmode: "decimal", value, ...attrs });
}

function conditionRow(c) {
  const deviceSel = select(deviceOptions(c.device, true), c.device, { "aria-label": "Apparaat" });
  if (!c.device && state.devices.size) deviceSel.value = [...state.devices.keys()][0];
  // time window controls
  const days = new Set(c.days?.length ? c.days : [1, 2, 3, 4, 5]);
  const dayBoxes = DAYS.map((label, i) => {
    const box = h("input", { type: "checkbox", checked: days.has(i + 1), "aria-label": label });
    return { n: i + 1, box, node: h("label", { class: "check day-check" }, box, label) };
  });
  const fromInput = h("input", { type: "time", value: c.from || "06:30", "aria-label": "Van" });
  const toInput = h("input", { type: "time", value: c.to || "07:30", "aria-label": "Tot" });
  const inSel = select([["1", "binnen"], ["0", "buiten"]], c.device === TIME && c.value === 0 ? "0" : "1", { "aria-label": "Binnen of buiten" });
  const windowBox = h("span", { class: "row rule-window" }, inSel, ...dayBoxes.map((d) => d.node),
    h("span", { class: "secondary" }, "van"), fromInput, h("span", { class: "secondary" }, "tot"), toInput);

  const metricSlot = h("span");
  const scale = numberInput(c.scale || 1, { class: "num-short", "aria-label": "Factor", title: "Factor, bijvoorbeeld 0,5 (m³ per half uur stoken)" });
  const scaleBox = h("span", { class: "row rule-scale" }, scale, h("span", { class: "secondary" }, "×"));
  const opSel = select(Object.entries(OPS), c.op, { "aria-label": "Vergelijking" });
  const textOpSel = select(Object.entries(TEXT_OPS), c.op === "!=" ? "!=" : "=", { "aria-label": "Vergelijking" });
  const target = select([["value", "een getal"], ["ref", "een andere waarde"]], c.ref ? "ref" : "value", { "aria-label": "Vergelijken met" });
  const value = numberInput(c.ref ? 0 : c.value ?? 0, { "aria-label": "Waarde" });
  const boolSel = select([["1", "ja"], ["0", "nee"]], (c.op === "!=") !== (c.value >= 0.5) ? "1" : "0", { "aria-label": "Waarde" });
  const text = h("input", { type: "text", maxlength: 64, value: c.text || "", "aria-label": "Tekst", placeholder: "bijv. Verwarmen" });
  const isText = h("span", { class: "secondary" }, "is");
  const unit = h("span", { class: "secondary" });
  // the second value: factor × device metric + offset
  const ref = c.ref || {};
  const refScale = numberInput(ref.scale || 1, { class: "num-short", "aria-label": "Factor van de tweede waarde" });
  const refDevice = select(deviceOptions(ref.device, false), ref.device || PRICE, { "aria-label": "Apparaat van de tweede waarde" });
  const refMetricSlot = h("span");
  const offset = numberInput(c.ref ? c.value || 0 : 0, { class: "num-short", "aria-label": "Plus" });
  const refBox = h("span", { class: "row rule-ref" }, refScale, h("span", { class: "secondary" }, "×"), refDevice, refMetricSlot,
    h("span", { class: "secondary" }, "+"), offset);
  const current = h("small", { class: "secondary rule-now" });
  const rm = h("button", { class: "btn", type: "button", "aria-label": "Voorwaarde verwijderen" }, "×");
  let metricSel, refMetricSel;

  const refresh = () => {
    if (!metricSel || !refMetricSel) return; // still building the row
    const timeWindow = deviceSel.value === TIME;
    windowBox.hidden = !timeWindow;
    metricSlot.hidden = timeWindow;
    if (timeWindow) {
      for (const el of [opSel, textOpSel, value, boolSel, text, isText, target, refBox, scaleBox]) el.hidden = true;
      unit.textContent = "";
      current.textContent = "tijdzone van de gateway; tot is niet inbegrepen, over middernacht kan";
      return;
    }
    const metric = metricSel.value;
    const kind = metricKind(deviceSel.value, metric);
    const boolean = kind === "bool", textual = kind === "text";
    const withRef = !boolean && !textual && target.value === "ref";
    opSel.hidden = boolean || textual;
    textOpSel.hidden = !textual;
    target.hidden = boolean || textual;
    value.hidden = boolean || textual || withRef;
    boolSel.hidden = !boolean;
    text.hidden = !textual;
    isText.hidden = !boolean;
    refBox.hidden = !withRef;
    scaleBox.hidden = !withRef;
    unit.textContent = boolean || textual || withRef ? "" : metricOf(deviceSel.value, metric)[1];
    // what the comparison says now
    const left = currentValue(deviceSel.value, metric);
    if (textual) {
      const m = pluginMetric(deviceSel.value, metric);
      current.textContent = m ? `nu: ${m.value}` : "";
    } else if (left == null) {
      current.textContent = "";
    } else if (withRef) {
      const r = currentValue(refDevice.value, refMetricSel.value);
      const a = left * (Number(scale.value) || 1);
      current.textContent = r == null ? `nu: ${num(a)} links`
        : `nu: ${num(a)} tegen ${num(r * (Number(refScale.value) || 1) + Number(offset.value || 0))}`;
    } else {
      current.textContent = `nu: ${formatValue(deviceSel.value, metric, left)}`;
    }
  };
  const setMetrics = (keep) => {
    metricSel = select(metricOptions(deviceSel.value), keep, { "aria-label": "Meetwaarde" });
    metricSel.addEventListener("change", refresh);
    metricSlot.replaceChildren(metricSel);
    refresh();
  };
  const setRefMetrics = (keep) => {
    refMetricSel = select(metricOptions(refDevice.value, true), keep, { "aria-label": "Meetwaarde van de tweede waarde" });
    refMetricSel.addEventListener("change", refresh);
    refMetricSlot.replaceChildren(refMetricSel);
    refresh();
  };
  deviceSel.addEventListener("change", () => setMetrics(metricSel.value));
  refDevice.addEventListener("change", () => setRefMetrics(refMetricSel.value));
  for (const el of [target, scale, refScale, offset]) el.addEventListener("input", refresh);
  setRefMetrics(ref.metric || "price_all_in");
  setMetrics(c.metric);

  const row = h("div", { class: "row rule-condition" }, scaleBox, deviceSel, metricSlot, windowBox, opSel, textOpSel, isText, boolSel, text,
    target, value, unit, refBox, current, rm);
  row.read = () => {
    if (deviceSel.value === TIME) {
      return { device: TIME, metric: "window", op: "=", value: Number(inSel.value),
        days: dayBoxes.filter((d) => d.box.checked).map((d) => d.n), from: fromInput.value, to: toInput.value };
    }
    const metric = metricSel.value;
    const kind = metricKind(deviceSel.value, metric);
    if (kind === "bool") return { device: deviceSel.value, metric, op: "=", value: Number(boolSel.value) };
    if (kind === "text") return { device: deviceSel.value, metric, op: textOpSel.value, value: 0, text: text.value.trim() };
    if (target.value === "ref") {
      return { device: deviceSel.value, metric, op: opSel.value, scale: Number(scale.value || 1), value: Number(offset.value || 0),
        ref: { device: refDevice.value, metric: refMetricSel.value, scale: Number(refScale.value || 1) } };
    }
    return { device: deviceSel.value, metric, op: opSel.value, value: Number(value.value || 0) };
  };
  rm.addEventListener("click", () => row.remove());
  return row;
}

function switchRow(a) {
  const sockets = [...state.devices.values()].filter((d) => d.can_switch || d.id === a.device);
  const deviceSel = select(sockets.map((d) => [d.id, d.name]), a.device, { "aria-label": "Socket" });
  const onSel = select([["0", "uitzetten"], ["1", "aanzetten"]], a.on ? "1" : "0", { "aria-label": "Aan of uit" });
  const rm = h("button", { class: "btn", type: "button", "aria-label": "Actie verwijderen" }, "×");
  const row = h("div", { class: "row" }, deviceSel, onSel, rm);
  row.read = () => ({ type: "switch", device: deviceSel.value, on: onSel.value === "1" });
  rm.addEventListener("click", () => row.remove());
  return row;
}

// plugin_set: a plugin device, one of its controls and the value to set
function pluginSetRow(a) {
  const list = controllable();
  const options = list.map((d) => [PLUGIN + d.id, `${d.name} (${d.plugin_name})`]);
  if (a.device && !options.some(([id]) => id === a.device)) options.push([a.device, `${deviceName(a.device)} (bediening uit)`]);
  const deviceSel = select(options, a.device, { "aria-label": "Plugin-apparaat" });
  const controlSlot = h("span");
  const valueSlot = h("span");
  const rm = h("button", { class: "btn", type: "button", "aria-label": "Actie verwijderen" }, "×");
  let controlSel = null;
  let readValue = () => null;

  const setValue = (keep) => {
    const c = pluginDevice(deviceSel.value)?.controls?.find((x) => x.key === controlSel?.value);
    readValue = () => null;
    if (!c) { valueSlot.replaceChildren(); return; }
    if (c.kind === "switch") {
      const sel = select([["1", "aan"], ["0", "uit"]], keep === false ? "0" : "1", { "aria-label": "Waarde" });
      readValue = () => sel.value === "1";
      valueSlot.replaceChildren(sel);
    } else if (c.kind === "setpoint") {
      const v = typeof keep === "number" ? keep : Math.round(((c.min + c.max) / 2) / c.step) * c.step;
      const input = h("input", { type: "number", min: c.min, max: c.max, step: c.step || "any", inputmode: "decimal", value: v, "aria-label": "Waarde" });
      readValue = () => Number(input.value);
      valueSlot.replaceChildren(...[input, c.unit ? h("span", { class: "secondary" }, c.unit) : null].filter(Boolean));
    } else if (c.kind === "select") {
      const sel = select((c.options || []).map((o) => [o.value, o.label || o.value]), keep, { "aria-label": "Waarde" });
      readValue = () => sel.value;
      valueSlot.replaceChildren(sel);
    } else {
      valueSlot.replaceChildren();
    }
  };
  const setControls = (keepKey, keepValue) => {
    // hidden controls (replaced by clearer ones) stay listed only for a
    // rule that already uses one
    const controls = (pluginDevice(deviceSel.value)?.controls || []).filter((c) => !c.hidden || c.key === keepKey);
    controlSel = select(controls.map((c) => [c.key, c.label]), keepKey, { "aria-label": "Instelling" });
    controlSel.addEventListener("change", () => setValue(undefined));
    controlSlot.replaceChildren(controlSel);
    setValue(controls.some((c) => c.key === keepKey) ? keepValue : undefined);
  };
  deviceSel.addEventListener("change", () => setControls(controlSel?.value));
  setControls(a.key, a.value);

  const row = h("div", { class: "row" }, deviceSel, controlSlot, valueSlot, rm);
  row.read = () => ({ type: "plugin_set", device: deviceSel.value, key: controlSel?.value || "", value: readValue() });
  rm.addEventListener("click", () => row.remove());
  return row;
}

/** The actions of an automation (then, or otherwise): sockets, plugin devices, a push message. */
function actionsBlock(actions, notifyText) {
  const notify = h("input", { type: "checkbox", checked: actions.some((a) => a.type === "notify") });
  const switches = h("div", { class: "rule-rows" });
  for (const a of actions) if (a.type === "switch") switches.append(switchRow(a));
  const canSwitch = [...state.devices.values()].some((d) => d.can_switch);
  const addSwitch = h("button", { class: "btn", type: "button", disabled: !canSwitch,
    title: canSwitch ? "" : "Er is geen socket die geschakeld kan worden (alleen Energy Sockets via API v1).",
    onclick: () => switches.append(switchRow({ device: "", on: false })) }, "+ Socket schakelen");
  const pluginSets = h("div", { class: "rule-rows" });
  for (const a of actions) if (a.type === "plugin_set") pluginSets.append(pluginSetRow(a));
  const canSet = controllable().length > 0;
  const addSet = h("button", { class: "btn", type: "button", disabled: !canSet,
    title: canSet ? "" : "Zet eerst ‘Bediening toestaan’ aan bij de plugin (Instellingen → Plugins).",
    onclick: () => pluginSets.append(pluginSetRow({})) }, "+ Apparaat bedienen");
  return {
    node: h("div", {},
      switches, pluginSets,
      h("div", { class: "row" }, addSwitch, addSet,
        canSet ? null : h("small", { class: "secondary" }, "Airco, tado of Tuya bedienen: zet eerst ‘Bediening toestaan’ aan bij de plugin.")),
      h("label", { class: "check" }, notify, notifyText)),
    read: () => [
      ...[...switches.children].map((row) => row.read()),
      ...[...pluginSets.children].map((row) => row.read()),
      ...(notify.checked ? [{ type: "notify" }] : []),
    ],
  };
}

/** The actions of an automation in order: the first actions, then wait steps that are each
 * followed by actions, and at the end optionally regulating. */
function stepsBlock(actions) {
  const segments = [[]];
  const waits = [];
  let regulate = null;
  for (const a of actions) {
    if (a.type === "wait") { waits.push(a); segments.push([]); }
    else if (a.type === "regulate") regulate = a;
    else segments[segments.length - 1].push(a);
  }
  const first = actionsBlock(segments[0], "Ook een pushmelding sturen");
  const waitParts = h("div", { class: "rule-rows" });
  const addWait = (w, acts) => { const part = waitPart(w, acts); waitParts.append(part); };
  waits.forEach((w, i) => addWait(w, segments[i + 1]));
  const regSlot = h("div", { class: "rule-rows" });
  let reg = null;
  const canReg = regulable().length > 0;
  const addReg = h("button", { class: "btn", type: "button", disabled: !canReg,
    title: canReg ? "" : "Regelen kan bij een airco of elektrische radiator met bediening aan (Instellingen → Plugins).",
    onclick: () => setRegulate(defaultRegulate()) }, "+ Regelen");
  const setRegulate = (a) => {
    reg = a ? regulateRow(a, () => setRegulate(null)) : null;
    regSlot.replaceChildren(...(reg ? [reg] : []));
    addReg.hidden = !!reg;
  };
  setRegulate(regulate);
  const addWaitBtn = h("button", { class: "btn", type: "button", title: "Wacht tot iets geldt (of een vaste tijd) en voer daarna de volgende acties uit",
    onclick: () => addWait({ type: "wait", max_s: 1800, fresh: true }, []) }, "+ Wachtstap");
  return {
    node: h("div", {}, first.node, waitParts, regSlot, h("div", { class: "row" }, addWaitBtn, addReg)),
    read: () => [...first.read(), ...[...waitParts.children].flatMap((p) => p.read()), ...(reg ? [reg.read()] : [])],
  };
}

/** A wait step: until a condition holds (at most some minutes) or a fixed time, then its own actions. */
function waitPart(w, actions) {
  const room = roomTemp();
  const until = w.until || (room ? { device: PLUGIN + room[0].id, metric: room[1].key, op: ">=", value: 19 } : { device: "", metric: "power_w", op: "<", value: 0 });
  // a new wait step waits for a condition; a stored one without a condition waits a fixed time
  const mode = select([["until", "Wacht tot"], ["time", "Wacht"]], w.until || w.fresh ? "until" : "time", { "aria-label": "Soort wachtstap" });
  const cond = conditionRow(until);
  const rmCond = cond.querySelector('[aria-label="Voorwaarde verwijderen"]');
  if (rmCond) rmCond.hidden = true;
  const max = numberInput(Math.round((w.max_s || 1800) / 60), { min: 1, max: 1440, class: "num-short", "aria-label": "Minuten" });
  const maxLabel = h("span", { class: "secondary" });
  const rm = h("button", { class: "btn", type: "button", "aria-label": "Wachtstap verwijderen" }, "×");
  const then = actionsBlock(actions, "Pushmelding sturen");
  const refresh = () => {
    const withCond = mode.value === "until";
    cond.hidden = !withCond;
    maxLabel.textContent = withCond ? "het volgende geldt, hooguit" : "";
  };
  mode.addEventListener("change", refresh);
  refresh();
  const part = h("div", { class: "card rule-step" },
    h("div", { class: "row" }, mode, maxLabel, max, h("span", { class: "secondary" }, "minuten"), rm),
    cond,
    h("p", { class: "secondary" }, "Daarna (als de voorwaarde geldt of de tijd om is):"),
    then.node);
  part.read = () => [
    { type: "wait", max_s: Math.round(Number(max.value || 0) * 60), ...(mode.value === "until" ? { until: cond.read() } : {}) },
    ...then.read(),
  ];
  rm.addEventListener("click", () => part.remove());
  return part;
}

/** A regulate action for the first device that can be regulated, on the room temperature. */
function defaultRegulate() {
  const d = regulable()[0];
  const c = d?.controls.find((x) => x.kind === "setpoint" && !x.hidden);
  const room = roomTemp();
  return {
    type: "regulate", device: d ? PLUGIN + d.id : "", key: c?.key || "",
    sensor: room ? { device: PLUGIN + room[0].id, metric: room[1].key } : { device: "", metric: "" },
    target: 19, band: 0.2, step: c?.step || 0.5, every_s: 900,
    offset: 1, below: 2, above: 2, boost: false, stop_window: false, off_at_end: false,
  };
}

/** Regulate: keep a measured value at a target by stepping a device's temperature. */
function regulateRow(a, onRemove) {
  const options = regulable().map((d) => [PLUGIN + d.id, `${d.name} (${d.plugin_name})`]);
  if (a.device && !options.some(([id]) => id === a.device)) options.push([a.device, `${deviceName(a.device)} (niet te regelen)`]);
  const deviceSel = select(options, a.device, { "aria-label": "Apparaat dat geregeld wordt" });
  const controlSlot = h("span");
  let controlSel = null;
  const setControls = (keep) => {
    const cs = (pluginDevice(deviceSel.value)?.controls || []).filter((c) => c.kind === "setpoint" && (!c.hidden || c.key === keep));
    controlSel = select(cs.map((c) => [c.key, c.label]), keep, { "aria-label": "Instelling" });
    controlSlot.replaceChildren(controlSel);
  };
  deviceSel.addEventListener("change", () => setControls(controlSel?.value));
  setControls(a.key);

  const sensorDev = select(deviceOptions(a.sensor?.device, false).filter(([id]) => id !== PRICE), a.sensor?.device, { "aria-label": "Apparaat van de meetwaarde" });
  const sensorSlot = h("span");
  const now = h("small", { class: "secondary rule-now" });
  let sensorMetric = null;
  const showNow = () => {
    const v = sensorMetric ? currentValue(sensorDev.value, sensorMetric.value) : null;
    now.textContent = v == null ? "" : `nu: ${formatValue(sensorDev.value, sensorMetric.value, v)}`;
  };
  const setSensor = (keep) => {
    sensorMetric = select(metricOptions(sensorDev.value, true), keep, { "aria-label": "Meetwaarde" });
    sensorMetric.addEventListener("change", showNow);
    sensorSlot.replaceChildren(sensorMetric);
    showNow();
  };
  sensorDev.addEventListener("change", () => setSensor(sensorMetric?.value));
  setSensor(a.sensor?.metric);

  const n = (v, label, attrs = {}) => numberInput(v, { class: "num-short", "aria-label": label, ...attrs });
  const target = n(a.target ?? 19, "Doel");
  const band = n(a.band ?? 0.2, "Marge", { min: 0.05, max: 5 });
  const step = n(a.step ?? 0.5, "Stap", { min: 0.1, max: 5 });
  const every = n(Math.round((a.every_s || 900) / 60), "Minuten", { min: 1, max: 1440 });
  const offset = n(a.offset ?? 0, "Afwijking", { min: -10, max: 10 });
  const below = n(a.below ?? 2, "Onder", { min: -10, max: 10 });
  const above = n(a.above ?? 2, "Boven", { min: -10, max: 10 });
  const boost = h("input", { type: "checkbox", checked: !!a.boost });
  const rangeNow = h("small", { class: "secondary rule-now" });
  const boostNow = h("small", { class: "secondary" });
  const showRange = () => {
    const unit = pluginDevice(deviceSel.value)?.controls?.find((x) => x.key === controlSel?.value)?.unit || "";
    const u = unit ? " " + unit : "";
    const r = rangeOf({ target: Number(target.value), offset: Number(offset.value), below: Number(below.value), above: Number(above.value) });
    rangeNow.textContent = `${num(r.mid)}${u}, tussen ${num(r.min)} en ${num(r.max)}${u}`;
    boostNow.textContent = boost.checked
      ? `Te koud: eerst ${num(r.max)}${u}; te warm: eerst ${num(r.min)}${u}. Binnen doel ± marge gaat hij naar ${num(r.mid)}${u} en regelt dan verder in stappen.`
      : `Begint op ${num(r.mid)}${u} en regelt dan in stappen.`;
  };
  for (const el of [target, offset, below, above]) el.addEventListener("input", showRange);
  const stopWindow = h("input", { type: "checkbox", checked: !!a.stop_window });
  const offAtEnd = h("input", { type: "checkbox", checked: !!a.off_at_end });
  const offLabel = h("label", { class: "check" }, offAtEnd, "dan uitzetten");
  const notifySteps = h("input", { type: "checkbox", checked: !!a.notify_steps });
  const syncOff = () => { offLabel.hidden = !stopWindow.checked; showRange(); };
  boost.addEventListener("change", syncOff);
  stopWindow.addEventListener("change", syncOff);
  syncOff();
  const rm = h("button", { class: "btn", type: "button", "aria-label": "Regelen verwijderen", onclick: onRemove }, "×");
  const s = (t) => h("span", { class: "secondary" }, t);

  const row = h("div", { class: "card rule-step" },
    h("div", { class: "row" }, h("strong", {}, "Regelen"), deviceSel, controlSlot, rm),
    h("div", { class: "row" }, s("op meetwaarde"), sensorDev, sensorSlot, now),
    h("div", { class: "row" }, s("doel"), target, s("marge ±"), band),
    h("div", { class: "row" }, s("apparaat op doel +"), offset, rangeNow),
    h("small", { class: "secondary" }, "Een airco of radiator meet bij het apparaat: om de kamer op 19 °C te houden moet hij vaak op 20 °C staan. Vul dat verschil in als afwijking (dan +1)."),
    h("div", { class: "row" }, h("label", { class: "check" }, boost, "Eerst snel naar het doel")),
    boostNow,
    h("div", { class: "row" }, h("label", { class: "check" }, notifySteps, "Pushmelding bij elke stap en als het regelen stopt (ter controle)")),
    h("details", { class: "rule-more" },
      h("summary", {}, "Meer instellingen"),
      h("div", { class: "row" }, s("elke"), every, s("minuten een stap van"), step, s("omhoog of omlaag")),
      h("div", { class: "row" }, s("tussen doel −"), below, s("en doel +"), above),
      h("div", { class: "row" }, s("Stopt als het apparaat uit gaat"),
        h("span", { class: "rule-conditions-only" }, s(", en ook "), h("label", { class: "check" }, stopWindow, "als het tijdvenster eindigt"), offLabel))),
    h("small", { class: "secondary" }, "Boven doel + marge gaat de temperatuur van het apparaat een stap omlaag, onder doel − marge een stap omhoog; daartussen blijft hij staan. Na een stap met de afstandsbediening gaat het regelen verder vanaf die stand."));
  showRange();
  controlSlot.addEventListener("change", showRange);
  deviceSel.addEventListener("change", showRange);
  row.read = () => ({
    type: "regulate", device: deviceSel.value, key: controlSel?.value || "",
    sensor: { device: sensorDev.value, metric: sensorMetric?.value || "" },
    target: Number(target.value), band: Number(band.value), step: Number(step.value),
    every_s: Math.round(Number(every.value || 0) * 60),
    offset: Number(offset.value), below: Number(below.value), above: Number(above.value), boost: boost.checked,
    stop_window: stopWindow.checked, off_at_end: stopWindow.checked && offAtEnd.checked,
    notify_steps: notifySteps.checked,
  });
  return row;
}

function ruleForm(rule, mode, onSaved, onCancel) {
  const automation = mode.kind === "automation";
  const name = h("input", { type: "text", maxlength: 80, value: rule.name || "", required: true });
  // an automation starts when its conditions hold, or by hand (a manual action)
  const start = select([["conditions", "als de voorwaarden gelden"], ["manual", "handmatig, vanaf het dashboard"]],
    rule.manual ? "manual" : "conditions", { "aria-label": "Wanneer de automatisering start" });
  const match = select([["all", "alle voorwaarden gelden"], ["any", "één van de voorwaarden geldt"]], rule.match || "all", { "aria-label": "Hoe de voorwaarden samengaan" });
  const conditions = h("div", { class: "rule-rows" });
  for (const c of rule.conditions || []) conditions.append(conditionRow(c));
  const addCond = h("button", { class: "btn", type: "button", onclick: () => conditions.append(conditionRow({ device: "", metric: "power_w", op: "<", value: 0 })) }, "+ Voorwaarde");

  const secs = rule.for_s || 0;
  const [amount, unitValue] = secs && secs % 3600 === 0 ? [secs / 3600, "3600"] : secs % 60 === 0 ? [secs / 60, "60"] : [secs, "1"];
  const duration = h("input", { type: "number", min: 0, step: "any", inputmode: "decimal", value: amount });
  const durUnit = select([["1", "seconden"], ["60", "minuten"], ["3600", "uur"]], unitValue, { "aria-label": "Eenheid" });

  const message = h("input", { type: "text", maxlength: 300, value: rule.message || "", placeholder: "leeg = de voorwaarden met hun waarde" });
  const then = automation ? stepsBlock(rule.actions || []) : null;
  const otherwise = automation ? actionsBlock(rule.else_actions || [], "Pushmelding sturen") : null;

  const error = h("div", { class: "form-error", role: "alert", hidden: true });
  const submit = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
  const title = rule.id ? (automation ? "Automatisering bewerken" : "Melding bewerken") : mode.newLabel;
  const form = h("form", { class: "card contract-form rule-form" },
    h("h3", {}, title),
    h("p", { class: "secondary narrow-only" }, "Instellen werkt het makkelijkst op een groter scherm."),
    rule.hint ? h("p", { class: "secondary" }, rule.hint) : null,
    h("div", { class: "form-grid" }, field("Naam", name, automation ? "ook de titel van een pushmelding" : "ook de titel van de melding")),
    automation ? h("div", { class: "form-grid" }, field("Starten", start)) : null,
    automation ? h("p", { class: "secondary rule-manual-only" }, "Een handmatige actie zet je aan en uit op een dashboardtegel (Bediening → kies de actie) of hier. Heeft hij geen acties bij uitzetten, geen wachtstap en geen regelen, dan is het een knop die de acties één keer uitvoert.") : null,
    h("h4", { class: "rule-conditions-only" }, "Als"),
    h("div", { class: "row rule-conditions-only" }, match),
    h("div", { class: "rule-conditions-only" }, conditions, h("div", { class: "row" }, addCond)),
    h("p", { class: "secondary rule-conditions-only" }, "All-in prijzen volgen je energiecontract (opslag, energiebelasting en btw); kaal is de marktprijs zonder btw. Uren vandaag goedkoper: bij lager dan 4 is het nu een van de 4 goedkoopste uren. Vergelijk met een getal, of met een andere waarde maal een factor. Voorbeeld: een half uur stoken kost 0,5 m³ gas of 0,8 kWh met de airco: 0,5 × gasprijs all-in hoger dan 0,8 × stroomprijs all-in betekent dat de airco goedkoper is."),
    h("h4", { class: "rule-conditions-only" }, "Gedurende"),
    h("div", { class: "row rule-conditions-only" }, duration, durUnit, h("small", { class: "secondary" }, "0 = meteen; een korte onderbreking start de tijd opnieuw")),
    h("h4", {}, "Dan"),
    automation ? h("p", { class: "secondary" }, "Met een wachtstap gaan de acties daarna pas verder als iets geldt of de tijd om is, ook als de voorwaarden hierboven dan al niet meer gelden. Regelen houdt een meetwaarde (bijvoorbeeld van tado) op een doel door de temperatuur van een airco of elektrische radiator in stappen bij te stellen.") : null,
    automation ? then.node : h("p", { class: "secondary" }, "Pushmelding naar alle apps met meldingen aan."),
    h("div", { class: "form-grid" }, field("Tekst van de melding (optioneel)", message)),
    automation ? h("h4", { class: "rule-conditions-only" }, "Als de voorwaarden niet meer gelden (optioneel)") : null,
    automation ? h("h4", { class: "rule-manual-only" }, "Bij uitzetten (optioneel)") : null,
    automation ? h("p", { class: "secondary" }, "Eén keer, nadat de automatisering is afgegaan (of de handmatige actie aan stond): bijvoorbeeld de airco weer uit, of de CV weer op schema.") : null,
    automation ? otherwise.node : null,
    error,
    h("div", { class: "row form-actions" }, submit, h("button", { class: "btn", type: "button", onclick: onCancel }, "Annuleren")));

  const syncStart = () => form.classList.toggle("manual", automation && start.value === "manual");
  start.addEventListener("change", syncStart);
  syncStart();

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    error.hidden = true;
    const manual = automation && start.value === "manual";
    // an automation (or a copy, which is saved switched off) made into a
    // manual action is switched on: it only starts when the user starts it,
    // and a switched-off action cannot be started from the dashboard
    const enabled = manual && !rule.manual ? true : rule.enabled ?? true;
    const body = {
      kind: mode.kind, name: name.value, enabled, message: message.value, match: match.value,
      for_s: Math.round(Number(duration.value || 0) * Number(durUnit.value)),
      conditions: manual ? [] : [...conditions.children].map((row) => row.read()),
      actions: automation ? then.read() : [{ type: "notify" }],
      else_actions: automation ? otherwise.read() : [],
      manual,
    };
    // a manual action has no time window to stop at
    if (manual) body.actions = body.actions.map((a) => (a.type === "regulate" ? { ...a, stop_window: false, off_at_end: false } : a));
    submit.disabled = true;
    try {
      if (rule.id) await api("PUT", `/api/rules/${rule.id}`, body);
      else await api("POST", "/api/rules", body);
      await onSaved();
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
    } finally {
      submit.disabled = false;
    }
  });
  return form;
}

// ---------- Web Push ----------

function base64ToBytes(s) {
  const b64 = s.replaceAll("-", "+").replaceAll("_", "/") + "=".repeat((4 - (s.length % 4)) % 4);
  return Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
}
function sameKey(buf, key) {
  if (!buf) return false;
  const a = new Uint8Array(buf);
  return a.length === key.length && a.every((v, i) => v === key[i]);
}
function deviceLabel() {
  const ua = navigator.userAgent;
  const os = /iPhone/.test(ua) ? "iPhone" : /iPad/.test(ua) ? "iPad" : /Android/.test(ua) ? "Android"
    : /Windows/.test(ua) ? "Windows" : /Mac OS X/.test(ua) ? "Mac" : /Linux/.test(ua) ? "Linux" : "Apparaat";
  const browser = /SamsungBrowser/.test(ua) ? "Samsung Internet" : /Edg\//.test(ua) ? "Edge" : /Firefox\//.test(ua) ? "Firefox"
    : /Chrome\//.test(ua) ? "Chrome" : /Safari\//.test(ua) ? "Safari" : "browser";
  return `${os} · ${browser}`;
}

async function renderPush(card) {
  const error = h("div", { class: "form-error", role: "alert", hidden: true });
  const show = (...children) => card.replaceChildren(...children.filter((c) => c != null), error);
  const fail = (e) => { error.textContent = e.message || String(e); error.hidden = false; };

  if (!window.isSecureContext) {
    show(h("p", {}, "Meldingen werken alleen als je de app via https opent, bijvoorbeeld via je Tailscale-adres (https://…ts.net) of met een certificaat (HWLOCAL_TLS)."),
      h("p", { class: "secondary" }, `Nu geopend via ${location.origin}.`));
    return;
  }
  if (!("serviceWorker" in navigator) || !("PushManager" in window) || !("Notification" in window)) {
    const ios = /iPhone|iPad/.test(navigator.userAgent);
    show(h("p", {}, ios
      ? "Op iPhone en iPad werken meldingen alleen in de app op het beginscherm (iOS 16.4 of nieuwer): Deel → Zet op beginscherm, en open de app daarvandaan."
      : "Deze browser ondersteunt geen pushmeldingen."));
    return;
  }

  let info, reg, sub;
  // ready never settles when the service worker could not be registered
  const ready = Promise.race([
    navigator.serviceWorker.ready,
    new Promise((_, reject) => setTimeout(() => reject(new Error("De service worker van de app draait niet, en zonder kunnen er geen meldingen komen. Herlaad de app; helpt dat niet, open hem dan opnieuw vanaf het beginscherm.")), 5000)),
  ]);
  show(h("p", { class: "secondary" }, "Laden…"));
  const refresh = async () => {
    error.hidden = true;
    try {
      [info, reg] = await Promise.all([api("GET", "/api/push"), ready]);
      sub = await reg.pushManager.getSubscription();
    } catch (e) { show(); fail(e); return; }
    if (!info.available) { show(h("p", {}, "Meldingen zijn niet beschikbaar op deze gateway.")); return; }
    const key = base64ToBytes(info.public_key);
    const mine = sub && sameKey(sub.options?.applicationServerKey, key) && info.subscriptions.some((s) => s.endpoint === sub.endpoint);
    const denied = Notification.permission === "denied";

    const enable = h("button", { class: "btn primary", type: "button", disabled: denied, onclick: async () => {
      enable.disabled = true;
      try {
        if (await Notification.requestPermission() !== "granted") throw new Error("Meldingen zijn niet toegestaan voor deze app.");
        if (sub && !sameKey(sub.options?.applicationServerKey, key)) await sub.unsubscribe();
        const s = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
        await api("POST", "/api/push/subscriptions", { ...s.toJSON(), label: deviceLabel() });
        await api("POST", "/api/push/test", { endpoint: s.endpoint });
        await refresh();
      } catch (e) { enable.disabled = false; fail(e); }
    } }, "Meldingen aanzetten");
    const disable = h("button", { class: "btn", type: "button", onclick: async () => {
      try {
        await api("DELETE", "/api/push/subscriptions", { endpoint: sub.endpoint });
        await sub.unsubscribe();
        await refresh();
      } catch (e) { fail(e); }
    } }, "Uitzetten");
    const test = h("button", { class: "btn", type: "button", onclick: async () => {
      error.hidden = true;
      try { await api("POST", "/api/push/test", { endpoint: sub.endpoint }); } catch (e) { fail(e); }
    } }, "Testmelding");

    const others = info.subscriptions.filter((s) => !sub || s.endpoint !== sub.endpoint);
    show(
      h("p", {}, mine ? "Meldingen staan aan op dit apparaat." : denied
        ? "Meldingen zijn geblokkeerd voor deze app. Sta ze toe in de instellingen van de browser of telefoon."
        : "Zet meldingen aan om alerts te krijgen, ook als de app dicht is."),
      h("div", { class: "row" }, mine ? [test, disable] : enable),
      others.length ? h("div", {},
        h("p", { class: "secondary" }, "Ook aan op:"),
        h("ul", { class: "contract-lines" }, others.map((s) => h("li", {}, `${s.label} (sinds ${dates.day(new Date(s.created))}) `,
          h("button", { class: "btn", type: "button", onclick: async () => {
            try { await api("DELETE", "/api/push/subscriptions", { endpoint: s.endpoint }); await refresh(); } catch (e) { fail(e); }
          } }, "Verwijderen"))))) : null,
      h("p", { class: "secondary" }, "Een melding gaat via de pushdienst van de browser (Google, Mozilla of Apple) en is onderweg versleuteld."),
    );
  };
  await refresh();
}
