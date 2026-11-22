// Instellingen: the gateway's settings (history import, price sources,
// the forecast's percentages, security, remote access, reports and e-mail,
// backup, gateway information), the order of the tab row, then the plugins
// (plugins.js). #/instellingen/prognose opens the forecast settings.
import { state, subscribe, tabList } from "./app.js";
import { h, api } from "./util.js";
import { importCard, entsoeCard, accessCard, tailscaleServeCard, gatewayCard } from "./devices.js";
import { securityCard } from "./security.js";
import { reportsCard, mailCard } from "./reports.js";
import { renderPlugins } from "./plugins.js";
import { backupCard } from "./backup.js";
import { numberInput, field, PERCENT_LABEL, percentValue, percentSource } from "./costs.js";

export function renderSettings(view, sub) {
  const gateway = gatewayCard();
  const forecast = forecastCard();
  const order = tabOrderCard();
  const reports = reportsCard();
  view.append(
    h("h2", { class: "section-title" }, "Historie importeren"), importCard(),
    h("h2", { class: "section-title" }, "Prijsbronnen"), entsoeCard(),
    h("h2", { class: "section-title" }, "Prognose (geavanceerd)"), forecast.node,
    h("h2", { class: "section-title" }, "Beveiliging"), securityCard(),
    h("h2", { class: "section-title" }, "Toegang op afstand"), accessCard(), tailscaleServeCard(),
    h("h2", { class: "section-title", id: "rapporten" }, "Rapporten"), reports.node,
    h("h2", { class: "section-title" }, "E-mail"), mailCard(reports.reload),
    h("h2", { class: "section-title" }, "Back-up"), backupCard(),
    h("h2", { class: "section-title" }, "Gateway"), gateway.node,
    h("h2", { class: "section-title" }, "Tabbladen"), order.node,
  );
  const plugins = renderPlugins(view);
  if (sub === "prognose") forecast.open();
  return () => { gateway.cleanup(); order.cleanup(); plugins?.(); };
}

/** The order of the tab row: move a tab up or down; saved at once for every client. */
function tabOrderCard() {
  const list = h("div", { class: "tab-order" });
  const msg = h("div", { class: "form-error", role: "alert", hidden: true });
  const reset = h("button", { class: "btn-ghost", type: "button" }, "Standaardvolgorde");
  const summary = h("summary", {}, "Volgorde aanpassen");
  // closed by default, so the list of tabs does not fill the page
  const node = h("section", { class: "card" },
    h("p", { class: "secondary" }, "Zet de tabbladen in de volgorde die je wilt, bijvoorbeeld een eigen tabblad als ‘Verwarmen’ direct naast Dashboard. De volgorde geldt op elk apparaat waarmee je de app opent."),
    h("details", { class: "tab-order-box" }, summary, list, h("div", { class: "row" }, reset)), msg);

  let busy = false;
  async function save(order) {
    busy = true;
    msg.hidden = true;
    render();
    try {
      const res = await api("PUT", "/api/settings/tabs", { order });
      state.tabOrder = res.order; // the stream event follows; show it now
    } catch (e) {
      msg.textContent = e.message;
      msg.hidden = false;
    } finally {
      busy = false;
      render();
    }
  }

  function render() {
    const tabs = tabList();
    const keys = tabs.map((t) => t.key);
    const move = (i, by) => {
      const next = [...keys];
      [next[i], next[i + by]] = [next[i + by], next[i]];
      save(next);
    };
    list.replaceChildren(...tabs.map((t, i) => h("div", { class: "tab-order-row" },
      h("span", { class: "tab-order-name" }, t.name, t.shown ? null : h("span", { class: "muted small" }, " · nu verborgen (geen apparaten)")),
      h("button", { class: "btn-ghost", type: "button", disabled: busy || i === 0, "aria-label": `${t.name} naar links`, title: "Naar links (eerder in de rij)", onclick: () => move(i, -1) }, "↑"),
      h("button", { class: "btn-ghost", type: "button", disabled: busy || i === tabs.length - 1, "aria-label": `${t.name} naar rechts`, title: "Naar rechts (later in de rij)", onclick: () => move(i, 1) }, "↓"))));
    reset.disabled = busy || !state.tabOrder.length;
  }
  reset.addEventListener("click", () => save([]));

  render();
  const cleanup = subscribe((change) => {
    if (["snapshot", "plugins", "plugin_tabs", "tab_order"].includes(change.type) && !busy) render();
  });
  return { node, cleanup };
}

