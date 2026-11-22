// Plugins section of Instellingen (settings.js): manage plugins
// (integrations next to HomeWizard).
// A plugin type declares a config form the PWA renders here; within a
// configured plugin you add devices by hand or by scanning the network.
// After a scan the list stays until closed: adding one device marks it "Al
// toegevoegd", "Alles toevoegen" adds the rest in one go. The devices of a
// plugin are a collapsible block (closed by default, remembered per plugin
// in this browser); the plugin's own buttons stay visible.
import { h, api } from "./util.js";
import { istaStatusText } from "./heating.js";
import { fmtMetric } from "./klimaat.js";
import { outageLine } from "./outages.js";

export function renderPlugins(view) {
  const root = h("div", { class: "plugins" });
  view.append(
    h("h2", { class: "section-title" }, "Plugins"),
    h("p", { class: "muted" },
      "Integraties naast je HomeWizard-apparaten. Apparaten binnen een plugin verschijnen als tegel op het tabblad Klimaat. ",
      "Ze sturen niets aan, tenzij je bij een plugin ‘Bediening toestaan’ aanzet."),
    root,
  );

  let types = [];
  const timers = new Set(); // auth status polls, cleared when leaving the view
  // open scan results per plugin id: {cands, names: address -> typed name,
  // errors: address -> message, busy}; they survive reload()
  const scans = new Map();

  async function reload() {
    root.replaceChildren(h("p", { class: "muted" }, "Laden…"));
    let data;
    try {
      data = await api("GET", "/api/plugins");
    } catch (e) {
      root.replaceChildren(note("Kon plugins niet laden: " + e.message, "error"));
      return;
    }
    types = data.types || [];
    const instances = data.instances || [];
    try {
      const t = await api("GET", "/api/plugins/tabs");
      tabs = t.tabs || [];
      maxTabs = t.max || 3;
    } catch { tabs = []; }
    root.replaceChildren();
    if (!instances.length) {
      root.append(note("Nog geen plugins toegevoegd.", "info"));
    }
    for (const id of scans.keys()) if (!instances.some((i) => i.id === id)) scans.delete(id);
    for (const inst of instances) root.append(instanceCard(inst));
    root.append(addPluginCard());
    if (instances.some((i) => i.devices?.length)) root.append(tabsCard());
  }

  let tabs = [];
  let maxTabs = 3;

  // the user's own tabs for plugin devices (next to Klimaat)
  function tabsCard() {
    const card = h("section", { class: "card plugin-card" },
      h("div", { class: "plugin-head" }, h("div", {}, h("strong", {}, "Eigen tabbladen"),
        h("div", { class: "muted small" }, `Zet plugin-apparaten op een eigen tabblad naast Klimaat Plugins, bijvoorbeeld ‘Verwarmen’. Hooguit ${maxTabs}; een tabblad zonder apparaten wordt niet getoond. Kies het tabblad per apparaat hierboven; de plek in de rij kies je onder Tabbladen.`))));
    const msg = h("div", { class: "error-text small" });
    const fail = (e) => { msg.textContent = e.message; };
    for (const t of tabs) {
      const name = h("input", { class: "input", value: t.name, maxlength: 24, "aria-label": "Naam van het tabblad" });
      card.append(h("div", { class: "plugin-bar" }, name,
        h("button", { class: "btn-ghost", type: "button", onclick: async () => {
          try { await api("PATCH", `/api/plugins/tabs/${t.id}`, { name: name.value }); reload(); } catch (e) { fail(e); }
        } }, "Hernoemen"),
        h("button", { class: "btn-ghost danger", type: "button", onclick: async () => {
          if (!confirm(`Tabblad '${t.name}' verwijderen? De apparaten gaan terug naar Klimaat.`)) return;
          try { await api("DELETE", `/api/plugins/tabs/${t.id}`); reload(); } catch (e) { fail(e); }
        } }, "Verwijderen")));
    }
    if (tabs.length < maxTabs) {
      const name = h("input", { class: "input", maxlength: 24, placeholder: "Naam, bijv. Verwarmen", "aria-label": "Nieuw tabblad" });
      card.append(h("div", { class: "plugin-bar" }, name,
        h("button", { class: "btn", type: "button", onclick: async () => {
          try { await api("POST", "/api/plugins/tabs", { name: name.value }); reload(); } catch (e) { fail(e); }
        } }, "Tabblad toevoegen")));
    }
    card.append(msg);
    return card;
  }

  function tabSelect(d) {
    if (!tabs.length) return null;
    const sel = h("select", { class: "select", "aria-label": "Tabblad" },
      h("option", { value: "0" }, "Klimaat Plugins"),
      ...tabs.map((t) => h("option", { value: String(t.id) }, t.name)));
    sel.value = String(d.tab_id || 0);
    sel.addEventListener("change", async () => {
      try { await api("PUT", `/api/plugins/devices/${d.id}/tab`, { tab_id: Number(sel.value) }); } catch (e) { alert(e.message); }
      reload();
    });
    return sel;
  }

  function typeInfo(typ) {
    return types.find((t) => t.type === typ) || { name: typ, instance_form: [], device_form: [], can_scan: false, can_add_manual: false };
  }

  // ---- one configured plugin ----
  function instanceCard(inst) {
    const info = typeInfo(inst.type);
    const card = h("section", { class: "card plugin-card" });
    const header = h("div", { class: "plugin-head" },
      h("div", {},
        h("strong", {}, inst.name || info.name),
        h("div", { class: "muted small" }, info.name)),
      h("div", { class: "plugin-actions" },
        toggle(inst.enabled, async (on) => {
          await api("PATCH", `/api/plugins/${inst.id}`, { enabled: on });
          reload();
        }, "Ingeschakeld"),
        inst.can_control ? toggle(inst.control, async (on) => {
          if (on && !confirm("Bediening toestaan?\n\nDe app heeft geen login: iedereen op je netwerk die de app opent, kan deze apparaten dan bedienen, net als de stopcontacten. Regels onder Meldingen kunnen ze ook automatisch bedienen.")) { reload(); return; }
          try {
            await api("PATCH", `/api/plugins/${inst.id}`, { control: on });
          } catch (e) {
            alert("Kon bediening niet wijzigen: " + e.message);
          }
          reload();
        }, "Bediening toestaan") : null,
        h("button", { class: "btn-ghost danger", onclick: () => removeInstance(inst) }, "Verwijderen")),
    );
    card.append(header);

    const connected = !info.needs_auth || !!(inst.auth && inst.auth.connected);
    if (info.needs_auth) card.append(authBlock(inst));
    if (inst.type === "ista") card.append(istaBlock(inst));

    // devices: a collapsible block with a summary
    const devs = inst.devices || [];
    if (!devs.length) {
      card.append(h("p", { class: "muted small" }, "Nog geen apparaten."));
    } else {
      const online = devs.filter((d) => d.reading?.online).length;
      const box = h("details", { class: "plugin-devices-box" },
        h("summary", {}, `${devs.length} ${devs.length === 1 ? "apparaat" : "apparaten"} · ${online} online`),
        h("div", { class: "plugin-devices" }, ...devs.map(deviceRow)));
      box.open = openBlocks.has(inst.id);
      box.addEventListener("toggle", () => { if (box.open) openBlocks.add(inst.id); else openBlocks.delete(inst.id); openBlocks.save(); });
      card.append(box);
    }

    // add / scan
    const bar = h("div", { class: "plugin-bar" });
    const scanLabel = info.needs_auth ? "Kamers ophalen" : inst.type === "ista" ? "Radiatoren ophalen" : "Netwerk scannen";
    if (info.can_scan && connected) bar.append(h("button", { class: "btn", onclick: () => scan(inst, card) }, scanLabel));
    if (info.can_add_manual) bar.append(h("button", { class: "btn-ghost", onclick: () => manualAdd(inst, info, card) }, "Apparaat handmatig"));
    card.append(bar);
    if (scans.has(inst.id)) card.append(scanPanel(inst));
    return card;
  }

  // ista: how far fetching is, refreshed while it runs
  function istaBlock(inst) {
    const line = h("p", { class: "muted small" }, "");
    const update = async () => {
      let acc;
      try { acc = (await api("GET", "/api/ista")).accounts.find((a) => a.account === inst.id); } catch { return; }
      if (!line.isConnected && line.dataset.seen) return; // left the view or reloaded
      line.dataset.seen = "1";
      line.textContent = inst.enabled ? istaStatusText(acc) : "Uitgeschakeld: er wordt niets opgehaald.";
      if (acc?.state === "syncing") {
        const t = setTimeout(() => { timers.delete(t); update(); }, 5000);
        timers.add(t);
      }
    };
    update();
    return line;
  }

  function authBlock(inst) {
    const box = h("div", { class: "plugin-auth" });
    const st = inst.auth || {};
    if (st.connected) {
      box.append(h("div", { class: "muted small" }, "✓ " + (st.message || "Verbonden.")));
    } else {
      box.append(h("div", { class: "muted small" }, st.pending ? (st.message || "Wachten op goedkeuring…") : "Nog niet verbonden."));
      box.append(h("button", { class: "btn", onclick: () => connect(inst, box) }, "Verbinden met tado"));
    }
    return box;
  }

  async function connect(inst, box) {
    box.replaceChildren(h("p", { class: "muted small" }, "Inlogcode ophalen…"));
    let ch;
    try {
      ch = await api("POST", `/api/plugins/${inst.id}/auth`);
    } catch (e) {
      box.replaceChildren(note("Kon inloggen niet starten: " + e.message, "error"));
      return;
    }
    const link = ch.verification_uri_complete || ch.verification_uri;
    box.replaceChildren(...[
      h("p", {}, ch.message || "Open de link en keur het verzoek goed in je browser:"),
      link ? h("p", {}, h("a", { href: link, target: "_blank", rel: "noopener" }, link)) : null,
      ch.user_code ? h("p", { class: "muted" }, "Code: ", h("strong", {}, ch.user_code)) : null,
      h("p", { class: "muted small" }, "Zodra je in tado hebt goedgekeurd, verschijnen je kamers via ‘Kamers ophalen’."),
    ].filter(Boolean));
    const started = Date.now();
    const timer = setInterval(async () => {
      let s;
      try { s = await api("GET", `/api/plugins/${inst.id}/auth`); } catch { return; }
      if (s.connected) {
        clearInterval(timer); timers.delete(timer); reload();
      } else if (!s.pending && Date.now() - started > 6000) {
        clearInterval(timer); timers.delete(timer);
        box.append(note(s.message || "Niet verbonden.", "error"));
      }
    }, 5000);
    timers.add(timer);
  }

  function deviceRow(d) {
    const r = d.reading;
    const summary = r?.metrics?.length
      ? r.metrics.map((m) => `${m.label}: ${fmtMetric(m)}`).join(" · ")
      : (r?.error ? "fout: " + r.error : "nog geen meting");

    const nameWrap = h("div", { class: "plugin-name" });
    const showName = () => nameWrap.replaceChildren(
      h("strong", {}, d.name || d.address),
      h("button", { class: "btn-link", title: "Hernoemen", onclick: editName }, "Hernoemen"));
    function editName() {
      const input = h("input", { class: "input", value: d.name || "" });
      const save = async () => {
        try {
          await api("PATCH", `/api/plugins/devices/${d.id}`, { name: input.value.trim() });
          reload();
        } catch (e) { input.after(h("span", { class: "error-text small" }, " " + e.message)); }
      };
      input.addEventListener("keydown", (e) => { if (e.key === "Enter") save(); if (e.key === "Escape") showName(); });
      nameWrap.replaceChildren(input,
        h("button", { class: "btn", onclick: save }, "Opslaan"),
        h("button", { class: "btn-ghost", onclick: showName }, "Annuleren"));
      input.focus();
      input.select();
    }
    showName();

    // control allowed for the plugin, on for this device, yet nothing to set
    const noControls = d.can_control && d.control && !(d.controls || []).some((c) => !c.hidden)
      ? h("div", { class: "muted small" }, d.plugin_type === "tuya"
        ? "Geen bediening: dit apparaat heeft geen apparaatmodel. Scan opnieuw (Netwerk scannen) om het op te halen; handmatig toegevoegde apparaten kunnen niet bediend worden."
        : "Dit apparaat heeft geen bediening.")
      : null;

    return h("div", { class: "plugin-device" },
      h("div", { class: "plugin-device-info" },
        nameWrap,
        h("div", { class: "muted small" }, d.address),
        h("div", { class: "plugin-metrics small" }, summary),
        d.enabled ? outageLine(d.id).node : null,
        noControls),
      h("div", { class: "plugin-actions" },
        tabSelect(d),
        toggle(d.enabled, async (on) => { await api("PATCH", `/api/plugins/devices/${d.id}`, { enabled: on }); reload(); }, "Aan"),
        d.can_control ? toggle(d.control, async (on) => {
          try {
            await api("PATCH", `/api/plugins/devices/${d.id}`, { control: on });
          } catch (e) {
            alert("Kon bediening niet wijzigen: " + e.message);
          }
          reload();
        }, "Bedienen") : null,
        h("button", { class: "btn-ghost danger", onclick: () => removeDevice(d) }, "×")),
    );
  }

  async function removeInstance(inst) {
    if (!confirm(`Plugin "${inst.name}" en al zijn apparaten verwijderen?`)) return;
    await api("DELETE", `/api/plugins/${inst.id}`);
    reload();
  }
  async function removeDevice(d) {
    if (!confirm(`Apparaat "${d.name || d.address}" verwijderen?`)) return;
    await api("DELETE", `/api/plugins/devices/${d.id}`);
    reload();
  }

  async function scan(inst, card) {
    card.querySelector(".plugin-scan")?.remove();
    const panel = h("div", { class: "plugin-scan" }, h("p", { class: "muted small" }, "Scannen…"));
    card.append(panel);
    let res;
    try {
      res = await api("POST", `/api/plugins/${inst.id}/scan`);
    } catch (e) {
      panel.replaceChildren(note("Scan mislukt: " + e.message, "error"));
      return;
    }
    const cands = res.candidates || [];
    if (!cands.length) { panel.replaceChildren(note("Geen apparaten gevonden.", "info")); return; }
    scans.set(inst.id, { cands, names: new Map(), errors: new Map(), busy: false });
    panel.replaceWith(scanPanel(inst));
  }

  // the scan results of a plugin: added devices marked, the rest with a name
  // field and Toevoegen, plus Alles toevoegen for all of them
  function scanPanel(inst) {
    const sc = scans.get(inst.id);
    const have = new Set((inst.devices || []).map((d) => d.address));
    const isAdded = (c) => c.added || have.has(c.address);
    const todo = sc.cands.filter((c) => !isAdded(c));
    const nameOf = (c) => (sc.names.get(c.address) ?? c.name ?? "").trim() || c.name;
    const add = (c) => api("POST", `/api/plugins/${inst.id}/devices`, { address: c.address, name: nameOf(c), config: c.config });

    const addAll = h("button", { class: "btn", type: "button", disabled: sc.busy, onclick: async () => {
      sc.busy = true;
      sc.errors.clear();
      addAll.disabled = true;
      addAll.textContent = "Toevoegen…";
      for (const c of todo) {
        try { await add(c); } catch (e) { sc.errors.set(c.address, e.message); } // the rest goes on
      }
      sc.busy = false;
      reload();
    } }, `Alles toevoegen (${todo.length})`);
    const close = h("button", { class: "btn-ghost", type: "button", onclick: () => { scans.delete(inst.id); panel.remove(); } }, "Sluiten");

    const panel = h("div", { class: "plugin-scan" },
      h("div", { class: "plugin-bar" },
        h("span", { class: "muted small" }, todo.length
          ? `${sc.cands.length} gevonden — geef een naam en voeg toe:`
          : `${sc.cands.length} gevonden, alle al toegevoegd.`),
        todo.length ? addAll : null, close));
    for (const c of sc.cands) {
      if (isAdded(c)) {
        panel.append(h("div", { class: "plugin-device" },
          h("div", { class: "plugin-device-info" }, h("div", {}, c.name || c.address), h("div", { class: "muted small" }, c.address)),
          h("span", { class: "muted small" }, "Al toegevoegd")));
        continue;
      }
      const nameInput = h("input", { class: "input", value: sc.names.get(c.address) ?? c.name ?? "", placeholder: "Naam", "aria-label": "Naam" });
      nameInput.addEventListener("input", () => sc.names.set(c.address, nameInput.value));
      const err = h("div", { class: "error-text small" }, sc.errors.get(c.address) || "");
      panel.append(h("div", { class: "plugin-device" },
        h("div", { class: "plugin-device-info" }, nameInput, h("div", { class: "muted small" }, c.address), err),
        h("button", {
          class: "btn", type: "button", disabled: sc.busy, onclick: async (e) => {
            const btn = e.currentTarget;
            btn.disabled = true;
            sc.errors.delete(c.address);
            try {
              await add(c);
            } catch (ex) {
              sc.errors.set(c.address, ex.message);
              err.textContent = ex.message;
              btn.disabled = false;
              return;
            }
            reload(); // the scan list stays; this one is now "Al toegevoegd"
          },
        }, "Toevoegen")));
    }
    return panel;
  }

  function manualAdd(inst, info, card) {
    const fields = {};
    const form = h("form", {
      class: "plugin-form", onsubmit: async (e) => {
        e.preventDefault();
        const address = fields.address?.value.trim();
        const config = {};
        for (const f of info.device_form || []) if (f.key !== "address") config[f.key] = fields[f.key]?.value.trim() || "";
        try {
          await api("POST", `/api/plugins/${inst.id}/devices`, { address, name: fields.name?.value.trim(), config });
          reload();
        } catch (err) { form.querySelector(".form-err").textContent = err.message; }
      },
    });
    form.append(labeledInput("Naam (optioneel)", (el) => (fields.name = el)));
    for (const f of info.device_form || []) {
      form.append(labeledInput(f.label, (el) => (fields[f.key] = el), f));
    }
    form.append(h("div", { class: "plugin-bar" },
      h("button", { class: "btn", type: "submit" }, "Toevoegen"),
      h("span", { class: "form-err error-text" })));
    card.append(form);
  }

  // ---- add a new plugin ----
  function addPluginCard() {
    const card = h("section", { class: "card" });
    const picker = h("select", { class: "select" },
      h("option", { value: "" }, "Kies een plugin…"),
      ...types.map((t) => h("option", { value: t.type }, t.name)));
    const formHost = h("div", {});
    picker.addEventListener("change", () => {
      formHost.replaceChildren();
      const info = types.find((t) => t.type === picker.value);
      if (info) formHost.append(addForm(info));
    });
    card.append(h("div", { class: "plugin-head" }, h("strong", {}, "Plugin toevoegen"), picker), formHost);
    return card;
  }

  function addForm(info) {
    const fields = {};
    const form = h("form", {
      class: "plugin-form", onsubmit: async (e) => {
        e.preventDefault();
        const config = {};
        for (const f of info.instance_form || []) config[f.key] = fields[f.key]?.value.trim() || "";
        try {
          await api("POST", "/api/plugins", { type: info.type, name: fields.name?.value.trim(), config });
          reload();
        } catch (err) { form.querySelector(".form-err").textContent = err.message; }
      },
    });
    if (info.description) form.append(h("p", { class: "muted small" }, info.description));
    form.append(labeledInput("Naam (optioneel)", (el) => (fields.name = el)));
    for (const f of info.instance_form || []) form.append(labeledInput(f.label, (el) => (fields[f.key] = el), f));
    form.append(h("div", { class: "plugin-bar" },
      h("button", { class: "btn", type: "submit" }, "Toevoegen"),
      h("span", { class: "form-err error-text" })));
    return form;
  }

  reload();
  return () => timers.forEach((t) => clearInterval(t));
}

