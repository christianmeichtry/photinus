# Roadmap

Post-freeze, rough priority. The freeze holds until these are deliberately picked up.

## Correctness / core
- **Partition self-alerting** — the last open hard problem from the brief: alert on the split itself, minority goes quiet.
- **Reliable farewells** — send the leave over TCP push/pull, not best-effort gossip; proven lossy at N=40.
- **Takeover re-page dedup/cap** — the page-storm amplifier: a membership change re-pages every non-up subject; cap or dedup when subjects are legion.

## Scale (from the N=40 experiment; needed beyond ~30 lanterns)
- **Binary wire encoding** — JSON is the CPU and memory king at scale; biggest single win.
- **Flash interval + retransmit scaling with N**; pace per-peer relational (liveness, skew) observations.
- **Delta anti-entropy** instead of full-store push/pull syncs (addresses the ~25x per-lantern RSS growth).

## Operator experience
- **Onboarding: stateless config + install builder.** A static page on photinus.dev builds the first box's `photinus.yml` and its `curl | sh` install line, entirely client-side; no signup, no backend, no stored configs. The swarm secret is generated or entered at install time on the machine and never touches the server. Growing a swarm happens from the **panel**: an "add a box" button pre-fills seeds and endpoints (the panel already knows them) and emits the curl plus config for a new box, with the operator pasting the secret. Every lantern can mint onboarding for the next; no central host, no secret custody server-side.
- **TCP-only mode** — one firewall rule instead of TCP+UDP; open question from the fleet debugging.
- **Maintenance-aware relight** — roll a fleet without paging (tell the swarm it is planned).
- **Hysteresis band and alert delay as finer config knobs** (band width currently fixed at 10 points).
- **Windows local probes**; **swarm.sh bootstrap refresh** (still carries the pre-file-secrets model).

## Product
- **iOS app phase 2** — in-app ntfy push subscription, StoreKit "Pro" tier; then self-signed TLS on the mux with cert pinning in the app.
- **Apple TV wall panel (tvOS).** A read-only, always-on swarm display for an office or home wall, a new target in the photinus-app repo reusing the iOS client (SwarmClient, models, door failover, palette); only the 10-foot views are new. The firefly constellation is the hero, big state pills, red wash on a real DOWN, no notifications (a TV is not a pager). The one hard part is door and token entry without a keyboard: pair via the shared iCloud keychain from the signed-in phone, no typing. A constellation screensaver is a bonus. Operator has several Apple TVs on hand, so this is a wanted personal display, not hypothetical.
- **Public release** — undraft the binaries, restore `install.sh` on the docroot, announce.

## Done (recent, for context)
Config file, pulse heartbeat, built-in ntfy notifier, load-average cpu, alert delay, hysteresis, secrets-in-files with rotation, the swarm-secret/swarm-token rename, CI gate with fuzzing and vuln scanning, ops runbook.