// ---------- forecast settings ----------

const TIMELINE_NAME = { electricity: "stroom en gas", water: "water", heating: "verwarming" };

// the forecast settings with the percentages (forecast.Used keys) they set
const FORECAST_ROWS = [
  { key: "period_min_pct", used: "period_min", label: "Vorige periode telt mee vanaf (% van de dagen gemeten)",
    hint: "Zonder ingevuld jaarverbruik volgt de prognose de 12 maanden vóór de afrekenperiode, als die minstens zoveel gemeten zijn. Anders is er geen prognose." },
  { key: "day_min_pct", used: "day_min", label: "Een dag telt als gemeten vanaf (% van de kwartieren)",
    hint: "De ontbrekende kwartieren van zo'n dag worden aangevuld met het gemiddelde van die dag." },
  { key: "electricity_pct", used: "usage_electricity", label: "Marge op het stroomverbruik (%)",
    hint: "Als de prognose de vorige periode volgt. Automatisch: hoe de maanden van deze periode afwijken van dezelfde maanden van de vorige periode (tussen 3 en 25%); met minder dan 2 maanden om te vergelijken de standaardwaarde." },
  { key: "gas_pct", used: "usage_gas", label: "Marge op het gasverbruik (%)", hint: "Als bij stroom." },
  { key: "water_pct", used: "usage_water", label: "Marge op het waterverbruik (%)", hint: "Als bij stroom." },
  { key: "heating_pct", used: "usage_heating", label: "Marge op het verbruik voor verwarming (%)", hint: "Als bij stroom, met de standen van ista." },
  { key: "entered_pct", used: "entered", label: "Marge op een ingevuld jaarverbruik (%)", hint: "Stroom, gas en water: als je bij het contract een verwacht jaarverbruik invult." },
  { key: "heating_entered_pct", used: "entered_heating", label: "Marge op een ingevuld verbruik voor verwarming (%)", hint: "Als je bij het ista-contract een verwacht jaarverbruik invult." },
  { key: "price_pct", used: "price", label: "Marge op verwachte marktprijzen (%)",
    hint: "Dynamisch contract: alleen over het marktprijsdeel van de kwartieren waarvan de prijs nog niet bekend is, niet over opslag en energiebelasting." },
  { key: "heating_price_pct", used: "price_heating", label: "Marge op de prijs voor verwarming (%)",
    hint: "Zolang de prijs per eenheid uit de vorige afrekening komt." },
];

