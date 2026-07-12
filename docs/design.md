# photinus design notes

Two parts. Part one is the original thinking, kept because it explains why the
design looks the way it does, annotated where the code has since settled a
question. Part two is the decision log: what actually got built and the
reasoning that survived contact with the implementation. The brief and the
non-negotiable rules live in `CLAUDE.md`.

## Part one: the thinking

### What a lantern does

Each host runs one lantern process. It:

1. Checks the things it is told to check (local: disk, load, memory, systemd units,
   certificates. Remote: TCP connect, HTTP status, whatever else).
2. Probes a handful of peers, not all of them. Fan-out is a constant, not N.
3. Gossips a compact view of what it believes to every peer it talks to.
4. Merges the views it receives.

Nothing is centralized. Every lantern can answer "what is the state of the swarm" from
local memory, which means `photinus status` works from any host, including during an
outage that has taken out half the fleet.

*Settled: this is what got built. Local checks so far are disk, cpu, memory,
swap, and uptime; systemd units and certificates are still to come.*

### Membership

Probably SWIM (Scalable Weakly-consistent Infection-style Membership), or a close variant.
It is the well-trodden path here: Serf and Consul both use it, the failure detector is
sound, and the false-positive handling (indirect probes before declaring a peer suspect)
is exactly the property that makes quorum alerting meaningful.

Not inventing a membership protocol. That is a solved problem and inventing one is how
this project would die.

*Settled: `hashicorp/memberlist`, wrapped thin in `internal/swarm`.*

### State merging

Each host's status is a versioned record. Merge is last-write-wins on a per-host lamport
counter, with the host itself as the authority on its own local checks. Observations
*about* a host by other hosts are kept separately and aggregated, since that is where
quorum lives.

Open question: how much history does a lantern keep? Enough to answer "when did this
start" without a database is the goal, but that trades against memory on small VPSes.

*Settled, with one deviation: merge is newest-wins on wall-clock timestamps
rather than lamport counters, and the skew check exists precisely to guard
those clocks. The authority split became the authority rule in
`internal/quorum`. The history question is still open; today a lantern keeps
only the newest observation per subject.*

### Quorum alerting

An alert has a required agreement threshold. Default: more than half the reachable swarm.

The interesting failure case is a network partition. If the swarm splits 7/6, both halves
believe the other half is down, and both may have quorum by their own count. Options:

- Require an absolute majority of the *last known* swarm size, not the reachable one.
  A minority partition then goes quiet rather than screaming. This is the Raft instinct
  and is probably right.
- Alert on the partition itself as a distinct event, which is arguably the most useful
  thing you could tell an operator anyway.

Leaning towards both.

*Settled: the last-known-size rule is implemented and tested against the 7/6
split. Alerting on the partition itself is still open.*

### Notification

The mesh agrees that something is wrong. Now what? Someone has to actually send the email
or the webhook, and if every lantern sends it you get 14 pages for one outage.

Cheapest correct answer: deterministic election by hash. The alert has an ID. The lantern
whose node ID is closest to `hash(alert_id)` and is alive sends it. No election protocol,
no leader, and it degrades correctly when that lantern is the one that is down (next
closest takes it).

*Settled: implemented as rendezvous hashing in `internal/notify`, including
the takeover when the elected sender dies mid-outage.*

### Non-goals

- Not a metrics store. No time series, no Grafana replacement. If you want graphs of CPU
  over six months, use Prometheus. This tool answers "is it up, and does the fleet agree".
- Not agentless. The whole design depends on peers watching peers.
- Not for one server. With N=1 there is no mesh and no quorum, and you should use a cron
  job and a curl.

### Open questions from the start, and where they landed

- Transport: gRPC, or raw UDP with a hand-rolled framing? UDP is what SWIM wants.
  *Settled: memberlist's own UDP gossip with TCP fallback carries everything.*
- Language: Go is the obvious fit (Serf's memberlist is right there, static binary, good
  concurrency story). Rust if the memory footprint on small hosts matters more.
  *Settled: Go.*
- Config: file, or gossiped through the mesh itself? The second is elegant and dangerous.
  *Settled: a file. Gossiped config makes the mesh a write channel into every
  host, which is a different threat model than reading each other's health.*
