# Troubleshooting FAQ

Everything in here happened to a real fleet on day one. The common thread:
almost every swarm problem is a network problem, and the swarm's own view
tells you which one, once you know how to read it.

## The lantern dies at startup with "no private IP address found"

The box has only public IPs and memberlist refuses to guess which address
to tell peers about. Pass `-advertise`, usually as the box's DNS name:

```sh
photinus run -advertise $(hostname -f) -seed other-box:7946
```

## Everyone joins, then everyone goes dark within seconds

The classic cause: an advertised address peers cannot reach. Joining uses
the seed's address (works), but from then on peers probe each box at the
address it *advertises*. Two frequent poisons:

- Debian-family boxes map their own hostname to `127.0.1.1` in
  `/etc/hosts`, so a name-based advertise resolves to loopback. photinus
  refuses loopback and falls back to the box's outbound address, saying so
  in the log; check the `peers will reach this lantern at ...` line.
- A NAT'd or multi-homed box advertising an address the others cannot
  route to. The log line is the truth: if that IP is not reachable from
  the other boxes, fix the advertise.

## A box rejoins every 30 seconds, goes dark 6 seconds later, forever

The signature of **TCP working while UDP is blocked**, and the single most
common real-world failure. memberlist does a full state sync over TCP
every 30 seconds (that is the rejoin), but its liveness probes ride UDP
(that is the death 6 seconds later). Meanwhile the flapping box suspects
everyone else, because its own UDP probes go nowhere. Side effect: a down
and a recovered notification every 30 seconds.

Fix: open or forward **UDP 7946**, not just TCP. Firewalls and NAT rules
almost always get TCP right and forget UDP.

## `suspected by 1 of N, quorum M` on several boxes at once

One lantern is accusing everyone. That is almost never N sick boxes and
almost always one box whose network is broken outbound; its accusations
travel to the others over whatever path still works. Find the accuser
(`photinus status -v` lists who says what) and fix that box. The swarm
refusing to act on one accuser is rule 5 doing its job.

## The gossip port looks closed from the internet, but the swarm works

Fine, even good. Gossip only needs the swarm's boxes to reach each other;
the fewer strangers who can reach 7946, the better. With a swarm key set,
strangers cannot join anyway, but they do not need to see the port either.

## The box has an iptables allow-list with `policy DROP`

Then 7946 needs two explicit lines, one per protocol, and the second one
is the one people forget:

```sh
iptables -I INPUT -p tcp --dport 7946 -j ACCEPT
iptables -I INPUT -p udp --dport 7946 -j ACCEPT
```

Remember to persist them wherever that ruleset is generated, or they die
with the next reboot.

## Two boxes share one public IP (same office, same NAT)

One external port forwards to one box. Give the first box 7946, the next
one 7947 forwarding to its internal 7946, and tell it so:

```sh
photinus run -advertise office.example.com:7947 ...
```

Both TCP and UDP, per box.

## macOS: the lantern advertises something ending in `.local`

`hostname -f` on a Mac returns an mDNS name that only the local network
resolves, and worse, it can resolve to the self-assigned 169.254 address
of a dead interface. photinus refuses loopback and link-local addresses
and falls back to the outbound interface, but behind NAT even that is a
LAN address peers cannot route to. Pass the public name explicitly:
`-advertise mac.example.com` (with `:port` if it sits behind a shared
NAT). `-id mac` also reads better in status than `Mac.local`.

## After an upgrade, status still shows the old output

There are two photinus binaries on the box and the shell found the stale
one first; `~/.local/bin` usually beats `/usr/local/bin` in PATH. Check
with `which photinus`, remove the leftover, `hash -r`.

## A DARK box still shows fresh check values

Its outbound gossip still arrives over some path (one-way UDP is common
behind NAT), so its own reports stay fresh while nobody can probe it
alive. The rows to distrust are the ones marked `?`, "no fresh word":
that data is stale, not merely one-sided.

## `known` is bigger than the fleet

The swarm remembers every lantern it ever saw and does not forget yet,
so renamed or reinstalled lanterns leave ghosts that inflate the quorum
denominator. Harmless in small numbers, since the safe failure direction
is quiet. To reset, restart all lanterns within a short window; graceful
forgetting is on the roadmap.

## How do I get paged when a cron job stops running?

Declare a pulse (a heartbeat, a dead man's switch) on every box, `-watch pulse:backup-db:26h`, and have the
job ping any lantern when it finishes:

```sh
curl -H "Authorization: Bearer $TOKEN" http://any-lantern:7946/pulse/backup-db
```

Any lantern means any: the receipt gossips from whichever one took the
ping, so there is no special box to keep alive. When the pulse stays
silent past its window, the declared lanterns vote it down and quorum
pages once. Pinging a name before declaring it is fine; the reply says
"not declared on this lantern" until the flag lands. The door answers
wherever the panel does: on the gossip port when `-panel-token` is set,
and on any `-panel` address.

## Notifications repeat during flapping

Every down and every recovery notifies, so a box flapping on a half-open
firewall pages twice a minute. Fix the network problem first; flap
damping (saying "X is flapping" once instead) is on the roadmap.
