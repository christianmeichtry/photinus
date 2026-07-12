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

1. Graceful-leave shrinking of the ever-seen ledger, and alerting on
   partition (the two halves of the partition problem in CLAUDE.md).
2. The remaining check types: uptime, disk, cpu, memory, swap locally, skew
   from peer flash timestamps.
3. `internal/notify` with deterministic hash election.
4. The YAML config file and per-OS default paths.
5. Binary flash encoding once observation counts grow.
