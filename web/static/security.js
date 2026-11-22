// Security: the login screen and the "Beveiliging" card under Instellingen
// (password, logged-in browsers, API tokens). Without a password the app is
// open to everyone on the network, as it always was; once one is set every
// browser logs in once and stays logged in while it is used (90 days).
import { h, api, ago, dates } from "./util.js";

/** GET /api/auth; null when the gateway cannot be reached. */
export async function authStatus() {
  try {
    return await api("GET", "/api/auth");
  } catch {
    return null;
  }
}

const insecure = () => location.protocol === "http:" && !["localhost", "127.0.0.1", "[::1]"].includes(location.hostname);

// password managers want a user name next to the password
const userField = () => h("input", { type: "text", name: "username", autocomplete: "username", value: "hwlocal", hidden: true, "aria-hidden": "true", tabindex: "-1" });

function passwordInput(label, autocomplete) {
  return h("input", { type: "password", "aria-label": label, placeholder: label, autocomplete, required: true, spellcheck: "false" });
}

/** Replaces the app with the login form; a correct password reloads it. */
export function showLogin() {
  if (document.body.dataset.login) return;
  document.body.dataset.login = "1";
  for (const el of document.querySelectorAll(".tabs")) el.hidden = true;
  const conn = document.getElementById("conn");
  if (conn) {
    conn.dataset.state = "down";
    conn.querySelector(".conn-text").textContent = "Niet ingelogd";
  }
  const pw = passwordInput("Wachtwoord", "current-password");
  const button = h("button", { class: "btn primary", type: "submit" }, "Inloggen");
  const error = h("div", { class: "form-error", role: "alert", hidden: true });
  const form = h("form", { class: "card login-card" },
    h("h2", {}, "Inloggen"),
    h("p", { class: "secondary" }, "Deze gateway is beveiligd met een wachtwoord. Je blijft daarna op dit apparaat ingelogd zolang je de app gebruikt."),
    userField(),
    h("div", { class: "row" }, pw, button),
    error,
    insecure() ? h("p", { class: "muted" }, "Let op: je verbinding gebruikt http, dus het wachtwoord gaat onversleuteld over je netwerk. Gebruik bij voorkeur https.") : null,
    h("p", { class: "muted" }, "Wachtwoord vergeten? Zet het op de gateway zelf uit met ", h("code", {}, "hwlocal password -off"), " of stel een nieuw in met ", h("code", {}, "hwlocal password"), "."));
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    button.disabled = true;
    error.hidden = true;
    try {
      await api("POST", "/api/auth/login", { password: pw.value });
      location.reload();
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
      pw.select();
      button.disabled = false;
    }
  });
  const view = document.getElementById("view");
  view.replaceChildren(form);
  pw.focus();
}

/** The "Beveiliging" card: set, change or remove the password; sessions and tokens. */
export function securityCard() {
  const root = h("div", { class: "security" }, h("section", { class: "card" }, h("p", { class: "secondary" }, "Laden…")));
  const load = async () => {
    const st = await authStatus();
    if (!st || !st.available) {
      root.replaceChildren(h("section", { class: "card" }, h("p", { class: "secondary" }, "Instelling niet beschikbaar.")));
      return;
    }
    root.replaceChildren(...(st.enabled ? enabledCards(load) : [setupCard(load)]));
  };
  load();
  return root;
}

function message() {
  const el = h("div", { class: "form-error", role: "alert", hidden: true });
  el.show = (text, error = true) => {
    el.textContent = text;
    el.style.color = error ? "" : "var(--success-text)";
    el.hidden = !text;
  };
  return el;
}

/** Two fields for a new password that must be equal. */
function newPassword() {
  const pw = passwordInput("Nieuw wachtwoord (minstens 8 tekens)", "new-password");
  const again = passwordInput("Nog een keer", "new-password");
  pw.minLength = 8;
  return {
    nodes: [pw, again],
    value: () => pw.value,
    check: () => (pw.value === again.value ? "" : "De twee wachtwoorden zijn niet gelijk."),
  };
}

