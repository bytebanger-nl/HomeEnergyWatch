// Screenshots of a running gateway (http://localhost:8080) with headless Edge.
//   node scripts/screenshots/shoot.mjs <outdir> <shots.mjs> [only-name-substring]
// Edge (Windows path below) and Node 22+ (global WebSocket) are needed.
// shots.mjs exports default [{name, hash, w, h, mobile, scale, wait, steps, sel, pad, maxH, full, dump}]
//  steps: JS run in the page (async allowed) with helpers:
//    click(text, n), pick(selectIndex, value), q(sel), wait(ms), byText(sel, text)
//  sel: JS expression returning an element or an array of elements; the shot is their union
//  dump: print the structure of #view instead of shooting
import { spawn } from 'node:child_process';
import { writeFileSync, mkdirSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const [, , outDir, shotsFile, only] = process.argv;
const shots = (await import(pathToFileURL(resolve(shotsFile)).href)).default.filter((s) => !only || s.name.includes(only));
mkdirSync(outDir, { recursive: true });
const EDGE = 'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe';
const port = 9335;
const prof = join(process.env.TEMP || '.', 'hwguide-edge');
mkdirSync(prof, { recursive: true });
const edge = spawn(EDGE, ['--headless=new', '--disable-gpu', '--hide-scrollbars', '--disable-extensions', '--no-first-run',
  `--remote-debugging-port=${port}`, `--user-data-dir=${prof}`, 'about:blank'], { stdio: 'ignore' });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let ws;
for (let i = 0; i < 50; i++) {
  try {
    const list = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
    const page = list.find((t) => t.type === 'page');
    if (page) { ws = new WebSocket(page.webSocketDebuggerUrl); break; }
  } catch {}
  await sleep(300);
}
await new Promise((r) => ws.addEventListener('open', r));
let id = 0; const pending = new Map();
ws.addEventListener('message', (e) => {
  const m = JSON.parse(e.data);
  if (m.id && pending.has(m.id)) { pending.get(m.id)(m); pending.delete(m.id); }
});
const send = (method, params = {}) => new Promise((r) => { const i = ++id; pending.set(i, r); ws.send(JSON.stringify({ id: i, method, params })); });
const run = async (expr) => {
  const r = await send('Runtime.evaluate', { expression: expr, awaitPromise: true, returnByValue: true });
  if (r.result?.exceptionDetails) return 'ERROR ' + (r.result.exceptionDetails.exception?.description || r.result.exceptionDetails.text);
  return r.result?.result?.value;
};

const HELPERS = `
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));
  const q = (s) => document.querySelector(s);
  const qa = (s) => [...document.querySelectorAll(s)];
  const vis = (e) => e && e.getClientRects().length > 0;
  const qv = (s) => qa(s).find(vis);
  const qav = (s) => qa(s).filter(vis);
  const byText = (s, t, n = 0) => qa(s).filter((e) => vis(e) && e.textContent.trim() === t)[n];
  const has = (s, t, n = 0) => qa(s).filter((e) => vis(e) && e.textContent.includes(t))[n];
  const click = (t, n = 0) => { const b = byText('button,a,summary,label', t, n); if (!b) throw new Error('no ' + t); b.click(); };
  const pickText = (i, t) => { const s = qa('select').filter(vis)[i]; s.value = [...s.options].find((o) => o.textContent.trim() === t).value; s.dispatchEvent(new Event('change', { bubbles: true })); };
  const pick = (i, v) => { const s = qa('select').filter(vis)[i]; s.value = v; s.dispatchEvent(new Event('change', { bubbles: true })); };
`;
const DUMP = `(() => { const out = []; const walk = (el, d) => { for (const c of el.children) { if (c.hidden) continue; const r = c.getBoundingClientRect(); if (!r.height) continue;
  out.push(' '.repeat(d * 2) + c.tagName.toLowerCase() + (c.className ? '.' + String(c.className).split(' ').join('.') : '') + ' y=' + Math.round(r.top + scrollY) + ' h=' + Math.round(r.height) + ' | ' + (c.innerText || '').slice(0, 50).split('\\n').join(' '));
  if (d < 2) walk(c, d + 1); } }; walk(document.getElementById('view'), 0); return out.join('\\n'); })()`;

await send('Page.enable');
await send('Runtime.enable');
for (const s of shots) {
  const w = s.w || (s.mobile ? 390 : 1200), h = s.h || (s.mobile ? 844 : 900);
  await send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: s.scale || 2, mobile: !!s.mobile });
  await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: s.dark ? 'dark' : 'light' }] });
  await send('Storage.clearDataForOrigin', { origin: 'http://localhost:8080', storageTypes: 'local_storage' });
  await send('Page.navigate', { url: 'about:blank' });
  await sleep(200);
  // a page of the same origin without the app, to empty its storage for sure
  await send('Page.navigate', { url: 'http://localhost:8080/manifest.webmanifest' });
  await sleep(400);
  await run('localStorage.clear()');
  await send('Page.navigate', { url: 'http://localhost:8080/' + (s.hash || '') });
  await sleep(s.wait || 6500);
  // element shots: the sticky tab bar must not cover what was scrolled into view
  if (s.sel) await run(`document.querySelector('.tabs').style.setProperty('position', 'static', 'important')`);
  if (s.steps) {
    const r = await run(`(async () => { ${HELPERS} ${s.steps}; return 'ok'; })()`);
    if (r !== 'ok') console.log(s.name, 'steps:', r);
    await sleep(s.wait2 || 2500);
  }
  if (s.dump) { console.log('=== ' + s.name + '\n' + await run(DUMP)); continue; }
  let clip;
  if (s.sel) {
    const r = await run(`(async () => { ${HELPERS} let els = (${s.sel}); if (!Array.isArray(els)) els = [els]; els = els.filter(Boolean);
      if (!els.length) return null; let t = Infinity, l = Infinity, b = -Infinity, rr = -Infinity;
      for (const e of els) { const x = e.getBoundingClientRect(); t = Math.min(t, x.top + scrollY); l = Math.min(l, x.left + scrollX); b = Math.max(b, x.bottom + scrollY); rr = Math.max(rr, x.right + scrollX); }
      return { t, l, b, r: rr }; })()`);
    if (!r || typeof r === 'string') { console.log(s.name, 'no element', r); continue; }
    const pad = s.pad ?? 10;
    const y = Math.max(0, r.t - pad), x = Math.max(0, r.l - pad);
    clip = { x, y, width: Math.min(w, r.r + pad) - x, height: Math.min(r.b - r.t + 2 * pad, s.maxH || 5000), scale: 1 };
  } else {
    const H = s.full ? Math.min(await run('document.documentElement.scrollHeight'), s.maxH || 5000) : h;
    clip = { x: 0, y: s.y || 0, width: w, height: H, scale: 1 };
  }
  const shot = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true, clip });
  if (!shot.result) { console.log(s.name, JSON.stringify(shot.error)); continue; }
  writeFileSync(join(outDir, s.name + '.png'), Buffer.from(shot.result.data, 'base64'));
  console.log('saved', s.name, Math.round(clip.width) + 'x' + Math.round(clip.height));
}
ws.close(); edge.kill(); process.exit(0);
