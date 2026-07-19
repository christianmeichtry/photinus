#!/bin/sh
# Relight this box's lantern on the new binary, reusing its exact running
# arguments. Run as the lantern's own user.
#
# The pid must be the one holding the gossip port, not the nohup sh -c
# wrapper a previous relight left as its parent: killing the wrapper leaves
# the lantern alive and the port-wait spinning forever.
set -eu
PID=$(ss -lntp 2>/dev/null | sed -n 's/.*:7946 .*pid=\([0-9]*\).*/\1/p' | head -1)
[ -n "$PID" ] || PID=$(pgrep -o -f '^[^ ]*photinus run')
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
# The bracket keeps the pattern from matching this script's own kill
# command, which "photinus run" as a plain string would.
pkill -9 -f '[p]hotinus run' || true
i=0
while ss -lnt 2>/dev/null | grep -q ':7946 '; do
  i=$((i+1))
  if [ $i -gt 120 ]; then echo "port 7946 never freed, aborting"; exit 1; fi
  sleep 0.5
done
# Secrets never live in this script or the repo: each box keeps them in
# mode-600 files the operator owns, and the run command carries neither
# the -key nor the -panel-token flag. The panel token is optional; a box
# without the file simply serves no panel door.
PHOTINUS_KEY=$(cat "$HOME/.photinus.key")
export PHOTINUS_KEY
if [ -f "$HOME/.photinus.panel-token" ]; then
  PHOTINUS_PANEL_TOKEN=$(cat "$HOME/.photinus.panel-token")
  export PHOTINUS_PANEL_TOKEN
fi
nohup sh -c "$CMD" >> /tmp/photinus.log 2>&1 &
sleep 3
NEW=$(ss -lntp 2>/dev/null | sed -n 's/.*:7946 .*pid=\([0-9]*\).*/\1/p' | head -1)
if [ -n "$NEW" ]; then echo "relit, pid $NEW"; else echo "no listener after relight"; exit 1; fi
