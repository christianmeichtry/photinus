# CLAUDE.md

Context for Claude Code working in this repo. Read this first, every session.

## What this is

**photinus** is decentralized mesh monitoring for servers. Every host runs an agent. The
agents watch each other, gossip what they see, and alert only when enough of them agree.
There is no central collector and no dashboard host.

Named after *Photinus carolinus*, the firefly that flashes in unison with nothing
coordinating it. Every design decision should be checkable against that: **is this thing
still true if any single node disappears?** If the answer is no, it is wrong.

Status: pre-alpha. The mesh works: lanterns gossip, agree, and run all seven
check types. No notification yet, no config file.

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

Every check is threshold-based: it produces an observation that is OK or not-OK, never a
stored number over time. All built-in checks exist; Windows probes for the local ones are
still on the roadmap (they report "not applicable" there until then).

| Check | What it tests | Kind |
|---|---|---|
| **tcp** | dial a `host:port` | remote |
| **uptime** | host uptime, flags a reboot since the last flash | local |
| **disk** | filesystem usage percent against a threshold | local |
| **cpu** | CPU utilization percent (short rolling average) against a threshold | local |
| **memory** | RAM utilization percent against a threshold | local |
| **swap** | swap/pagefile utilization percent, an early warning before OOM | local |
| **skew** | clock drift between this lantern and the swarm's flashes | relational |

Resource checks (`uptime`, `disk`, `cpu`, `memory`, `swap`) are *local* facts: rule 4
applies, a lantern is the sole authority on its own resource checks and gossips them as
observations. In code that means their observations target the lantern's own ID, and
quorum treats observer-equals-target as authoritative: the host's own word decides, no
agreement needed, hearsay never mixes in. `skew` is different: it is computed from the
timestamps on peer flashes, so it is inherently about the mesh rather than any single
host (which is also why it lives in `internal/lantern`, not `internal/check`). It is the
most on-brand thing photinus can measure, since the whole project is named after
synchronized flashing.

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

A swarm that pages you exactly once. `internal/notify` with the deterministic
hash election from the hard-problems section: when quorum convicts a subject,
the alive lantern whose ID hashes closest to the alert sends the one
notification, and nobody else does. Start with a single outbound channel
(exec a command with the alert as arguments is enough to prove it), and test
the degraded case where the elected lantern is the dead one.

Done so far: the mesh (two lanterns agree a fake host is down, verified), all
seven check types, the authority rule for local checks, skew from flash
timestamps. The history is in docs/design.md.

No YAML config and no second notification channel until one alert reliably
arrives exactly once.

## Repo layout beyond the code

- `site/index.html` is the landing page, a single static file, no build step. If you touch
  it, it stays a single file with no dependencies.
- `docs/design.md` is the thinking. When a design decision gets made in code, update it.
