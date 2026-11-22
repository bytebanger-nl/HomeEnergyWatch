// Rapporten: a PDF report over a period (download), reports mailed every
// month or week, and the e-mail settings they need. The cards live under
// Instellingen; "Verbruik en Kosten" downloads the period it shows.
import { h, api } from "./util.js";

const iso = (d) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
const addDays = (d, n) => { const x = new Date(d); x.setDate(x.getDate() + n); return x; };
// what a scheduled report covers besides the month or week itself
const RANGES = { contract_year: "contractjaar tot nu", year: "kalenderjaar tot nu" };
const dateTime = new Intl.DateTimeFormat("nl-NL", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

/**
 * Downloads the PDF report of [from, to) (Date objects at midnight), or
 * with range "contract_year" / "year" from the start of the contract year
 * or 1 January up to today. sections: keys, or empty for all. Throws with
 * the gateway's message.
 */
export async function downloadReport({ from, to, range = "", sections = [], title = "" }) {
  const q = new URLSearchParams(range ? { range } : { from: iso(from), to: iso(addDays(to, -1)) });
  if (sections.length) q.set("sections", sections.join(","));
  if (title) q.set("title", title);
  const res = await fetch(`/api/reports/pdf?${q}`, { credentials: "same-origin" });
  if (!res.ok) {
    let msg = `Rapport niet gemaakt (${res.status})`;
    try { msg = (await res.json()).error || msg; } catch { /* not JSON */ }
    if (res.status === 401) window.dispatchEvent(new CustomEvent("hw:login"));
    throw new Error(msg);
  }
  const blob = await res.blob();
  const name = /filename="([^"]+)"/.exec(res.headers.get("Content-Disposition") || "")?.[1] || "rapport.pdf";
  const url = URL.createObjectURL(blob);
  const a = h("a", { href: url, download: name, hidden: true });
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 60_000);
}

/** The periods offered for a report made by hand. */
function presets() {
  const now = new Date();
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const monday = addDays(today, -((today.getDay() + 6) % 7));
  return [
    ["prev-month", "Vorige maand", () => { const to = new Date(now.getFullYear(), now.getMonth(), 1); return [new Date(to.getFullYear(), to.getMonth() - 1, 1), to]; }],
    ["month", "Deze maand tot nu", () => [new Date(now.getFullYear(), now.getMonth(), 1), addDays(today, 1)]],
    ["prev-week", "Vorige week", () => [addDays(monday, -7), monday]],
    ["contract-year", "Contractjaar tot nu", () => ({ range: "contract_year" })],
    ["year", "Dit jaar tot nu", () => [new Date(now.getFullYear(), 0, 1), addDays(today, 1)]],
    ["prev-year", "Vorig jaar", () => [new Date(now.getFullYear() - 1, 0, 1), new Date(now.getFullYear(), 0, 1)]],
    ["custom", "Eigen periode", null],
  ];
}

/** Checkboxes for the sections; returns the node and a getter of the chosen keys. */
function sectionPicker(sections, chosen) {
  const boxes = sections.map((s) => {
    const box = h("input", { type: "checkbox", value: s.key, checked: !chosen || chosen.includes(s.key) });
    return { key: s.key, box, node: h("label", { class: "check report-section", title: s.hint }, box, h("span", {}, s.label), h("span", { class: "secondary small" }, s.hint)) };
  });
  const node = h("fieldset", { class: "report-sections" }, h("legend", {}, "Onderdelen"), ...boxes.map((b) => b.node));
  return { node, get: () => boxes.filter((b) => b.box.checked).map((b) => b.key) };
}

