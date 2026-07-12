# photinus design notes

This is the thinking behind the code. When a design decision gets made in
code, it gets written down here. The brief and the non-negotiable rules live
in `CLAUDE.md`; this file records how the code currently honors them and what
was consciously deferred.

## Decisions made in the first milestone

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

## Verified behavior (the milestone)

Two lanterns on one machine, seeded at each other, both watching a TCP port
with nothing listening:

- both see the swarm as 2 lit,
- both independently report the target DOWN with 2/2 agreement,
- killing one lantern with SIGKILL leaves the other answering status
  instantly from local memory,
- after the dead lantern's observations age out, the survivor's single vote
  cannot reach a quorum of 2 and the alert clears: a minority goes quiet.

## Open problems, in rough order of next

1. `internal/notify` with deterministic hash election.
2. Graceful-leave shrinking of the ever-seen ledger, and alerting on
   partition (the two halves of the partition problem in CLAUDE.md).
3. The YAML config file and per-OS default paths.
4. Windows probes for the resource checks.
5. Binary flash encoding once observation counts grow. Skew and the resource
   checks added observations per lantern, so this is closer than it was.
