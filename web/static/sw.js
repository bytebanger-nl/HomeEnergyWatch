// Service worker: keeps the app shell available so the app opens instantly
// (and explains that it is offline when you are not at home). Live data and
// history always come from the network; they are never cached.
const CACHE = "hwlocal-v45";
const SHELL = [
  "/", "/index.html", "/app.css", "/errors.js", "/app.js", "/live.js", "/history.js", "/devices.js",
  "/plugins.js", "/klimaat.js", "/tiles.js", "/rules.js", "/actions.js", "/charts.js", "/util.js", "/prices.js", "/costs.js", "/usage.js", "/heating.js", "/security.js", "/settings.js", "/reports.js", "/backup.js", "/outages.js", "/berichten.js", "/manifest.webmanifest", "/icons/icon.svg", "/icons/icon-192.png",

];

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== "GET" || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith("/api/") || url.pathname === "/ca.crt") return; // network only

  // network first, so updates of the gateway show up immediately; fall back
  // to the cached shell when the gateway is unreachable
  event.respondWith(
    fetch(event.request)
      .then((res) => {
        if (res.ok) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(event.request, copy));
        }
        return res;
      })
      .catch(() => caches.match(event.request).then((hit) => hit || caches.match("/index.html"))),
  );
});

// Web Push: alerts of the rules (see rules.js), also when the app is closed.
self.addEventListener("push", (event) => {
  let m = {};
  try { m = event.data ? event.data.json() : {}; } catch { m = { body: event.data.text() }; }
  event.waitUntil(self.registration.showNotification(m.title || "Home Energy Watch", {
    body: m.body || "",
    tag: m.tag || undefined,
    renotify: Boolean(m.tag),
    icon: "/icons/icon-192.png",
    badge: "/icons/icon-192.png",
    data: { url: m.url || "/" },
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const url = new URL(event.notification.data?.url || "/", self.location.origin).href;
  event.waitUntil((async () => {
    const wins = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    for (const w of wins) {
      if (new URL(w.url).origin === self.location.origin) {
        await w.focus();
        return w.navigate ? w.navigate(url) : undefined;
      }
    }
    return self.clients.openWindow(url);
  })());
});

// the push service replaced the subscription: hand the new one to the gateway
self.addEventListener("pushsubscriptionchange", (event) => {
  event.waitUntil((async () => {
    const old = event.oldSubscription;
    const sub = event.newSubscription || (old && await self.registration.pushManager.subscribe(old.options));
    if (!sub) return;
    await fetch("/api/push/subscriptions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...sub.toJSON(), label: "App (vernieuwd)" }),
    });
    if (old) {
      await fetch("/api/push/subscriptions", {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ endpoint: old.endpoint }),
      });
    }
  })());
});
