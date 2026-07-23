#!/bin/sh
# Relight this box's lantern on the new binary, reusing its exact running
# arguments. Run as the lantern's own user.
#
# Strand-proof by construction: once we have killed the old lantern we ALWAYS
# relaunch. An earlier version aborted if the gossip port did not free within
# a timeout, which on a slow-to-release kernel left the box dark past the 2m
# alert delay and paged a false outage. Grace does not save you from that page
# (an -expect box arms its alarm the instant it goes, Leave or not); only
# rejoining fast does. So: a short graceful window, then force, then relaunch.
set -eu

port_up() { ss -lnt 2>/dev/null | grep -q ':7946 '; }

# The pid must be the one holding the gossip port, not the nohup `sh -c`
# wrapper a previous relight left as its parent: killing the wrapper leaves
# the lantern alive and the port-wait spinning forever. Ancient kernels omit
# pid= from `ss -lntp`, so fall back to walking /proc for the real process.
PID=$(ss -lntp 2>/dev/null | sed -n 's/.*:7946 .*pid=\([0-9]*\).*/\1/p' | head -1)
if [ -z "${PID:-}" ]; then
  PID=$(pgrep -o -f '^[^ ]*photinus run' 2>/dev/null || true)
fi
if [ -z "${PID:-}" ]; then
  for p in /proc/[0-9]*/cmdline; do
    c=$(tr '\0' ' ' < "$p" 2>/dev/null) || continue
    case "$c" in
      sh\ -c*) : ;;
      *photinus\ run*) PID=$(basename "$(dirname "$p")"); break ;;
    esac
  done
fi
[ -n "${PID:-}" ] || { echo "no lantern pid found"; exit 1; }

CMD=$(tr '\0' ' ' < /proc/$PID/cmdline)
CMD=${CMD#sh -c }
case "$CMD" in
  *photinus\ run*) ;;
  *) echo "refusing to relight: recovered command looks wrong: $CMD"; exit 1 ;;
esac

if [ -f "$HOME/.local/bin/photinus.new" ]; then
  chmod +x "$HOME/.local/bin/photinus.new"
  mv "$HOME/.local/bin/photinus.new" "$HOME/.local/bin/photinus"
fi

# Graceful first (announce Leave), but only briefly; then force. From here on
# we never exit without relaunching.
kill -TERM "$PID" 2>/dev/null || true
i=0
while port_up; do
  i=$((i+1))
  [ $i -ge 20 ] && break          # ~10s graceful window
  sleep 0.5
done
if port_up; then
  echo "port 7946 still held after graceful window, forcing"
  kill -9 "$PID" 2>/dev/null || true
  j=0
  while port_up; do
    j=$((j+1))
    [ $j -ge 60 ] && break         # ~30s more for the kernel to release it
    sleep 0.5
  done
fi

# Secrets never live in this script or the repo: each box keeps them in
# mode-600 files the operator owns, and the run command carries neither
# the -swarm-secret nor the -swarm-token flag. The swarm token is optional; a box
# without the file simply serves no panel door.
PHOTINUS_SWARM_SECRET=$(cat "$HOME/.photinus.swarm-secret")
export PHOTINUS_SWARM_SECRET
if [ -f "$HOME/.photinus.swarm-token" ]; then
  PHOTINUS_SWARM_TOKEN=$(cat "$HOME/.photinus.swarm-token")
  export PHOTINUS_SWARM_TOKEN
fi

# Relaunch unconditionally -- never leave the box dark.
nohup sh -c "$CMD" >> /tmp/photinus.log 2>&1 &

# Wait for the listener rather than a fixed sleep: a slow kernel can take
# several seconds to bind, and `ss -lnt` needs no pid= so it works everywhere.
k=0
until port_up; do
  k=$((k+1))
  [ $k -ge 20 ] && break           # ~10s to come up
  sleep 0.5
done
if port_up; then echo "relit, listener up"; else echo "no listener after relight"; exit 1; fi