- How do you bootstrap? A seed list is boring and works. mDNS for LAN is nicer.
  *Settled for now: a seed list, retried in the background, where no seed
  matters after startup. mDNS remains a nice later idea.*

## Part two: decisions made in code

### Membership is memberlist, nothing more

`internal/swarm` wraps `hashicorp/memberlist` with its default LAN config.
Joining, failure detection, and gossip transport are memberlist's job. The
wrapper adds exactly three things: an ever-seen ledger for the last-known-size
rule, a broadcast queue for flashes, and a retry loop for seeds that are not
up yet. A seed that is unreachable at startup is retried every 5 seconds until
the first peer connects; after that, seeds stop mattering, which keeps rule 1
honest.

### Last known swarm size is an ever-seen ledger

Quorum is a majority of the last known swarm size, not of the currently
reachable members. The current implementation of "last known" is the set of
lantern names ever seen alive, including self, and it never shrinks. A crashed
lantern still counts toward the denominator, which is what makes a minority
partition go quiet.

Consciously deferred: shrinking the ledger on *graceful* leave. Until that
lands, decommissioning a lantern permanently inflates the denominator and a
swarm that shrinks a lot will refuse to alert. This is the safe failure
direction (quiet, not screaming), but it needs fixing before real use. Also
deferred: alerting on the partition itself.

### Flashes carry only the sender's own observations

A flash is a JSON array of the sender's own observations, queued on
memberlist's TransmitLimitedQueue. memberlist re-gossips each message to a
constant sample of peers per round, so propagation is swarm-wide while fan-out
stays constant (rule 3). Lanterns do not re-encode or forward what they merged
from others; the transport already does that, and forwarding merged state
would blur rule 4's line between own facts and aggregated hearsay.

On receive, the newest observation per (observer, check, target) wins.
Observations claiming to come from the receiving lantern are dropped: a
lantern is the sole authority on its own checks and no peer may overwrite
that, accidentally or otherwise.

Consciously deferred: a compact binary encoding. JSON is fine for a handful of
checks; it will not be fine for hundreds, since flashes ride in UDP gossip
packets of roughly 1400 bytes. Also deferred: full state sync on join via
memberlist's push/pull hooks (LocalState/MergeRemoteState are stubs). A late
joiner converges within one flash interval anyway, since flashes repeat.

### Remote swarms: advertise and the shared key

Two flags make multi-machine swarms workable. `-advertise host[:port]` tells
peers where to reach a lantern when the bind address is not what the world
sees (NAT, several interfaces); without it memberlist guesses, which is right
on simple LAN boxes and wrong almost everywhere else. `-key` is a shared
swarm secret: it switches on memberlist's gossip encryption and doubles as
the join gate, since a lantern without the key cannot decrypt anything and
is refused. The cipher key is derived from the passphrase with SHA-256, so
operators pick a sentence instead of minting exactly 32 bytes. The key also
reads from `$PHOTINUS_KEY` so it does not have to sit in `ps` output.

Consciously deferred: per-lantern identity or certificates. One shared key
means one trust zone, which matches the current design assumption that the
swarm runs on a network you already trust with your monitoring. Also still
using memberlist's LAN timing profile; geographically spread swarms over
WAN links will want the WAN profile once that becomes real.

### Status is a unix socket, answered from memory

`photinus status` talks to the local lantern over a unix socket
(`photinus-<id>.sock` in the temp dir by default) and the lantern answers
entirely from its in-memory store (rule 2). Nothing in the status path opens a
network connection. The same handler is the natural seed of the future
read-only HTTP status API that the mobile app will use; when that lands it
must remain something any lantern can serve.

### Observations age out

An observation older than five flash intervals stops counting toward quorum.
A dead lantern's last opinion therefore fades instead of voting forever. The
age cut lives in `quorum.Decide` and is applied at decision time, not at
merge time, so the store stays a plain newest-wins map.

### The authority rule: local checks do not need quorum

Only one lantern can see its own disk, so a local check could never reach a
majority. Rule 4 already contains the answer: a lantern is the sole authority
on its own local checks. In code, local checks set their observation's target
to the lantern's own ID, and `quorum.Decide` treats an observation whose
observer equals its target as authoritative: it decides alone, needs no
agreement, and hearsay about the same subject never mixes with it. If the
authority's observation goes stale (the host died with its lantern), the
subject falls back to ordinary quorum, which in practice means the tcp checks
and membership take over the story of that host.

