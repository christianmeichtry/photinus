# Operating a fleet

The runbook for running photinus on real boxes. Everything here was
earned on the five-box test fleet; the traps at the bottom each cost an
outage or a leak to learn.

## The secrets model

Secrets live in mode-600 files owned by the lantern's user, never in
flags, never in scripts, never in the repo. The run command carries
neither `-key` nor `-panel-token`; both are read from the environment,
sourced from the files at start.

| Item | Location | Notes |
|---|---|---|
| swarm key | `~/.photinus.key` | required, same on every box |
| panel token | `~/.photinus.panel-token` | optional: no file, no panel door |
| binary | `~/.local/bin/photinus` | one static binary |
| relight script | `/tmp/photinus/relight.sh` or `/tmp/relight.sh` | from `scripts/relight.sh` |

Why files and not flags: flags show in `ps` for every user on the box,
and they end up in scripts that end up in repositories. Both happened.

## Bringing up a new box

```sh
# 1. secrets, pasted once
umask 077
printf '%s\n' 'THE-SWARM-KEY'   > ~/.photinus.key
printf '%s\n' 'THE-PANEL-TOKEN' > ~/.photinus.panel-token

# 2. binary
mkdir -p ~/.local/bin
# scp or curl the right build to ~/.local/bin/photinus, chmod +x

# 3. first light (no -key, no -panel-token: they come from the env)
PHOTINUS_KEY=$(cat ~/.photinus.key) \
PHOTINUS_PANEL_TOKEN=$(cat ~/.photinus.panel-token) \
nohup ~/.local/bin/photinus run \
  -advertise this-box.example.com \
  -expect box1 -expect box2 \
  -seed box1.example.com:7946 \
  >> /tmp/photinus.log 2>&1 &
```

Or, once the config file is in use: put everything including the key in
`/etc/photinus/photinus.yml`, chmod 600, and the whole command is
`photinus run`. The lantern warns at startup if the file holds the key
and other users can read it.

## Upgrading a box (relight)

```sh
# stage the new binary, then
scp photinus-linux-amd64 box:.local/bin/photinus.new
ssh box 'sh /tmp/relight.sh'
```

The relight script captures the running command, swaps the binary,
kills the old lantern, waits for the port, and restarts with the same
arguments and the secrets from the files. Roll one box at a time and
check the swarm count between boxes; mixed releases coexist by design.

## Rotating the panel token

Rolling, no downtime: the token guards each box's HTTP door
independently of gossip.

```sh
# per box: replace the file, relight
ssh box 'umask 077; cat > ~/.photinus.panel-token' < new-token-file
ssh box 'sh /tmp/relight.sh'
```

Clients (app, PWA doors) must re-enter the token afterward.

## Rotating the swarm key

NOT rolling: lanterns on different keys cannot gossip, and a rolling
restart builds two half-swarms that each declare the other dead. Two
phases, all boxes:

1. Stage everywhere first: new key into every `~/.photinus.key`, new
   binary if the rotation rides a release. Nothing restarts yet.
2. Stop every lantern, fast: `pkill -9 -f '[p]hotinus run'` on all
   boxes within a few seconds of each other.
3. Start every lantern (relight's start path, or the saved command).
   Fresh lanterns hold notifications through a warmup, so a quick
   all-stop/all-start pages nobody.

Expect a monitoring blackout of under a minute. That is the price of an
encrypted mesh with one shared key; pay it rarely and deliberately.

## Traps, each paid for

- **`pkill -f 'photinus run'` matches more than the lantern.** Run
  through ssh, the pattern matches the remote shell carrying the
  command string, killing it before it does its work; in a script it
  can match the script's own kill line. Always `'[p]hotinus run'`.
- **Never trace the start path.** `sh -x` prints the exported secrets
  into whatever is capturing the session. Diagnose start failures with
  echo lines, never with tracing.
- **The wrapper pid is not the lantern.** `nohup sh -c` leaves a shell
  wrapper whose cmdline also says "photinus run". Pick the pid that
  holds port 7946, not the first pgrep hit.
- **A stopped-then-started box needs its command saved first.** During
  an all-stop rotation there is no running process to capture arguments
  from. Save the command line before phase one.
- **/tmp does not survive everywhere.** macOS cleans it; anything a
  lantern needs at runtime (the exec pager script) either lives
  somewhere durable or gets checked by the stack check. Better: use
  `-notify-url` and no script at all.
- **Verify a rotation by the negative, not the positive.** The old
  token must 401 on every door; a lantern holding the old key must fail
  to join. "The new one works" alone proves nothing about the leak.

## The stack check

After any fleet operation, in order: every door 200 with the current
token and 401 with the previous one; swarm N lit of N with zero
subjects lacking a live word; the proxied panel and the site up; the
last 100 log lines per box free of fresh errors (fossils from the
operation itself are fine, read the timestamps); pager path or
notify-url intact on every box; repo clean, gate green.