// ---- small helpers ----

// the plugins whose device block is open, remembered in this browser
const openBlocks = (() => {
  let ids = new Set();
  try { ids = new Set(JSON.parse(localStorage.getItem("hw.plugins.open") || "[]")); } catch { /* private mode */ }
  return {
    has: (id) => ids.has(id),
    add: (id) => ids.add(id),
    delete: (id) => ids.delete(id),
    save: () => { try { localStorage.setItem("hw.plugins.open", JSON.stringify([...ids])); } catch { /* private mode */ } },
  };
})();
function labeledInput(label, ref, field = {}) {
  const input = h("input", {
    class: "input", type: field.type === "password" ? "password" : field.type === "number" ? "number" : "text",
  });
  if (field.required) input.required = true;
  ref(input);
  // the help below the field: as a placeholder it was cut off and gone once typed in
  return h("label", { class: "plugin-field" }, h("span", {}, label), input,
    field.help ? h("small", { class: "muted" }, field.help) : null);
}

function toggle(on, onchange, label) {
  const input = h("input", { type: "checkbox" });
  input.checked = !!on;
  input.addEventListener("change", () => onchange(input.checked));
  return h("label", { class: "plugin-toggle", title: label }, input, h("span", {}, label));
}

function note(text, kind) {
  return h("div", { class: "notice", "data-kind": kind }, text);
}

