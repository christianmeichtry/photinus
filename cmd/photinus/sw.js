// photinus panel service worker. It caches only the shell so the app opens
// offline and installs, and it never touches status data or cross-origin
// requests: status must always be live, and door failover lives in the
// page, not here.
//
// The cache name carries the release (stamped in when served), so every
// new release is a byte-different worker: the browser installs it, the
// activate step below drops the old release's cache, and the next load
// serves the new shell instead of an old one forever.
const SHELL = "photinus-shell-@RELEASE@";
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
  // The page itself is network-first: an operations panel must show the
  // release the lantern actually runs, and the cache is only the offline
  // fallback. Icons and the manifest stay cache-first, they never change
  // within a release.
  if (url.pathname === "/") {
    e.respondWith(
      fetch(e.request).then((res) => {
        const copy = res.clone();
        caches.open(SHELL).then((c) => c.put(e.request, copy));
        return res;
      }).catch(() => caches.match(e.request))
    );
    return;
  }
  e.respondWith(
    caches.match(e.request).then((hit) => hit || fetch(e.request))
  );
});