/** Advanced: the percentages of the forecasts, automatic or the user's own. */
function forecastCard() {
  const body = h("div", {}, h("p", { class: "secondary" }, "Laden…"));
  const msg = h("div", { class: "form-error", role: "alert", hidden: true });
  const details = h("details", { class: "tab-order-box" }, h("summary", {}, "Percentages bekijken en aanpassen"), body, msg);
  const node = h("section", { class: "card", id: "prognose" },
    h("p", { class: "secondary" }, "De prognose van je eindafrekening (stroom, gas, water en verwarming) rekent met een paar percentages: wanneer de vorige periode meetelt, en de marge rond de verwachte uitkomst. Ze worden automatisch bepaald, uit je metingen waar dat kan. Hier zie je wat er nu gebruikt wordt en kun je een eigen waarde invullen; laat een veld leeg voor automatisch."),
    details);

  const report = (text, error) => {
    msg.textContent = text;
    msg.style.color = error ? "" : "var(--success-text)";
    msg.hidden = !text;
  };

  // what the running forecasts use for one key: per timeline the automatic value
  const nowText = (data, row) => {
    const out = [];
    for (const [tl, use] of Object.entries(data.used || {})) {
      const keys = row.used === "trend" ? Object.keys(PERCENT_LABEL).filter((k) => k.startsWith("trend_")) : [row.used];
      for (const u of use.percentages || []) {
        if (!keys.includes(u.key)) continue;
        const own = u.source === "setting";
        const value = own ? u.auto ?? u.pct : u.pct;
        const source = own ? u.auto_source || "default" : u.source;
        const label = row.used === "trend" ? `${PERCENT_LABEL[u.key]} ${TIMELINE_NAME[tl] === "stroom en gas" ? "" : `(${TIMELINE_NAME[tl]}) `}` : `${TIMELINE_NAME[tl]} `;
        out.push(`${label}${percentValue(u.key, value)} (${percentSource(source, u.months)})`);
      }
    }
    return out;
  };

  function render(data) {
    const set = data.settings || {}, def = data.defaults || {};
    const inputs = {};
    const rows = FORECAST_ROWS.map((row) => {
      const input = numberInput(set[row.key], { min: 0, max: 100, placeholder: `automatisch (${def[row.key]})`, "aria-label": row.label });
      inputs[row.key] = input;
      const now = nowText(data, row);
      const auto = now.length ? `Automatisch nu: ${now.join(", ")}.` : `Automatisch: ${percentValue(row.used, def[row.key])} (standaard).`;
      const reset = h("button", { class: "btn-ghost", type: "button", onclick: () => { input.value = ""; } }, "Automatisch");
      return h("div", { class: "forecast-row" }, field(row.label, h("div", { class: "row" }, input, reset), `${row.hint} ${auto}`));
    });
    const trend = h("select", { "aria-label": "Trend" },
      h("option", { value: "" }, "Automatisch (aan)"), h("option", { value: "off" }, "Uit"));
    trend.value = set.trend === false ? "off" : "";
    const trendNow = nowText(data, { used: "trend" });
    rows.splice(2, 0, h("div", { class: "forecast-row" }, field("Trend van deze periode", trend,
      "Gebruik je tot nu toe stelselmatig meer of minder dan in dezelfde maanden van de vorige periode (na minstens 2 maanden; bij gas minstens een maand in het stookseizoen), dan volgt de rest van de periode die trend." +
      (trendNow.length ? ` Nu berekend: ${trendNow.join(", ")}.` : " Nu nog niet berekend."))));

    const errors = Object.entries(data.used || {}).filter(([, u]) => u.error).map(([tl, u]) => h("p", { class: "hint" }, `${TIMELINE_NAME[tl][0].toUpperCase()}${TIMELINE_NAME[tl].slice(1)}: ${u.error}`));
    const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
    const all = h("button", { class: "btn", type: "button" }, "Alles automatisch");
    const form = h("form", {}, ...errors, ...rows, h("div", { class: "row form-actions" }, save, all));
    const submit = async (body, done) => {
      save.disabled = all.disabled = true;
      report("", false);
      try {
        const res = await api("PUT", "/api/settings/forecast", body);
        render(res);
        report(done, false);
      } catch (e) {
        report(e.message, true);
        save.disabled = all.disabled = false;
      }
    };
    form.addEventListener("submit", (e) => {
      e.preventDefault();
      const body = {};
      for (const [key, input] of Object.entries(inputs)) {
        if (input.value.trim() !== "") body[key] = Number(input.value);
      }
      if (trend.value === "off") body.trend = false;
      submit(body, "Opgeslagen. De prognose rekent hier vanaf nu mee.");
    });
    all.addEventListener("click", () => submit({}, "Alles staat weer op automatisch."));
    body.replaceChildren(form);
  }

  api("GET", "/api/settings/forecast").then(render).catch((e) => body.replaceChildren(h("p", { class: "form-error" }, e.message)));
  return { node, open: () => { details.open = true; node.scrollIntoView(); } };
}