function setupCard(reload) {
  const np = newPassword();
  const button = h("button", { class: "btn primary", type: "submit" }, "Wachtwoord instellen");
  const msg = message();
  const form = h("form", { class: "card" },
    h("p", { class: "secondary" }, "Er is geen wachtwoord ingesteld: iedereen op je netwerk (ook gasten op je wifi) kan de app gebruiken, apparaten bedienen en instellingen wijzigen."),
    h("p", { class: "secondary" }, "Met een wachtwoord moet elke browser één keer inloggen, ook de app op je telefoon. Daarna blijft hij ingelogd zolang je hem gebruikt (90 dagen na het laatste gebruik). Dit apparaat blijft meteen ingelogd."),
    insecure() ? h("p", { class: "notice" }, "Je gebruikt de app via http: het wachtwoord gaat dan onversleuteld over je netwerk. Dat houdt nieuwsgierige gebruikers buiten, maar niet iemand die je wifi afluistert. Gebruik voor echte bescherming https (zie de handleiding: TLS of tailscale serve).") : null,
    userField(),
    h("div", { class: "row" }, ...np.nodes, button),
    msg);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const bad = np.check();
    if (bad) return msg.show(bad);
    button.disabled = true;
    try {
      await api("PUT", "/api/auth/password", { password: np.value() });
      reload();
    } catch (err) {
      msg.show(err.message);
      button.disabled = false;
    }
  });
  return form;
}

function enabledCards(reload) {
  // status, log out, change and remove the password
  const logout = h("button", { class: "btn", type: "button" }, "Uitloggen");
  logout.addEventListener("click", async () => {
    await api("POST", "/api/auth/logout").catch(() => {});
    location.reload();
  });
  const status = h("section", { class: "card" },
    h("p", {}, h("b", {}, "Wachtwoord staat aan. "), "De app en de API vragen om in te loggen."),
    insecure() ? h("p", { class: "muted" }, "Je verbinding gebruikt http: het wachtwoord gaat onversleuteld over je netwerk. Gebruik bij voorkeur https.") : null,
    h("div", { class: "row" }, logout));

  const current = passwordInput("Huidig wachtwoord", "current-password");
  const np = newPassword();
  const change = h("button", { class: "btn primary", type: "submit" }, "Wijzigen");
  const off = h("button", { class: "btn danger", type: "button" }, "Wachtwoord uitzetten");
  const msg = message();
  const form = h("form", { class: "card" },
    h("h3", {}, "Wachtwoord wijzigen of uitzetten"),
    h("p", { class: "secondary" }, "Na het wijzigen moeten alle andere browsers opnieuw inloggen. API-tokens blijven werken."),
    userField(),
    h("div", { class: "row" }, current),
    h("div", { class: "row" }, ...np.nodes, change),
    h("div", { class: "row" }, off),
    msg);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const bad = np.check();
    if (bad) return msg.show(bad);
    change.disabled = true;
    try {
      await api("PUT", "/api/auth/password", { current: current.value, password: np.value() });
      reload();
    } catch (err) {
      msg.show(err.message);
      change.disabled = false;
    }
  });
  off.addEventListener("click", async () => {
    if (!current.value) {
      msg.show("Vul je huidige wachtwoord in om het uit te zetten.");
      current.focus();
      return;
    }
    if (!confirm("Zonder wachtwoord kan iedereen op je netwerk de app weer gebruiken en apparaten bedienen. Uitzetten?")) return;
    off.disabled = true;
    try {
      await api("DELETE", "/api/auth/password", { current: current.value });
      reload();
    } catch (err) {
      msg.show(err.message);
      off.disabled = false;
    }
  });

  return [status, form, sessionsCard(), tokensCard()];
}

