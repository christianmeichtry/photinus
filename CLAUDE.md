# CLAUDE.md

Context for Claude Code working in this repo. Read this first, every session.

## What this is

**photinus** is decentralized mesh monitoring for servers. Every host runs an agent. The
agents watch each other, gossip what they see, and alert only when enough of them agree.
There is no central collector and no dashboard host.

Named after *Photinus carolinus*, the firefly that flashes in unison with nothing
coordinating it. Every design decision should be checkable against that: **is this thing
still true if any single node disappears?** If the answer is no, it is wrong.

Status: pre-alpha. The mesh works end to end: lanterns gossip, agree, run all
seven check types, and the elected lantern pages exactly once. No config file
yet, flags only.

## Vocabulary (use these words in code, comments, docs, CLI, and commit messages)

| Term | Means |
|---|---|
| **lantern** | the agent process running on one host |
| **flash** | one periodic heartbeat carrying a lantern's view of the world |
| **swarm** | the set of lanterns that know about each other |
| **quorum** | how much of the swarm must agree before an alert fires |
| **check** | one thing being tested (disk, HTTP endpoint, systemd unit) |
| **observation** | one lantern's opinion about one check on one host |

Do not introduce "node", "agent", "cluster", "peer group", or "collector" as user-facing
nouns. `peer` is fine internally. The vocabulary is the brand; keep it consistent.

## Stack

- **Go 1.22+**. Chosen for `hashicorp/memberlist` (a battle-tested SWIM implementation),
  static single-binary distribution, and painless cross-compilation to the small boxes
  this is aimed at.
- **`hashicorp/memberlist`** for membership and failure detection. **Do not write a
  membership protocol.** That is a solved problem and hand-rolling one is how this project
  dies. Wrap it, do not replace it.
- Standard library first. Every dependency needs a reason, and the reason cannot be
  convenience.
- Config: YAML, one file. Default location is per OS:
  - Linux: `/etc/photinus/photinus.yml`
  - macOS: `~/Library/Application Support/photinus/photinus.yml` (fallback
    `/usr/local/etc/photinus/photinus.yml`)
  - Windows: `%ProgramData%\photinus\photinus.yml`

  Always overridable with `--config`.

## Cross-platform and architecture

- **Cross-platform and multi-arch.** Linux first (amd64, arm64, arm, think Raspberry Pis
  and small VPS boxes), with macOS and Windows as first-class targets later. Go's
  cross-compilation makes one `go build` per `GOOS`/`GOARCH` produce a single static
  binary with no runtime dependencies.
- **Platform-specific checks must degrade, not fail.** A check that cannot run on the
  current OS (a systemd unit on Windows, say) detects that and reports "not applicable".
  It never errors and never takes the lantern down with it.

## Installation

Simple by design. Download one static binary for your platform, drop it on the host, and
run `photinus run` with a seed list. No runtime, no interpreter, no package manager
required. One binary per platform, nothing else. If installation ever needs a second step
beyond "put the binary somewhere and start it", push back and say so.

## Architecture

```
cmd/photinus/        CLI entrypoint (lantern run, status, swarm, check)
internal/lantern/    the agent loop: check, gossip, merge
internal/swarm/      memberlist wrapper, peer state
internal/check/      check implementations, one file per type
internal/quorum/     agreement logic and alert decisions
internal/notify/     outbound notification, hash-elected sender
```

The loop, in one breath: a lantern runs its local checks, probes a constant-size sample of
peers (never all of them, fan-out must not scale with N), gossips a compact view, merges
what it receives, and asks the quorum package whether the swarm now agrees that something
is broken.

## Checks (built-in types)

Every check is threshold-based: it produces an observation, never a stored number over
time. All built-in checks exist; Windows probes for the local ones are still on the
roadmap (they report "not applicable" there until then).

**A warning is not an outage.** Resource checks (disk, cpu, memory, swap, uptime, net)
and skew can only WARN when past their threshold: the host is up and its lantern is
reporting, so the words must never say down. DOWN is reserved for reachability checks
(tcp, and future ones like http) confirmed by quorum. Notifications follow the same
split: kinds are "down", "warning", "recovered" (up after down), and "cleared" (up
after a warning).

| Check | What it tests | Kind |
|---|---|---|
| **tcp** | dial a `host:port` | remote |
| **http** | fetch a url; 2xx/3xx is up, anything else is down | remote |
| **cert** | TLS certificate health; broken or expired is down, expiring soon warns | remote |
| **pulse** | a heartbeat / dead man's switch: a job pings any lantern, silence past the window is down by quorum | remote |
| **uptime** | host uptime, flags a reboot since the last flash | local |
| **disk** | filesystem usage percent against a threshold | local |
| **cpu** | CPU utilization percent (short rolling average) against a threshold | local |
| **memory** | RAM utilization percent against a threshold | local |
| **swap** | swap/pagefile utilization percent, an early warning before OOM | local |
| **net** | traffic rate on the default-route interface; reports always, warns only past an optional Mbit/s limit | local |
| **skew** | clock drift between this lantern and the swarm's flashes | relational |
| **lantern** | every known peer's liveness, from membership, automatic | relational |

**The standard local checks run by default** (`disk:/`, `cpu`, `memory`, `swap`,
`uptime`, `net`): one binary and a seed gives a fully monitored host. `-watch` adds more
(extra disks, tcp targets) or overrides a default's threshold by naming it;
`-defaults=false` opts a box out. `skew` and `lantern` need no flags at all: skew is
measured from flashes, and the lantern check is the mesh watching itself, membership
turned into quorum-decided subjects so a dead host pages with zero configuration.

