// Devices view: manage devices (add, rename, pair, enable, remove). The
// cards for importing history, price sources, remote access and gateway
// information are exported for Instellingen (settings.js).
import { state, subscribe, applyDevice } from "./app.js";
import { h, api, statusPill, ago, energy, gas, liters, dates } from "./util.js";
import { outageLine } from "./outages.js";

export function renderDevices(view) {
  const list = h("div", { class: "dev-list" });
  const input = h("input", { type: "text", placeholder: "IP-adres, bijv. 192.168.1.30", "aria-label": "IP-adres van het apparaat", inputmode: "decimal", autocomplete: "off" });
  const addBtn = h("button", { class: "btn primary", type: "submit" }, "Toevoegen");
  const addError = h("div", { class: "form-error", role: "alert", hidden: true });
  const form = h("form", { class: "card" },
        h("p", { class: "secondary" }, "Apparaten worden automatisch gevonden. Lukt dat niet (bijvoorbeeld bij een apart IoT-netwerk), voeg ze dan toe op IP-adres. Zet in de HomeWizard Energy-app per apparaat 'Lokale API' aan."),
    h("div", { class: "row" }, input, addBtn), addError);
  view.append(h("h2", { class: "section-title" }, "Apparaten"), list, h("h2", { class: "section-title" }, "Toevoegen"), form);

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    addBtn.disabled = true;
    addError.hidden = true;
    addBtn.textContent = "Zoeken…";
    try {
      applyDevice(await api("POST", "/api/devices", { host: input.value }));
      input.value = "";
    } catch (err) {
      addError.textContent = err.message;
      addError.hidden = false;
    } finally {
      addBtn.disabled = false;
      addBtn.textContent = "Toevoegen";
    }
  });

  const rows = new Map();
  const render = () => {
    const devices = [...state.devices.values()];
    if (devices.length) list.querySelector(":scope > .notice")?.remove(); // "Laden…" from before the devices came
    for (const [id, r] of rows) if (!state.devices.has(id)) { r.node.remove(); rows.delete(id); }
    for (const d of devices) {
      let r = rows.get(d.id);
      if (!r) { r = deviceRow(d); rows.set(d.id, r); }
      r.update(d);
      list.appendChild(r.node);
    }
    if (!devices.length) {
      list.replaceChildren(h("div", { class: "notice" }, state.loaded ? "Nog geen apparaten gevonden." : "Laden…"));
      rows.clear();
    }
  };
  const unsubscribe = subscribe((change) => {
    if (change.type === "device" && rows.has(change.id)) rows.get(change.id).update(state.devices.get(change.id));
    else render();
  });
  render();
  return unsubscribe;
}

