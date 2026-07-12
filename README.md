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

Pre-alpha, but it runs. A swarm of lanterns can watch each other and real checks (tcp,
disk, cpu, memory, swap, uptime, clock skew), agree by quorum with partition-safe
counting, and page exactly once through a hash-elected sender. Swarms can span machines,
with encrypted gossip behind a shared key. No config file yet (flags only), no packaged
releases, and the design is still being worked out in the open. If you run servers and
this sounds useful, open an issue and tell me what breaks in your current setup.

## The idea, concretely

- **Lantern** the agent that runs on each host
- **Flash** a periodic heartbeat carrying that host's view of the mesh
- **Swarm** the set of lanterns that know about each other
- **Quorum** the fraction of the swarm that must agree before an alert fires

A single lantern losing sight of a host is data. Half the swarm losing sight of it is an
outage. The difference between those two is the whole product.

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