/** Instellingen › Rapporten: make one now, and the scheduled ones. */
export function reportsCard() {
  const node = h("div", { class: "reports" });
  const status = h("p", { class: "secondary" }, "Laden…");
  node.append(status);
  let info = null;

  async function load() {
    try {
      info = await api("GET", "/api/reports");
      render();
    } catch (e) {
      status.textContent = e.message;
    }
  }

  function render() {
    node.replaceChildren(makeCard(), scheduleCard());
  }

  // ---- a report now ----
  function makeCard() {
    const list = presets();
    const select = h("select", { class: "select", "aria-label": "Periode" }, ...list.map(([id, label]) => h("option", { value: id }, label)));
    const now = new Date();
    const fromIn = h("input", { type: "date", "aria-label": "Eerste dag", value: iso(new Date(now.getFullYear(), now.getMonth() - 1, 1)) });
    const toIn = h("input", { type: "date", "aria-label": "Laatste dag", value: iso(addDays(new Date(now.getFullYear(), now.getMonth(), 1), -1)) });
    const custom = h("div", { class: "row", hidden: true }, h("label", {}, "Van ", fromIn), h("label", {}, "t/m ", toIn));
    select.addEventListener("change", () => { custom.hidden = select.value !== "custom"; });
    const title = h("input", { type: "text", maxlength: "80", placeholder: "Titel (standaard: Energierapport)", "aria-label": "Titel" });
    const picker = sectionPicker(info.sections);
    const go = h("button", { class: "btn primary", type: "submit" }, "PDF downloaden");
    const msg = h("div", { class: "form-error", role: "alert", hidden: true });
    const form = h("form", { class: "card" },
      h("h3", {}, "Rapport maken"),
      h("p", { class: "secondary" }, "Een PDF met grafieken en tabellen over een periode: verbruik, kosten, voorschot en wat er verder gemeten wordt. Onderdelen zonder metingen vallen vanzelf weg."),
      h("div", { class: "row" }, select, custom), picker.node, h("div", { class: "row" }, title, go), msg);
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      msg.hidden = true;
      let from, to, range = "";
      if (select.value === "custom") {
        if (!fromIn.value || !toIn.value) return;
        const [fy, fm, fd] = fromIn.value.split("-").map(Number);
        const [ty, tm, td] = toIn.value.split("-").map(Number);
        from = new Date(fy, fm - 1, fd);
        to = addDays(new Date(ty, tm - 1, td), 1);
      } else {
        const p = list.find(([id]) => id === select.value)[2]();
        if (Array.isArray(p)) [from, to] = p;
        else range = p.range;
      }
      const sections = picker.get();
      if (!sections.length) { msg.textContent = "Kies minstens één onderdeel."; msg.hidden = false; return; }
      go.disabled = true;
      go.textContent = "Maken…";
      try {
        await downloadReport({ from, to, range, sections: sections.length === info.sections.length ? [] : sections, title: title.value.trim() });
      } catch (err) {
        msg.textContent = err.message;
        msg.hidden = false;
      } finally {
        go.disabled = false;
        go.textContent = "PDF downloaden";
      }
    });
    return form;
  }

  // ---- scheduled reports ----
  function scheduleCard() {
    const card = h("section", { class: "card" }, h("h3", {}, "Automatisch mailen"));
    if (!info.mail_configured) {
      card.append(h("p", { class: "notice-inline" }, "Stel eerst hieronder bij E-mail een mailserver in; daarna kun je rapporten laten versturen."));
    }
    card.append(h("p", { class: "secondary" }, "Een maandrapport gaat op de 2e van de maand om 08:00 over de vorige maand, een weekrapport op maandag om 08:00 over de vorige week. Dan zijn de metingen en prijzen van de laatste dag compleet. Met ‘Contractjaar tot nu’ loopt het rapport van het begin van je contractjaar tot het einde van die maand of week. Staat Verwarming erin en heeft ista de periode nog niet helemaal gemeten, dan gaat het rapport op tijd zonder verwarming; het overzicht verwarming volgt in een aparte mail zodra ista alles gemeten heeft, hooguit 7 dagen later. Een rapport met alleen verwarming wacht tot dan. Een nieuw rapport begint bij de volgende periode; met ‘Nu versturen’ krijg je de laatste meteen."));
    const list = h("div", { class: "schedule-list" });
    for (const sc of info.schedules) list.append(scheduleRow(sc));
    if (!info.schedules.length) list.append(h("p", { class: "secondary" }, "Nog geen geplande rapporten."));
    const add = h("button", { class: "btn", type: "button", onclick: () => { add.hidden = true; card.append(scheduleForm(null, () => { add.hidden = false; })); } }, "Gepland rapport toevoegen");
    card.append(list, h("div", { class: "row" }, add));
    return card;
  }

  function scheduleRow(sc) {
    const kind = (sc.kind === "week" ? "Elke week" : "Elke maand") + (RANGES[sc.range || ""] ? `, ${RANGES[sc.range || ""]}` : "");
    const state = [];
    if (!sc.enabled) state.push("staat uit");
    else if (sc.wait_until) state.push(`wacht tot ista de periode gemeten heeft, uiterlijk ${dateTime.format(new Date(sc.wait_until))}`);
    else if (sc.next_at) state.push(`volgende: ${dateTime.format(new Date(sc.next_at))}`);
    if (sc.enabled && sc.heating_period) state.push(`verwarming volgt zodra ista de periode gemeten heeft, uiterlijk ${dateTime.format(new Date(sc.heating_until))}`);
    if (sc.heating_error) state.push(`verwarming niet verstuurd (${dateTime.format(new Date(sc.heating_at))}): ${sc.heating_error}`);
    if (sc.last_error) state.push(`laatste poging mislukt (${dateTime.format(new Date(sc.last_at))}): ${sc.last_error}`);
    else if (sc.last_at) state.push(`laatst verstuurd: ${dateTime.format(new Date(sc.last_at))}`);
    const msg = h("div", { class: "form-error", role: "alert", hidden: true });
    const row = h("div", { class: "schedule-row" },
      h("div", { class: "schedule-head" },
        h("strong", {}, sc.name), h("span", { class: "secondary" }, ` · ${kind} · aan ${sc.to.join(", ")}`)),
      h("div", { class: sc.last_error || sc.heating_error ? "small form-error" : "secondary small" }, state.join(" · ")));
    const toggle = h("button", { class: "btn-ghost", type: "button" }, sc.enabled ? "Uitzetten" : "Aanzetten");
    const edit = h("button", { class: "btn-ghost", type: "button" }, "Bewerken");
    const send = h("button", { class: "btn-ghost", type: "button", disabled: !info.mail_configured }, "Nu versturen");
    const del = h("button", { class: "btn-ghost danger", type: "button" }, "Verwijderen");
    const fail = (e) => { msg.textContent = e.message; msg.style.color = ""; msg.hidden = false; };
    toggle.addEventListener("click", async () => {
      try { await api("PUT", `/api/reports/schedules/${sc.id}`, { ...sc, enabled: !sc.enabled }); load(); } catch (e) { fail(e); }
    });
    edit.addEventListener("click", () => { row.replaceWith(scheduleForm(sc, load)); });
    send.addEventListener("click", async () => {
      send.disabled = true;
      send.textContent = "Versturen…";
      try {
        await api("POST", `/api/reports/schedules/${sc.id}/send`);
        await load();
      } catch (e) {
        fail(e);
        send.disabled = false;
        send.textContent = "Nu versturen";
      }
    });
    del.addEventListener("click", async () => {
      if (!confirm(`Gepland rapport ‘${sc.name}’ verwijderen?`)) return;
      try { await api("DELETE", `/api/reports/schedules/${sc.id}`); load(); } catch (e) { fail(e); }
    });
    row.append(h("div", { class: "row" }, toggle, edit, send, del), msg);
    return row;
  }

  function scheduleForm(sc, onClose) {
    const name = h("input", { type: "text", maxlength: "60", value: sc?.name || "", placeholder: "Naam, bijv. Maandrapport", "aria-label": "Naam" });
    const kind = h("select", { class: "select", "aria-label": "Hoe vaak" },
      h("option", { value: "month", selected: !sc || sc.kind === "month" }, "Elke maand (2e, 08:00)"),
      h("option", { value: "week", selected: sc?.kind === "week" }, "Elke week (maandag, 08:00)"));
    const range = h("select", { class: "select", "aria-label": "Wat het rapport laat zien" },
      ...Object.entries({ "": "Over die maand of week", ...RANGES }).map(([v, label]) => h("option", { value: v, selected: (sc?.range || "") === v }, label[0].toUpperCase() + label.slice(1))));
    const to = h("input", { type: "text", value: sc?.to.join(", ") || "", placeholder: "naam@voorbeeld.nl, …", "aria-label": "Ontvangers", autocomplete: "off", spellcheck: "false" });
    const title = h("input", { type: "text", maxlength: "80", value: sc?.title || "", placeholder: "Titel (standaard: Energierapport)", "aria-label": "Titel" });
    const enabled = h("input", { type: "checkbox", checked: !sc || sc.enabled });
    const picker = sectionPicker(info.sections, sc?.sections);
    const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
    const cancel = h("button", { class: "btn", type: "button" }, "Annuleren");
    const msg = h("div", { class: "form-error", role: "alert", hidden: true });
    const form = h("form", { class: "schedule-form" },
      h("div", { class: "row" }, name, kind, range),
      h("label", { class: "field" }, h("span", { class: "secondary small" }, `Ontvangers (komma's ertussen, hoogstens ${info.max_recipients})`), to),
      title, picker.node,
      h("label", { class: "switch" }, enabled, h("span", {}, "Aan")),
      h("div", { class: "row" }, save, cancel), msg);
    cancel.addEventListener("click", () => { form.remove(); onClose?.(); if (sc) render(); });
    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      msg.hidden = true;
      const body = {
        name: name.value.trim(), kind: kind.value, range: range.value, title: title.value.trim(), enabled: enabled.checked,
        to: to.value.split(/[,;\s]+/).filter(Boolean), sections: picker.get(),
      };
      save.disabled = true;
      try {
        await api(sc ? "PUT" : "POST", sc ? `/api/reports/schedules/${sc.id}` : "/api/reports/schedules", body);
        await load();
      } catch (err) {
        msg.textContent = err.message;
        msg.hidden = false;
        save.disabled = false;
      }
    });
    return form;
  }

  load();
  return { node, reload: load };
}