function deviceRow(initial) {
  let d = initial;
  const title = h("strong");
  const pill = h("span");
  const facts = h("div", { class: "facts" });
  const msg = h("div", { class: "device-msg" });
  const error = h("div", { class: "form-error", role: "alert", hidden: true });
  const nameInput = h("input", { type: "text", maxlength: 60, "aria-label": "Naam" });
  const nameForm = h("form", { class: "name-edit", hidden: true }, nameInput,
    h("button", { class: "btn primary", type: "submit" }, "Opslaan"),
    h("button", { class: "btn", type: "button", onclick: () => { nameForm.hidden = true; titleRow.hidden = false; } }, "Annuleren"));
  const titleRow = h("div", { class: "row" }, title, pill);

  const run = async (fn) => {
    error.hidden = true;
    try { const res = await fn(); if (res && res.id) applyDevice(res); } catch (e) { error.textContent = e.message; error.hidden = false; }
  };
  const id = () => encodeURIComponent(d.id);
  const pairBtn = h("button", { class: "btn primary", type: "button", onclick: () => run(() => api("POST", `/api/devices/${id()}/pair`)) }, "Koppelen");
  const renameBtn = h("button", { class: "btn", type: "button", onclick: () => { nameInput.value = d.name; titleRow.hidden = true; nameForm.hidden = false; nameInput.focus(); } }, "Naam wijzigen");
  const identifyBtn = h("button", { class: "btn", type: "button", onclick: () => run(() => api("POST", `/api/devices/${id()}/identify`)) }, "Laat knipperen");
  const enableBtn = h("button", { class: "btn", type: "button", onclick: () => run(() => api("PATCH", `/api/devices/${id()}`, { enabled: !d.enabled })) });
  const removeBtn = h("button", { class: "btn danger", type: "button", onclick: () => {
    if (!confirm(`${d.name} verwijderen?`)) return;
    const purge = confirm("Ook de opgeslagen historie van dit apparaat wissen?\n\nOK = historie wissen, Annuleren = historie bewaren.");
    run(() => api("DELETE", `/api/devices/${id()}?purge=${purge}`));
  } }, "Verwijderen");

  nameForm.addEventListener("submit", (e) => {
    e.preventDefault();
    run(async () => {
      const res = await api("PATCH", `/api/devices/${id()}`, { name: nameInput.value });
      nameForm.hidden = true; titleRow.hidden = false;
      return res;
    });
  });

  // optional on the dashboard: the meter readings (per tariff, gas, water) below the chart
  const countersBox = h("input", { type: "checkbox" });
  const countersLabel = h("label", { class: "check", hidden: true }, countersBox, "Meterstanden tonen op het dashboard");
  countersBox.addEventListener("change", () => run(() => api("PATCH", `/api/devices/${id()}`, { show_counters: countersBox.checked })));

  // how often it did not respond; loaded again when it goes offline or comes back
  const outages = outageLine(initial.id);
  let lastStatus = initial.status;

  const node = h("article", { class: "card dev-row" },
    h("div", {}, titleRow, nameForm, facts, countersLabel, msg, outages.node, error),
    h("div", { class: "actions" }, pairBtn, renameBtn, identifyBtn, enableBtn, removeBtn));

  const update = (next) => {
    d = next;
    if (d.status !== lastStatus) {
      if (d.status === "online" || d.status === "offline") outages.refresh();
      lastStatus = d.status;
    }
    title.textContent = d.name;
    pill.replaceChildren(statusPill(d.status));
    const items = [
      d.model || d.product_type || "Onbekend type",
      d.host,
      d.api_version >= 2 ? (d.paired ? "API v2 · gekoppeld" : "API v2") : (d.v2_available ? "API v1 · v2 beschikbaar" : "API v1"),
      d.firmware ? `firmware ${d.firmware}` : null,
      d.serial ? `serienummer ${d.serial}` : null,
      d.source === "mdns" ? "automatisch gevonden" : "handmatig toegevoegd",
      d.last_seen ? `gezien ${ago(d.last_seen)}` : null,
    ].filter(Boolean);
    facts.replaceChildren(...items.map((t) => h("span", {}, t)));
    msg.textContent = d.message || "";
    msg.hidden = !msg.textContent;
    // Pairing (API v2) is only required when the device cannot be read
    // without it (e.g. the Plug-In Battery). Devices that also offer v2,
    // like the P1 meter, work fully over API v1; for them it is optional.
    const required = d.status === "needs_pairing";
    pairBtn.hidden = !d.can_pair || d.status === "pairing";
    pairBtn.className = required ? "btn primary" : "btn";
    pairBtn.textContent = required ? "Koppelen" : "Koppelen (optioneel)";
    pairBtn.title = required ? "" : "Dit apparaat werkt zonder koppeling via API v1. Koppelen (API v2, HTTPS met token) is niet nodig.";
    enableBtn.textContent = d.enabled ? "Uitschakelen" : "Inschakelen";
    countersLabel.hidden = !["p1", "water", "kwh", "socket", "battery"].includes(d.kind);
    countersBox.checked = !!d.show_counters;
    countersLabel.lastChild.textContent = d.kind === "p1" ? "Meterstanden tonen op het dashboard (per tarief en gas)" : "Meterstand tonen op het dashboard";
    identifyBtn.disabled = d.status !== "online";
  };
  update(initial);
  return { node, update };
}

/** The "Gateway" card: version and settings of the running gateway; follows the connection. */
export function gatewayCard() {
  const node = h("section", { class: "card" });
  renderInfo(node);
  const unsubscribe = subscribe((change) => { if (change.type === "snapshot" || change.type === "conn") renderInfo(node); });
  return { node, cleanup: unsubscribe };
}