function sessionsCard() {
  const list = h("ul", { class: "auth-list" });
  const msg = message();
  const others = h("button", { class: "btn", type: "button" }, "Alle andere uitloggen");
  const card = h("section", { class: "card" },
    h("h3", {}, "Ingelogde browsers"),
    h("p", { class: "secondary" }, "Een browser die 90 dagen niet is gebruikt, wordt vanzelf uitgelogd."),
    list, h("div", { class: "row" }, others), msg);
  const load = async () => {
    let sessions;
    try {
      sessions = await api("GET", "/api/auth/sessions");
    } catch (err) {
      return msg.show(err.message);
    }
    others.hidden = sessions.length < 2;
    list.replaceChildren(...sessions.map((s) => {
      const end = s.current
        ? h("span", { class: "tone-chip idle" }, "dit apparaat")
        : h("button", {
          class: "btn-ghost danger", type: "button",
          onclick: async () => {
            try { await api("DELETE", `/api/auth/sessions/${encodeURIComponent(s.id)}`); load(); } catch (err) { msg.show(err.message); }
          },
        }, "Uitloggen");
      return h("li", {},
        h("div", {}, h("div", {}, s.agent || "Onbekende browser"),
          h("div", { class: "muted" }, `${s.client} · ingelogd ${dates.day(new Date(s.created))} · laatst gebruikt ${ago(s.last_seen)}`)),
        end);
    }));
  };
  others.addEventListener("click", async () => {
    try { await api("DELETE", "/api/auth/sessions/others"); load(); } catch (err) { msg.show(err.message); }
  });
  load();
  return card;
}

const SCOPES = { read: "Alleen lezen", control: "Lezen en bedienen" };

function tokensCard() {
  const list = h("ul", { class: "auth-list" });
  const name = h("input", { type: "text", placeholder: "Naam, bijv. Home Assistant", "aria-label": "Naam van het token", maxlength: "60", required: true });
  const scope = h("select", { "aria-label": "Rechten" }, ...Object.entries(SCOPES).map(([v, t]) => h("option", { value: v }, t)));
  const create = h("button", { class: "btn primary", type: "submit" }, "Token maken");
  const created = h("div", { class: "token-new", hidden: true });
  const msg = message();
  const form = h("form", { class: "card" },
    h("h3", {}, "API-tokens"),
    h("p", { class: "secondary" }, "Voor scripts en koppelingen die de API gebruiken (bijvoorbeeld Home Assistant). Stuur het token mee als ",
      h("code", {}, "Authorization: Bearer hwl_…"), ". Een token met ‘Alleen lezen’ kan niets schakelen of wijzigen; geen enkel token kan deze beveiligingsinstellingen wijzigen."),
    list, created,
    h("div", { class: "row" }, name, scope, create), msg);
  const load = async () => {
    let tokens;
    try {
      tokens = await api("GET", "/api/auth/tokens");
    } catch (err) {
      return msg.show(err.message);
    }
    list.replaceChildren(...tokens.map((t) => h("li", {},
      h("div", {}, h("div", {}, t.name),
        h("div", { class: "muted" }, `${SCOPES[t.scope] || t.scope} · gemaakt ${dates.day(new Date(t.created))} · laatst gebruikt ${t.last_used ? ago(t.last_used) : "nooit"}`)),
      h("button", {
        class: "btn-ghost danger", type: "button",
        onclick: async () => {
          if (!confirm(`Token ‘${t.name}’ intrekken? Wat het gebruikt, krijgt daarna geen toegang meer.`)) return;
          try { await api("DELETE", `/api/auth/tokens/${encodeURIComponent(t.id)}`); load(); } catch (err) { msg.show(err.message); }
        },
      }, "Intrekken"))));
  };
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    create.disabled = true;
    msg.show("");
    try {
      const t = await api("POST", "/api/auth/tokens", { name: name.value, scope: scope.value });
      const field = h("input", { type: "text", readonly: true, value: t.secret, "aria-label": "Nieuw token", spellcheck: "false" });
      const copy = h("button", { class: "btn", type: "button" }, "Kopiëren");
      copy.addEventListener("click", async () => {
        field.select();
        try { await navigator.clipboard.writeText(t.secret); copy.textContent = "Gekopieerd"; } catch { /* selected; copy by hand */ }
      });
      created.replaceChildren(
        h("p", {}, h("b", {}, `Token ‘${t.name}’: `), "kopieer het nu, je ziet het maar één keer."),
        h("div", { class: "row" }, field, copy));
      created.hidden = false;
      name.value = "";
      load();
    } catch (err) {
      msg.show(err.message);
    } finally {
      create.disabled = false;
    }
  });
  load();
  return form;
}
