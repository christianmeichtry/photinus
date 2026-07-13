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
pkill -9 -f 'photinus run' || true
i=0
while ss -lnt 2>/dev/null | grep -q ':7946 '; do
  i=$((i+1))
  if [ $i -gt 120 ]; then echo "port 7946 never freed, aborting"; exit 1; fi
  sleep 0.5
done
export PHOTINUS_KEY='flash twice, wait once'
nohup sh -c "$CMD" >> /tmp/photinus.log 2>&1 &
sleep 3
NEW=$(ss -lntp 2>/dev/null | sed -n 's/.*:7946 .*pid=\([0-9]*\).*/\1/p' | head -1)
if [ -n "$NEW" ]; then echo "relit, pid $NEW"; else echo "no listener after relight"; exit 1; fi