### Resource checks and what they measure

`uptime`, `disk`, `cpu`, `memory`, and `swap` are threshold checks on the
current value, per the non-goal of never storing history. Platform probes
live behind build tags; on platforms without a probe (currently Windows,
which is on the roadmap) they report "not applicable" and never fail.

- Linux reads `/proc`: uptime, meminfo for memory and swap, and cpu
  utilization as the delta of `/proc/stat` ticks between flashes, which is
  real utilization averaged over one interval.
- macOS reads sysctls. Memory leans on `kern.memorystatus_level`, the
  kernel's own estimate of available memory in percent; counting free pages
  would read a healthy Mac as nearly full, because file cache is kept hot on
  purpose. CPU has no tick counters in sysctl, so the probe approximates
  utilization as the one minute load average spread over the cores; it
  overcounts when processes wait on disk and undercounts short bursts, both
  fine for a threshold check. Swap parses `vm.swapusage` and remembers that
  macOS resizes swap dynamically.
- A host with no swap configured reports OK, not "not applicable": nothing
  can fill up, which is the healthy answer.

### Skew, the relational check

Skew measures clock drift between lanterns from the timestamps flashes
already carry, and it is the reason the check lives in `internal/lantern`
rather than `internal/check`: it produces one observation per peer and needs
the received flashes to do it. A flash stamped S arriving at local time A
gives A minus S, which is the true offset plus transit delay. Delay is always
positive, so the minimum over a sliding window of fresh flashes approaches
the true offset; re-gossiped old flashes are filtered out by only sampling
stamps newer than anything heard from that peer before.

The aggregation does the diagnosis without any extra machinery. A peer with
a wrong clock is observed skewed by every lantern and quorum convicts it. A
lantern whose own clock is wrong accuses everyone, convinces nobody, and is
itself convicted by the rest of the swarm. Skew also matters for a selfish
reason: observation aging compares peer stamps against the local clock, so a
lantern drifted beyond maxAge silently loses its vote. The default threshold
of 5 seconds sits inside the default aging window of 10 seconds.

### Warnings are not outages

A disk at 91% has not taken anything down: the host is up and its lantern is
the one reporting. Calling that DOWN would train operators to ignore DOWN.
So subjects have three states. Resource checks and skew can only reach WARN;
DOWN is reserved for reachability checks confirmed by quorum. The status
table sorts DOWN above WARN above up, and notifications carry the split as
kinds: down, warning, recovered (up after down), cleared (up after warning).
An escalation from warning to down notifies again; the way back down the
ladder notifies as what it now is. Colors in the status output are
decoration, never information: everything they say is also in the words, so
pipes and NO_COLOR lose nothing.

### The lantern check: membership becomes a subject

Membership always knew who stopped answering; it just never told quorum.
The lantern check closes that loop: every flash, each lantern reports every
known peer (the ever-seen roster minus itself) as up or down according to
its own membership view, as ordinary aggregated observations under the
check name "lantern". A dead host is then convicted by quorum and paged
like anything else, with no configuration. Nobody reports on themselves, so
the subject can never be authoritative: a partition's minority can see the
majority as dead all it wants, it will never reach quorum about it.

This is also why the standard local checks now run by default: one binary
plus a seed equals a monitored host, which is the installation story the
brief promised. `-watch` adds or overrides, `-defaults=false` opts out.

### Flashes split before the packet does

With defaults, skew, and liveness, a five-host swarm puts around fourteen
observations in a flash, and the JSON outgrows the roughly 1400 byte UDP
gossip packet. A broadcast that does not fit never leaves memberlist's
queue, and the failure mode is silence. So a flash is chunked: observations
are packed greedily into payloads capped at 1000 bytes and each chunk
gossips independently. Merging is per observation and does not care how
they arrive. The binary encoding stays on the roadmap; chunking makes the
current encoding safe rather than efficient.

### Website checks: http and cert

