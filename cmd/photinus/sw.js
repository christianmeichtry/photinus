// photinus panel service worker. It caches only the shell so the app opens
// offline and installs, and it never touches status data or cross-origin
// requests: status must always be live, and door failover lives in the
// page, not here.
const SHELL = "photinus-shell-v1";
const ASSETS = ["/", "/manifest.json", "/icon-180.png", "/icon-192.png", "/icon-512.png"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(SHELL).then((c) => c.addAll(ASSETS)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(keys.filter((k) => k !== SHELL).map((k) => caches.delete(k)))
    ).then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (e) => {
  const url = new URL(e.request.url);
  // Only the shell, only our own origin. Status and every failover door go
  // straight to the network so the page always sees the truth.
  if (e.request.method !== "GET" || url.origin !== self.location.origin || url.pathname.endsWith("status.json")) {
    return;
  }
  e.respondWith(
    caches.match(e.request).then((hit) => hit || fetch(e.request))
  );
});