function renderInfo(node) {
  const i = state.info;
  if (!i) { node.replaceChildren(h("p", { class: "secondary" }, "Geen verbinding met de gateway.")); return; }
  const tls = { off: "uit (http)", acme: "Let's Encrypt", selfsigned: "eigen certificaat" }[i.tls] || i.tls;
  const rows = [
    ["Versie", i.version],
    ["Uitleesinterval", `${i.interval_ms / 1000} seconden`],
    ["Automatisch zoeken", i.discovery ? "aan (mDNS)" : "uit"],
    ["HTTPS", tls],
    ["Tijdzone", i.time_zone],
  ];
  const children = [h("dl", { class: "info-list" }, ...rows.flatMap(([k, v]) => [h("dt", {}, k), h("dd", {}, v)]))];
  if (i.ca_download) {
    children.push(h("p", { class: "secondary" }, "Installeer het certificaat van de gateway één keer op elke telefoon en pc, zodat de browser de verbinding vertrouwt: ",
      h("a", { href: "/ca.crt", download: "homewizardlocal-ca.crt" }, "certificaat downloaden"), "."));
  }
  if (!window.isSecureContext) {
    children.push(h("p", { class: "secondary" }, "Je gebruikt de app via http. Installeren als app op je telefoon werkt pas via HTTPS; zie de README voor het instellen van je domein."));
  }
  children.push(h("p", { class: "secondary" }, ABOUT));
  node.replaceChildren(...children);
}

/** Where the data comes from; HomeWizard is a trademark of its owner, not of this app. */
const ABOUT = "Home Energy Watch werkt met HomeWizard Energy-apparaten (P1-meter, Energy Sockets, kWh-meters, watermeter en Plug-In Battery) via hun lokale API. Deze app is niet verbonden aan of goedgekeurd door HomeWizard.";

/** ENTSO-E token: can be set or removed, never shown (the gateway does not send it back). */
export function entsoeCard() {
  const status = h("p", { class: "secondary" }, "Laden…");
  const input = h("input", { type: "password", placeholder: "ENTSO-E API-token", "aria-label": "ENTSO-E API-token", autocomplete: "off", spellcheck: "false" });
  const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
  const remove = h("button", { class: "btn danger", type: "button", hidden: true }, "Token verwijderen");
  const message = h("div", { class: "form-error", role: "alert", hidden: true });
  const form = h("form", { class: "card" },
    h("p", { class: "secondary" }, "Stroomprijzen komen van EnergyZero en, als reserve, Nord Pool; daarvoor is geen account nodig. ENTSO-E is een officiële, gratis extra reservebron. Vraag een token aan via transparency.entsoe.eu (registreren en dan via de helpdesk API-toegang aanvragen)."),
    status, h("div", { class: "row" }, input, save, remove), message);

  const show = (st) => {
    const parts = [st.configured ? "Er is een token ingesteld." : "Geen token ingesteld; ENTSO-E wordt overgeslagen."];
    if (st.configured && !st.used) parts.push("ENTSO-E staat niet in HWLOCAL_PRICE_SOURCES en wordt dus niet gebruikt.");
    if (st.from_env) parts.push("Het token komt uit de omgevingsvariabele ENTSOE_API_TOKEN en kan hier niet worden gewijzigd.");
    status.textContent = parts.join(" ");
    input.disabled = save.disabled = st.from_env;
    remove.hidden = !st.configured || st.from_env;
  };
  const report = (text, error) => {
    message.textContent = text;
    message.style.color = error ? "" : "var(--success-text)";
    message.hidden = !text;
  };
  const submit = async (token) => {
    save.disabled = remove.disabled = true;
    report("", false);
    if (token) save.textContent = "Controleren…";
    try {
      const st = await api("PUT", "/api/settings/entsoe", { token });
      input.value = "";
      show(st);
      report(st.warning || (token ? "Token opgeslagen en in gebruik." : "Token verwijderd."), false);
    } catch (err) {
      report(err.message, true);
      save.disabled = false;
    } finally {
      save.textContent = "Opslaan";
      remove.disabled = false;
    }
  };
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    if (input.value.trim()) submit(input.value.trim());
  });
  remove.addEventListener("click", () => {
    if (confirm("ENTSO-E-token verwijderen?")) submit("");
  });
  api("GET", "/api/settings/entsoe").then(show).catch(() => { status.textContent = "Instelling niet beschikbaar."; });
  return form;
}