/** Instellingen › E-mail: the SMTP server reports are sent through. */
export function mailCard(onChange) {
  const host = h("input", { type: "text", placeholder: "smtp.voorbeeld.nl", "aria-label": "Server", autocomplete: "off", spellcheck: "false" });
  const security = h("select", { class: "select", "aria-label": "Beveiliging" },
    h("option", { value: "starttls" }, "STARTTLS (poort 587)"),
    h("option", { value: "tls" }, "TLS (poort 465)"),
    h("option", { value: "none" }, "Geen (alleen in je eigen netwerk)"));
  const port = h("input", { type: "number", min: "1", max: "65535", placeholder: "587", "aria-label": "Poort", class: "port" });
  const user = h("input", { type: "text", placeholder: "Gebruikersnaam (vaak je e-mailadres)", "aria-label": "Gebruikersnaam", autocomplete: "off", spellcheck: "false" });
  const pass = h("input", { type: "password", placeholder: "Wachtwoord", "aria-label": "Wachtwoord", autocomplete: "new-password" });
  const from = h("input", { type: "text", placeholder: "Afzender, bijv. Energie <thuis@voorbeeld.nl>", "aria-label": "Afzender", autocomplete: "off", spellcheck: "false" });
  const save = h("button", { class: "btn primary", type: "submit" }, "Opslaan");
  const off = h("button", { class: "btn danger", type: "button", hidden: true }, "E-mail uitzetten");
  const status = h("p", { class: "secondary" }, "Laden…");
  const msg = h("div", { class: "form-error", role: "alert", hidden: true });
  const testTo = h("input", { type: "email", placeholder: "Testmail naar…", "aria-label": "Testmail naar", autocomplete: "email" });
  const test = h("button", { class: "btn", type: "button" }, "Testmail sturen");
  const testRow = h("div", { class: "row", hidden: true }, testTo, test);
  const form = h("form", { class: "card" },
    h("p", { class: "secondary" }, "Rapporten gaan via de mailserver van je provider of je eigen domein, of via een dienst als Brevo of SMTP2GO. Bij Gmail gebruik je een app-wachtwoord (daarvoor moet tweestapsverificatie aan staan). Outlook.com en Microsoft 365 staan inloggen met een wachtwoord niet meer toe en werken daarom niet. Het wachtwoord wordt versleuteld opgeslagen en nooit teruggegeven."),
    status,
    h("div", { class: "row" }, host, security, port),
    h("div", { class: "row" }, user, pass),
    from,
    h("div", { class: "row" }, save, off), msg, testRow);

  const report = (text, error) => {
    msg.textContent = text;
    msg.style.color = error ? "" : "var(--success-text)";
    msg.hidden = !text;
  };
  const show = (m) => {
    host.value = m.host || "";
    security.value = m.security || "starttls";
    port.value = m.port || "";
    user.value = m.user || "";
    from.value = m.from || "";
    pass.value = "";
    pass.placeholder = m.has_password ? "Wachtwoord (ongewijzigd)" : "Wachtwoord";
    status.textContent = m.configured ? `Ingesteld: ${m.host}:${m.port}.` : "Nog geen mailserver ingesteld.";
    off.hidden = !m.configured;
    testRow.hidden = !m.configured;
  };
  security.addEventListener("change", () => {
    const def = { starttls: "587", tls: "465", none: "25" };
    if (!port.value || Object.values(def).includes(port.value)) port.value = def[security.value];
  });
  async function put(body, done) {
    save.disabled = off.disabled = true;
    report("", false);
    try {
      const m = await api("PUT", "/api/settings/mail", body);
      show(m);
      report(done, false);
      onChange?.();
    } catch (e) {
      report(e.message, true);
    } finally {
      save.disabled = off.disabled = false;
    }
  }
  form.addEventListener("submit", (e) => {
    e.preventDefault();
    put({ host: host.value.trim(), port: Number(port.value) || 0, security: security.value, user: user.value.trim(), password: pass.value, from: from.value.trim() }, "Opgeslagen. Stuur een testmail om te kijken of het werkt.");
  });
  off.addEventListener("click", () => {
    if (confirm("E-mail uitzetten? Geplande rapporten worden dan niet meer verstuurd.")) put({ host: "", port: 0, security: "", user: "", from: "", clear_password: true }, "E-mail staat uit.");
  });
  test.addEventListener("click", async () => {
    if (!testTo.value.trim()) { testTo.focus(); return; }
    test.disabled = true;
    test.textContent = "Versturen…";
    report("", false);
    try {
      await api("POST", "/api/settings/mail/test", { to: testTo.value.trim() });
      report(`Testmail verstuurd naar ${testTo.value.trim()}. Kijk ook in de spammap.`, false);
    } catch (e) {
      report(e.message, true);
    } finally {
      test.disabled = false;
      test.textContent = "Testmail sturen";
    }
  });
  api("GET", "/api/settings/mail").then(show).catch(() => { status.textContent = "Instelling niet beschikbaar."; });
  return form;
}