Both are remote checks like tcp, so quorum, notification, and status
needed nothing new. The verdict mapping is deliberately strict for http:
2xx and 3xx are up and everything else is down, including 4xx, because
the operator watches a URL expecting it to work, and a 404 on that URL is
a broken thing regardless of how alive the server process is. TLS
verification stays on for the same reason: a certificate browsers reject
means users cannot reach the site, and the check must not be more
forgiving than the users are.

The cert check is where warn against down earns its keep: broken,
mis-hosted, or expired certificates are outages (browsers block on the
spot), while a certificate inside the warning window (7 days by default,
per-check overridable) warns days before the outage would happen. The
expiry judgment is a pure function over the leaf certificate so the
clock-dependent logic is table-tested without a network.

### The web panel: a dashboard with no dashboard host

`-panel addr` makes a lantern serve a read-only HTML panel plus
`/status.json`, both answered from local memory like everything else.
The point is what it is not: there is no dashboard host, because every
lantern serves the same panel about the same swarm, so pointing a browser
(or DNS round-robin) at any box works and any box can die. The page is a
single embedded file with no external dependencies, state words carry the
information and color only decorates, and it is unauthenticated by design:
loopback-bound by default in the fleet script, and anything public
belongs behind a reverse proxy with auth. This is also the read API the
future mobile app rides.

### Notification: hash election, no protocol

Every lantern detects the same outages, so the problem is not sending a page
but sending exactly one. The election is rendezvous hashing: each alive
lantern gets a score from hashing its ID with the alert, the highest score
wins, and everyone computes the same winner from the same facts. There is no
election protocol, no term counters, no coordinator, nothing to time out.
When the winner is dead it is simply absent from the alive list and the next
score wins by the same arithmetic.

The tracker on each lantern fires only on state transitions: down when a
subject newly reaches quorum, recovered when it stops. Three edges got
explicit treatment:

- **Startup.** A lantern that just started is a swarm of one and its own
  vote is a quorum, so the tracker stays quiet for a warmup equal to the
  observation aging window. After warmup, an already-down subject does page:
  a fresh swarm facing a dead host should say so, and a restarted lantern
  occasionally re-paging an ongoing outage is the right side of that trade.
- **Unknown is not recovered.** A subject with zero live observations keeps
  its last known state and produces nothing. Stale is silence, not good
  news.
- **The elected sender dies mid-outage.** Survivors cannot know whether the
  page got out before the death, so the next winner sends it again. A
  duplicate page beats a silent outage, deliberately. Membership growth
  alone never re-pages; only the recorded sender's death does.

The outbound channel is one exec'd command with four arguments: kind, check,
target, and a sentence. No retry: a notification channel that needs retries
needs fixing. More channels (mail, webhooks) can come as more Senders, and a
second channel is explicitly not wanted until one alert reliably arrives
exactly once.

Under a partition both sides still make independent decisions, but the
minority cannot reach quorum at all (the last-known-size rule), so it also
cannot page. Exactly-once is therefore per connected majority, which is the
strongest claim a coordinator-free design can honestly make.

## Verified behavior

Two lanterns on one machine, seeded at each other, both watching a TCP port
with nothing listening:

- both see the swarm as 2 lit,
- both independently report the target DOWN with 2/2 agreement,
- killing one lantern with SIGKILL leaves the other answering status
  instantly from local memory,
- after the dead lantern's observations age out, the survivor's single vote
  cannot reach a quorum of 2 and the alert clears: a minority goes quiet.

Three lanterns with `-notify` watching the same dead port:

- exactly one page fires, from the lantern every log independently names as
  the winner,
- bringing the port up produces exactly one recovered page,
- killing the elected sender mid-outage produces exactly one takeover page
  from the next winner.

Encrypted swarms: two lanterns sharing a key form a swarm; a third with the
wrong key is refused with a plain reason in its log.

## Open problems, in rough order of next

1. The YAML config file and per-OS default paths; the flag list has grown
   past what a systemd unit should carry inline.
2. Graceful-leave shrinking of the ever-seen ledger, and alerting on
   partition (the two halves of the partition problem in CLAUDE.md).
3. Windows probes for the resource checks.
4. Binary flash encoding once observation counts grow. Skew and the resource
   checks added observations per lantern, so this is closer than it was.
5. More notification channels as additional Senders, mail first.
6. How much history a lantern keeps, so status can answer "when did this
   start" without becoming a database.
