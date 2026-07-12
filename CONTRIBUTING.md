# Contributing

The project is pre-alpha. There is no working code yet, which changes what is useful.

## The most useful thing you can do right now

Open an issue describing **how you monitor servers today and what it gets wrong.** Fleet
size, what you run, what has actually woken you up at night, and what turned out to be a
false alarm. That is what the design is being built against, and it is worth more than a
patch at this stage.

If you have opinions about SWIM, gossip fan-out, or quorum under partition, read
[`docs/design.md`](docs/design.md) and tell me where it is wrong. The partition handling
and the notification-storm election are the two parts most likely to be wrong, and they
are both written down precisely so someone can argue with them.

## If you want to send code

- Read [`CLAUDE.md`](CLAUDE.md) first. It holds the vocabulary, the invariants, and the
  non-goals. A patch that quietly introduces a coordinator or a central collector will be
  rejected no matter how good it is, because that is the one thing this tool is not.
- `gofmt -l .` must print nothing. `go vet ./...` must be clean.
- Tests for anything touching quorum or state merging. That is where the bugs will live.
  Partition scenarios especially.
- Commit messages in the imperative mood, saying why rather than what.

## Vocabulary

Use the project's words: lantern, flash, swarm, quorum, check, observation. Not node,
agent, cluster, or collector. Consistency here is not pedantry, it is what keeps the
concepts distinct from every other monitoring tool.

## Licence

Contributions are accepted under Apache 2.0, the same licence as the project. By opening a
pull request you agree your contribution is licensed under it. There is no CLA.
