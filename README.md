# photinus

**Decentralized mesh monitoring for servers. Every node watches. No node is in charge.**

[photinus.dev](https://photinus.dev)

---

Most monitoring is a star: agents on every host, all reporting to one collector, with a
dashboard bolted on top. It works right up until the collector is the thing that dies, and
then you are blind at precisely the moment you needed to see.

Photinus inverts that. Every host runs a lantern. Lanterns watch each other, gossip what
they see to their neighbours, and raise an alert only when enough of them agree. There is
no collector, no dashboard host, no single point of failure and no single point of truth.

The name comes from *Photinus carolinus*, the firefly that flashes in unison across an
entire hillside with nothing coordinating it. Each insect only watches its neighbours.
Synchrony is the outcome, not the instruction. That is the same shape as a gossip protocol,
and it is the shape of this tool.

## Status

**Alpha.** It runs, and it has been watching a real fleet of five machines for a while
now, paging through real outages. What works today:

- Twelve check types: tcp, http, TLS certificates, disk, cpu, memory, swap, uptime,
  network rate, clock skew, peer liveness, and **pulse**, a heartbeat / dead man's
  switch: a cron job pings any lantern, and silence past a window becomes a
  quorum-agreed alert.
- Partition-safe quorum counting (a minority partition goes quiet instead of paging),
  each lantern the sole authority on its own local facts, and exactly-once paging
  through a hash-elected sender with flap damping.
- Encrypted gossip behind a shared swarm secret, a versioned wire format so mixed-release
  fleets coexist, and rolling upgrades proven box by box on the live fleet.
- A web panel served by every lantern, no dashboard host, answering on the same port
  the gossip already uses, from local memory, with the network on fire.
- Declared members: a box that is down, or never joined at all, is reported down
  instead of being invisible.

Not there yet: the YAML config file is in review (flags only until it lands), there is
no packaged installer (build from source below), Windows local probes report "not
applicable", and partition self-alerting is still an open problem, worked out in the
open in [`docs/design.md`](docs/design.md). Expect sharp edges and breaking releases.

If you run servers and this sounds useful, open an issue and tell me what breaks in
your current setup.

## Running it

```sh
go build ./cmd/photinus

# first box
./photinus run -id one

# every other box
./photinus run -id two -seed one.example.com:7946 -swarm-secret 'a shared passphrase'

# any box, any time
./photinus status
```

One binary, one open port (7946), and the standard local checks (disk, cpu, memory,
swap, uptime, network rate) run by default. Add `-watch` for services
(`-watch http:https://example.com`, `-watch cert:example.com`,
`-watch pulse:backup-db:26h`) and `-notify` to page.

## The idea, concretely

- **Lantern** the agent that runs on each host
- **Flash** a periodic heartbeat carrying that host's view of the mesh
- **Swarm** the set of lanterns that know about each other
- **Quorum** the fraction of the swarm that must agree before an alert fires

A single lantern losing sight of a host is data. Half the swarm losing sight of it is an
outage. The difference between those two is the whole product.

## How it works, with pictures

[photinus.dev/how.html](https://photinus.dev/how.html) walks the whole design in seven
diagrams and one flowchart: mesh versus star, the flash, the life of an alert, quorum,
the authority rule, exactly-once paging, heartbeats, and what a partition does.

## Design notes

See [`docs/design.md`](docs/design.md) for where the thinking currently stands, including
the parts that are unresolved.

## Repo layout

```
cmd/photinus/   the CLI: run and status
internal/       lantern, swarm, check, quorum, notify
site/           the landing page, one static file
docs/           design notes
```

## Licence

Apache 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

Apache rather than MIT for the explicit patent grant, which is what makes corporate legal
departments comfortable letting a daemon onto production infrastructure. That is the whole
audience for this tool, so the friction is worth removing.

Built in Sierre, Valais by [atelier agile](https://atelier-agile.ch).