/** Remote access: Tailscale on/off and extra host names, applied at once by the gateway. */
export function accessCard() {
  const toggle = h("input", { type: "checkbox", role: "switch", "aria-label": "Toegang via Tailscale" });
  const status = h("p", { class: "secondary" }, "Laden…");
  const hosts = h("input", { type: "text", placeholder: "bijv. pc.tail1234.ts.net", "aria-label": "Extra hostnamen, kommagescheiden", autocomplete: "off", spellcheck: "false" });
  const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
  const message = h("div", { class: "form-error", role: "alert", hidden: true });
  const form = h("form", { class: "card" },
    h("p", { class: "secondary" }, "Met Tailscale open je de app ook buiten huis, zonder een poort in je router open te zetten. Installeer Tailscale op deze computer en op je telefoon of laptop en zet de schakelaar aan. Tailscale-adressen (100.64.0.0/10) worden ook door sommige providers gebruikt; daarom staat dit standaard uit."),
    h("label", { class: "switch" }, toggle, h("span", {}, "Toegang via Tailscale")),
    status,
    h("p", { class: "secondary" }, "Open je de app via een naam met punten, zoals de MagicDNS-naam (computernaam.tailnet.ts.net), zet die dan hieronder. De korte computernaam en IP-adressen werken altijd."),
    h("div", { class: "row" }, hosts, save), message);

  let current = null;
  const show = (st, keepInput) => {
    current = st;
    toggle.checked = st.tailscale;
    const parts = [];
    if (st.tailscale_from_option) parts.push("Staat aan via de opstartoptie --allow-tailscale (HWLOCAL_ALLOW_TAILSCALE) en kan hier niet uit.");
    if (st.tailscale && st.addresses.length) parts.push(`Bereikbaar via ${st.addresses.map((a) => `http://${a}:${location.port || (location.protocol === "https:" ? 443 : 80)}`).join(", ")}.`);
    else if (st.tailscale) parts.push("Op deze computer is geen Tailscale-adres gevonden; staat Tailscale aan?");
    if (st.via_tailscale) parts.push("Je bent nu via Tailscale verbonden: je kunt toegang hier alleen uitzetten. Aanzetten en namen toevoegen kan vanaf het lokale netwerk.");
    if (st.option_hosts.length) parts.push(`Ook toegestaan via de opstartopties of computernaam: ${st.option_hosts.join(", ")}.`);
    status.textContent = parts.join(" ");
    toggle.disabled = st.tailscale_from_option || (st.via_tailscale && !st.tailscale);
    if (!keepInput) hosts.value = st.hosts.join(", ");
  };
  const report = (text, error) => {
    message.textContent = text;
    message.style.color = error ? "" : "var(--success-text)";
    message.hidden = !text;
  };
  const submit = async (tailscale, names, done) => {
    toggle.disabled = save.disabled = true;
    report("", false);
    try {
      show(await api("PUT", "/api/settings/access", { tailscale, hosts: names }));
      report(done, false);
    } catch (err) {
      show(current, true); // keep what was typed
      report(err.message, true);
    } finally {
      save.disabled = false;
    }
  };
  const names = () => hosts.value.split(/[\s,]+/).filter(Boolean);
  toggle.addEventListener("change", () => {
    if (!toggle.checked && current.via_tailscale && !confirm("Je bent via Tailscale verbonden. Na het uitzetten kom je er alleen nog vanaf het lokale netwerk bij. Doorgaan?")) {
      toggle.checked = true;
      return;
    }
    submit(toggle.checked, current.hosts, toggle.checked ? "Tailscale-toegang staat aan." : "Tailscale-toegang staat uit.");
  });
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    submit(current.setting, names(), "Hostnamen opgeslagen.");
  });
  api("GET", "/api/settings/access").then(show).catch(() => { status.textContent = "Instelling niet beschikbaar."; toggle.disabled = save.disabled = true; });
  // Tailscale Serve adds the MagicDNS name to the host names
  const reload = () => {
    if (!form.isConnected) return window.removeEventListener("hw:access", reload); // the view was left
    api("GET", "/api/settings/access").then((st) => show(st)).catch(() => {});
  };
  window.addEventListener("hw:access", reload);
  return form;
}

