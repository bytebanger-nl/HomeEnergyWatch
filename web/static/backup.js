// Back-up card in Instellingen: where the daily backup goes, when the last
// one was made (or why it failed), the backups that are kept and a button
// to make one now. The settings themselves are flags/env vars
// (HWLOCAL_BACKUP_*): a backup holds every password, so the app can neither
// download one nor point it elsewhere.
import { h, api, dates } from "./util.js";

const size = (bytes) => bytes >= 1e6 ? `${(bytes / 1e6).toFixed(1).replace(".", ",")} MB` : `${Math.max(1, Math.round(bytes / 1e3))} kB`;

export function backupCard() {
  const node = h("section", { class: "card" }, h("p", { class: "secondary" }, "Laden…"));

  function render(st) {
    if (!st.enabled) {
      node.replaceChildren(h("p", { class: "secondary" },
        "Back-ups staan uit (HWLOCAL_BACKUP_DIR=off). Zet ze aan met een map voor de back-ups; zie de handleiding."));
      return;
    }
    const last = st.last;
    const newest = st.backups[0];
    const status = last?.error
      ? h("p", { class: "form-error", role: "alert" }, `Laatste poging (${dates.dayTime(new Date(last.at))}) mislukt: ${last.error}`)
      : h("p", {}, newest ? `Laatste back-up: ${dates.dayTime(new Date(newest.at))} (${size(newest.size)}).` : "Nog geen back-up gemaakt.");
    const list = st.backups.length
      ? h("details", {}, h("summary", {}, `${st.backups.length} van ${st.keep} bewaard`),
        h("ul", {}, ...st.backups.map((b) => h("li", {}, `${b.file} · ${size(b.size)}`))))
      : null;
    const msg = h("div", { class: "form-error", role: "alert", hidden: true });
    const now = h("button", { class: "btn", type: "button", disabled: st.running }, st.running ? "Bezig…" : "Nu back-up maken");
    now.addEventListener("click", async () => {
      now.disabled = true;
      now.textContent = "Bezig…";
      msg.hidden = true;
      try {
        render(await api("POST", "/api/settings/backup", {}));
      } catch (e) {
        msg.textContent = e.message;
        msg.hidden = false;
        now.disabled = false;
        now.textContent = "Nu back-up maken";
      }
    });
    node.replaceChildren(
      h("p", { class: "secondary" },
        `Elke dag om ${st.at} maakt de gateway een back-up van de database, de sleutel voor de opgeslagen wachtwoorden en de instellingen. Zet de map bij voorkeur op een andere schijf of een NAS. Terugzetten: stop de gateway en gebruik 'hwlocal restore'.`),
      h("p", {}, "Map: ", h("code", {}, st.dir)),
      status,
      st.next ? h("p", { class: "secondary" }, `Volgende: ${dates.dayTime(new Date(st.next))}.`) : null,
      list,
      h("div", { class: "row form-actions" }, now),
      msg,
    );
  }

  api("GET", "/api/settings/backup").then(render).catch((e) => node.replaceChildren(h("p", { class: "form-error" }, e.message)));
  return node;
}