Resource checks (`uptime`, `disk`, `cpu`, `memory`, `swap`, `net`) are *local* facts: rule 4
applies, a lantern is the sole authority on its own resource checks and gossips them as
observations. In code that means their observations target the lantern's own ID, and
quorum treats observer-equals-target as authoritative: the host's own word decides, no
agreement needed, hearsay never mixes in. `skew` is different: it is computed from the
timestamps on peer flashes, so it is inherently about the mesh rather than any single
host (which is also why it lives in `internal/lantern`, not `internal/check`). It is the
most on-brand thing photinus can measure, since the whole project is named after
synchronized flashing.

## Wire compatibility (binding from 0.0.2 on)

Fleets upgrade box by box, so a lantern must always coexist with the
previous release. The rules:

- Flashes ride in a versioned envelope (`{"v":1,"obs":[...]}`). Within a
  wire version, changes are **additive only**: new optional fields, never
  renamed or re-meant ones. Anything else bumps `v`.
- A lantern accepts its own wire version and one back, and **drops** what
  it does not understand with a log line. It never guesses: wrong
  monitoring conclusions are worse than missing ones.
- Every lantern announces its release in memberlist node metadata;
  `status` shows versions whenever the swarm is mixed.
- The notify command's arguments (`kind check target sentence`) are a
  public contract: append new arguments at the end, never reorder.

## Rules that are not negotiable

1. **No single point of failure, including in the code.** No "primary" lantern, no elected
   coordinator that holds state, no bootstrap node that matters after startup.
2. **`photinus status` must work from any host, from local memory, with the network on
   fire.** If answering a status query requires talking to another machine, it is broken.
3. **Fan-out is constant, not O(N).** A lantern gossips to a fixed sample of peers.
4. **A lantern is the sole authority on its own local checks.** Observations *about* other
   hosts are separate data and get aggregated. Never merge the two.
5. **Alerts fire on agreement, not on opinion.** One lantern seeing a host as down is a
   fact about that lantern. Half the swarm seeing it is an outage.

## Known hard problems (do not paper over these)

- **Partition.** A 7/6 split leaves both halves believing the other is dead, and both may
  reach quorum by their own count. Intended fix: require a majority of the *last known*
  swarm size, not the currently reachable one, so a minority partition goes quiet rather
  than screaming. Also alert on the partition itself, since that is the most useful thing
  you can tell an operator.
- **Notification storms.** If every lantern sends the email, one outage means fourteen
  pages. Intended fix: deterministic hash election. The lantern whose ID is closest to
  `hash(alert_id)` and is alive sends it. No election protocol, degrades correctly when
  that lantern is itself the one that is down.

If you find yourself writing code that quietly sidesteps either of these, stop and say so.

## Non-goals

- Not a metrics store. photinus evaluates current resource metrics (cpu, disk, memory,
  swap, uptime) as pass/fail checks and alerts on thresholds, but it keeps no history, no
  time series, and no dashboards. If you want graphs over time, Prometheus exists.
  photinus tells you "is it broken right now, and does the swarm agree", not "what did it
  look like last Tuesday".
- Not agentless. The design depends on peers watching peers.
- Not for a single server. With one host there is no mesh and no quorum. Use cron and
  curl.

## Roadmap

- **Mobile app (later).** A phone app will display swarm status and notify operators of
  issues. It is a *read client*, not a new central host: it queries any lantern (rule 2,
  answered from that lantern's local memory) and receives pushes through the existing
  `notify` path. This means lanterns will eventually expose a small read-only status API
  (HTTP/JSON) that any lantern can serve. It must never become a dashboard host or a
  single point of failure. If the app design starts to require "the one server the app
  talks to", that is wrong.

## Conventions

- Errors wrapped with context: `fmt.Errorf("probing peer %s: %w", id, err)`.
- No `panic` outside `main`.
- Table-driven tests. The quorum and merge logic is where bugs will actually live, so that
  is where the tests go. Test partition scenarios explicitly.
- Log lines are for operators, not for developers. Plain sentences, no stack traces at INFO.
- Commit messages: imperative mood, and say *why*, not *what*. The diff already says what.
- **Never use long dashes in any prose, comments, docs, or copy.** Short dashes or rephrase.

## Commands

```sh
go build ./cmd/photinus       # build
go test ./...                 # test
go vet ./...                  # vet
gofmt -l .                    # must print nothing
```

## Current milestone

The config file. One YAML file at the per-OS default path (see Stack),
covering everything the flags do today: identity, bind/advertise, seeds, key,
checks with thresholds, notify command, intervals. Flags stay and win over
the file, so nothing breaks. The file is the deployment story: a systemd
unit or launchd plist should say `photinus run` and nothing else.

Done so far: the mesh, all seven check types, the authority rule for local
checks, skew from flash timestamps, remote swarms (-advertise, -key), and
notification with hash election, verified to page exactly once including the
takeover when the elected sender dies. The history is in docs/design.md.

## Repo layout beyond the code

- `site/index.html` is the landing page, a single static file, no build step. If you touch
  it, it stays a single file with no dependencies.
- `docs/design.md` is the thinking. When a design decision gets made in code, update it.
- `docs/faq.md` is troubleshooting knowledge earned on real fleets. When a support
  question gets answered twice, it goes there.