/** Text with its https:// addresses as links (Tailscale's messages name the page to enable Serve). */
function linkified(text) {
  const p = h("span");
  for (const part of String(text).split(/(https:\/\/[^\s"'<>]+)/)) {
    p.append(/^https:\/\//.test(part) ? h("a", { href: part, target: "_blank", rel: "noopener" }, part) : part);
  }
  return p;
}

/** Tailscale Serve: the gateway sets up `tailscale serve` on an HTTPS port chosen here, after checking it is free. */
export function tailscaleServeCard() {
  const status = h("p", { class: "secondary" }, "Laden…");
  const port = h("input", { type: "number", min: 1, max: 65535, step: 1, value: 443, inputmode: "numeric", "aria-label": "Poort voor Tailscale Serve", style: "max-width:8em" });
  const save = h("button", { class: "btn primary", type: "submit" }, "Instellen");
  const off = h("button", { class: "btn", type: "button", hidden: true }, "Uitzetten");
  const used = h("p", { class: "secondary", hidden: true });
  const message = h("div", { class: "form-error", role: "alert", hidden: true });
  const form = h("form", { class: "card" },
    h("p", { class: "secondary" }, "Met Tailscale Serve krijgt de app een https-adres in je tailnet, nodig om de app te installeren en voor meldingen. Alleen apparaten in je tailnet kunnen het openen. Kies de poort waarop Tailscale de app aanbiedt: 443 geeft een adres zonder poortnummer; gebruikt een andere app 443 al, kies dan bijvoorbeeld 8443."),
    status,
    h("div", { class: "row" }, port, save, off),
    used, message);

  let current = null;
  const report = (text, error) => {
    message.replaceChildren(linkified(text));
    message.style.color = error ? "" : "var(--success-text)";
    message.hidden = !text;
  };
  const show = (st, keepInput) => {
    current = st;
    status.replaceChildren();
    const ready = st.available && st.https && !st.via_tailscale;
    // what stops Tailscale Serve from being set up goes in the red message, not the grey text
    const problem = !st.available ? (st.problem || "Tailscale is niet beschikbaar.")
      : !st.https ? "HTTPS-certificaten staan uit in je tailnet: zet in de Tailscale-beheerconsole onder DNS MagicDNS en HTTPS Certificates aan." : "";
    report(problem, true);
    if (!st.available) {
      status.append("Tailscale Serve kan nu niet vanuit de app worden ingesteld.");
    } else if (st.ports.length) {
      status.append("Bereikbaar via ");
      st.urls.forEach((u, i) => status.append(i ? ", " : "", h("a", { href: u, target: "_blank", rel: "noopener" }, u)));
      status.append(` (Tailscale stuurt door naar ${st.target}).`);
    } else {
      status.append(`Staat uit. Tailscale-naam van deze computer: ${st.dns_name || "onbekend"}.`);
    }
    if (st.via_tailscale) status.append(" Je bent via Tailscale verbonden: instellen kan alleen vanaf het lokale netwerk; uitzetten kan wel.");
    const others = (st.used || []).map((u) => `${u.port} (${[u.proto, ...u.targets].join(" ")}${u.funnel ? ", Funnel" : ""})`);
    used.textContent = others.length ? `Al in gebruik in Tailscale Serve door andere apps: ${others.join("; ")}.` : "";
    used.hidden = !others.length;
    if (!keepInput) port.value = st.ports[0] || (st.used.some((u) => u.port === 443) ? 8443 : 443);
    save.textContent = st.ports.length ? "Poort wijzigen" : "Instellen";
    save.disabled = port.disabled = !ready;
    off.hidden = !st.ports.length;
    off.disabled = false;
  };
  const call = async (method, body, done) => {
    save.disabled = off.disabled = true;
    report("", false);
    try {
      show(await api(method, "/api/settings/tailscale-serve", body));
      report(done, false);
      if (method === "PUT") window.dispatchEvent(new Event("hw:access"));
    } catch (err) {
      if (current) show(current, true); // keep the typed port
      report(err.message, true);
    }
  };
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const p = Number(port.value);
    if (!Number.isInteger(p) || p < 1 || p > 65535) {
      report("Kies een poort van 1 tot en met 65535.", true);
      return;
    }
    status.textContent = "Tailscale instellen…";
    call("PUT", { port: p }, `Tailscale Serve staat aan op poort ${p}.`);
  });
  off.addEventListener("click", () => {
    if (current && current.urls.includes(location.origin) &&
      !confirm("Je gebruikt dit adres nu. Na het uitzetten werkt het niet meer. Doorgaan?")) return;
    call("DELETE", undefined, "Tailscale Serve staat uit.");
  });
  api("GET", "/api/settings/tailscale-serve").then((st) => show(st)).catch(() => { form.hidden = true; });
  return form;
}

const KIND_LABEL = { electricity: "Stroom (P1)", gas: "Gas (P1)", water: "Water", meter: "Stroom (socket/kWh-meter)" };

/** Uploads one CSV; device is optional. A 409 (choose the device) is returned, not thrown. */
function uploadCSV(file, dryRun, device) {
  const q = new URLSearchParams({ dry_run: dryRun });
  if (device) q.set("device", device);
  return fetch(`/api/import?${q}`, { method: "POST", headers: { "Content-Type": "text/csv" }, body: file })
    .then(async (res) => {
      const data = await res.json().catch(() => null);
      if (res.status === 409 && data && data.candidates) return { choose: true, ...data };
      if (!res.ok) throw new Error((data && data.error) || `Fout ${res.status}`);
      return data;
    });
}

function unattributedText(kind, u) {
  if (!u) return "";
  const parts = [];
  if (u.import_kwh) parts.push(energy(u.import_kwh).join(" "));
  if (u.export_kwh) parts.push(`${energy(u.export_kwh).join(" ")} terug`);
  if (u.gas_m3) parts.push(gas(u.gas_m3).join(" "));
  if (u.water_m3) parts.push(liters(u.water_m3 * 1000).join(" "));
  return parts.join(", ");
}

// Import of CSV exports from the HomeWizard Energy app: preview first, then import.
// Socket and kWh-meter exports do not say which device they came from: the
// gateway matches them on the meter reading, and with several candidates a
// device can be chosen per file.
export function importCard() {
  const input = h("input", { type: "file", accept: ".csv,text/csv", multiple: true, "aria-label": "CSV-bestanden" });
  const preview = h("div", { class: "table-wrap" });
  const status = h("div", { class: "device-msg", role: "status" });
  const go = h("button", { class: "btn primary", type: "button", hidden: true }, "Importeren");
  /** One entry per file: { f, r (response), e (error), device (chosen id) }. */
  let items = [];
  /** Quarters a file adds or replaces (a partly measured quarter). */
  const gain = (r) => r.added + (r.replaced || 0);

  const check = async (item) => {
    try {
      item.r = await uploadCSV(item.f, true, item.device);
      item.e = null;
      if (!item.r.choose) item.device = item.r.device.id;
    } catch (e) { item.r = null; item.e = e; }
  };
  const refresh = () => {
    renderPreview();
    const ready = items.filter((x) => x.r && !x.r.choose && gain(x.r) > 0);
    const added = ready.reduce((n, x) => n + gain(x.r), 0);
    const open = items.some((x) => x.r && x.r.choose);
    status.textContent = open
      ? "Kies bij de gemarkeerde bestanden het apparaat waar ze bij horen."
      : added
        ? "Controleer het overzicht. Volledige metingen worden nooit overschreven, alleen kwartieren die de gateway maar deels heeft gemeten; opnieuw importeren telt niets dubbel."
        : "Er is niets nieuws om te importeren.";
    go.textContent = `Importeren (${added.toLocaleString("nl-NL")} kwartieren)`;
    go.hidden = !added || open;
  };

  input.addEventListener("change", async () => {
    items = [...input.files].map((f) => ({ f }));
    go.hidden = true;
    preview.replaceChildren();
    if (!items.length) return;
    status.textContent = "Bestanden controleren…";
    await Promise.all(items.map(check));
    refresh();
  });

  go.addEventListener("click", async () => {
    go.disabled = true;
    status.textContent = "Importeren…";
    for (const item of items.filter((x) => x.r && !x.r.choose && gain(x.r) > 0)) {
      try { item.r = await uploadCSV(item.f, false, item.device); item.done = true; } catch (e) { item.e = e; item.r = null; }
    }
    renderPreview(true);
    const added = items.reduce((n, x) => n + (x.done && x.r ? gain(x.r) : 0), 0);
    status.textContent = `Klaar: ${added.toLocaleString("nl-NL")} kwartieren aangevuld. Je vindt ze onder Verbruik en Kosten.`;
    go.hidden = true;
    go.disabled = false;
    input.value = "";
  });

  /** Device cell: a choice when several devices fit the file. */
  function deviceCell(item, done) {
    const r = item.r;
    const cands = r.candidates || [];
    if (done || cands.length < 2) return r.device ? r.device.name : "–";
    const select = h("select", { "aria-label": `Apparaat voor ${item.f.name}` },
      r.choose ? h("option", { value: "", selected: true, disabled: true }, "Kies…") : null,
      ...cands.map((c) => h("option", { value: c.id, selected: !r.choose && c.id === r.device.id }, c.name)));
    select.addEventListener("change", async () => {
      item.device = select.value;
      select.disabled = true;
      await check(item);
      refresh();
    });
    const hint = r.choose ? "tellerstand past bij geen enkel apparaat eenduidig" : r.match === "counter" ? "gekozen op tellerstand" : "";
    return h("div", {}, select, hint ? h("div", { class: "hint" }, hint) : null);
  }

  function renderPreview(done = false) {
    const head = ["Bestand", "Soort", "Apparaat", "Periode", done ? "Aangevuld" : "Nieuwe kwartieren", "Al aanwezig", "Onderbroken", "Niet toe te rekenen"];
    const rows = items.map((item) => {
      const { f, r, e } = item;
      if (e) return [f.name, "–", "–", e.message, "", "", "", ""];
      const file = r.file;
      const period = file.first_data ? `${dates.day(new Date(file.first_data))} – ${dates.day(new Date(file.last_data))}` : "geen meetgegevens";
      if (r.choose) return [f.name, KIND_LABEL[file.kind] || file.kind, deviceCell(item, done), period, "–", "–", "–", "–"];
      const gaps = (file.gaps || 0) + (file.resets || 0);
      return [f.name, KIND_LABEL[file.kind] || file.kind, deviceCell(item, done), period,
        r.added.toLocaleString("nl-NL") + (r.replaced ? ` + ${r.replaced.toLocaleString("nl-NL")} vervangen` : ""), r.skipped.toLocaleString("nl-NL"),
        gaps ? `${gaps}×${file.glitches ? ` (${file.glitches} lege standen)` : ""}` : "–",
        unattributedText(file.kind, file.unattributed) || "–"];
    });
    preview.replaceChildren(h("table", { class: "data" },
      h("thead", {}, h("tr", {}, ...head.map((t) => h("th", { scope: "col" }, t)))),
      h("tbody", {}, ...rows.map((r) => h("tr", {}, ...r.map((c) => h("td", {}, c)))))));
  }

  return h("section", { class: "card" },
    h("p", { class: "secondary" }, "Voeg je bestaande historie toe met de CSV-export uit de HomeWizard Energy-app (per kwartier): stroom en gas van de P1-meter, de watermeter en stroom van Energy Sockets en kWh-meters. Je kunt meerdere bestanden tegelijk kiezen; bij sockets en kWh-meters kiest de gateway het apparaat op de tellerstand."),
    h("div", { class: "row" }, input, go), status, preview,
    h("p", { class: "muted" }, "Tijdens een onderbreking in het bestand is wel verbruik gemeten, maar niet wanneer precies; dat deel wordt niet over kwartieren verdeeld en staat hierboven als 'niet toe te rekenen'."));
}
