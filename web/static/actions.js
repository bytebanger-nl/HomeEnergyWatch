// Manual actions: automations without conditions that the user starts and
// stops by hand (on Automatisering and in a dashboard tile). Switched on
// they run their actions (wait, regulate) like any automation, switched off
// their "anders"-actions. One without "anders"-actions, wait or regulate is
// a button (state.momentary): it runs its actions once.
//
// actionControl is the switch or button; watchRules shares one poll of the
// rules between everything on screen that shows a manual action.
import { h, api, onOffSwitch } from "./util.js";

const POLL_MS = 10e3;
const AFTER_SWITCH_MS = 3e3;

/** The switch or button of manual action r. opts: onDone(view) after it was switched, onError(message). */
export function actionControl(r, opts = {}) {
  const s = r.state || {};
  const set = async (on) => {
    try {
      const v = await api("POST", `/api/rules/${r.id}/${on ? "start" : "stop"}`);
      refreshRules();
      // the gateway acts at its next evaluation (within seconds): show that
      // without waiting for the next poll
      setTimeout(refreshRules, AFTER_SWITCH_MS);
      opts.onDone?.(v);
    } catch (e) {
      opts.onError?.(e.message);
    }
  };
  if (s.momentary) {
    const btn = h("button", { class: "btn", type: "button", disabled: !r.enabled || s.active,
      onclick: () => { btn.disabled = true; set(true); } }, s.active ? "Bezig…" : "Uitvoeren");
    return btn;
  }
  const sw = onOffSwitch(`${r.name} aan/uit`, set);
  sw.set(!!s.active, { disabled: !r.enabled });
  return sw.node;
}

// ---- the shared poll ----

const watchers = new Set();
let rules = null; // the last list, null before the first
let timer = null;

/** Calls fn(rules) now (when loaded) and after every poll; returns the function that stops it. */
export function watchRules(fn) {
  watchers.add(fn);
  if (rules) fn(rules);
  if (watchers.size === 1) refreshRules();
  return () => {
    watchers.delete(fn);
    if (!watchers.size) { clearTimeout(timer); timer = null; }
  };
}

/** Loads the rules again now (after switching, tuning) and tells every watcher. */
export async function refreshRules() {
  clearTimeout(timer);
  timer = null;
  try {
    rules = (await api("GET", "/api/rules")).rules || [];
    for (const fn of watchers) fn(rules);
  } catch {
    // keep the last list; the next poll tries again
  }
  if (watchers.size && !timer) timer = setTimeout(refreshRules, POLL_MS);
}

/** The manual actions of a list of rules, by name. */
export const manualActions = (list) => (list || []).filter((r) => r.manual).sort((a, b) => a.name.localeCompare(b.name));
