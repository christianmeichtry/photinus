# Push notifications to your phone

photinus already decides *what* to say and *who* says it: the swarm agrees,
the hash-elected lantern runs your `-notify` command exactly once. This
guide points that command at your phone using [ntfy](https://ntfy.sh), a
small self-hostable pub-sub notifier. It stays on-brand: one Go binary, no
account, runs on a box you already own.

## The short way: no script at all

The pager script below is no longer needed. photinus posts to ntfy (or any
webhook that reads a body and headers) built-in:

```sh
photinus run ... -notify-url https://ntfy.example.com/photinus-a1b2c3 \
  -notify-url-token tk_xxxxxxxx
```

The token can also come from `$PHOTINUS_NOTIFY_TOKEN`. Priorities and tags
map the same way the script did: down arrives urgent, flapping high,
recoveries and clears quietly. `-notify` and `-notify-url` combine, so an
exec script can run alongside the built-in post. Set up the ntfy server
(section 1) and your phone (section 3) as before; the script docs below
stay for exec users who want their own transport.

## 1. Run ntfy on one lantern's host

Any box works; drongar is a fine choice since it already runs Caddy. On
that box:

```sh
# Debian/Ubuntu
sudo apt install ntfy    # or grab the single binary from ntfy.sh/releases
sudo systemctl enable --now ntfy
```

Put it behind your existing web server with auth so only you can subscribe.
A Caddy vhost:

```
ntfy.example.com {
	reverse_proxy 127.0.0.1:2586
}
```

and in `/etc/ntfy/server.yml` set `base-url: "https://ntfy.example.com"`,
then enable auth so the topic is not world-readable:

```sh
sudo ntfy access --reset                       # deny anonymous
sudo ntfy user add --role=admin you            # your login
sudo ntfy access you 'photinus-*' read-write   # your topics
```

Pick an unguessable topic name, e.g. `photinus-a1b2c3`.

## 2. The pager script

Save as `/usr/local/bin/photinus-page`, `chmod +x`, and point every
lantern at it with `-notify /usr/local/bin/photinus-page`. It receives four
arguments from photinus: `kind check target sentence`.

```sh
#!/bin/sh
# photinus -> ntfy. Args: kind check target sentence.
TOPIC="https://ntfy.example.com/photinus-a1b2c3"
TOKEN="tk_xxxxxxxx"   # ntfy token for your user

case "$1" in
  down)                       prio=urgent ;;
  warning)                    prio=default ;;
  recovered|cleared|flapping|settled) prio=low ;;
  *)                          prio=default ;;   # unknown future kinds
esac

curl -s \
  -H "Authorization: Bearer $TOKEN" \
  -H "Title: $2 $3" \
  -H "Priority: $prio" \
  -d "$4" \
  "$TOPIC" > /dev/null
```

The priority mapping covers every kind notify emits (down, warning,
recovered, cleared, flapping, settled); an unknown future kind falls
through to `default` rather than being dropped, since the notify contract
is append-only.

Also drop these lines into `swarm.sh` in place of the log-only pager so the
whole fleet pages the same way.

## 3. Your phone

Install the free ntfy app (iOS/Android), add your server
`https://ntfy.example.com` with your token, and subscribe to the topic.
Down alerts arrive as urgent (they break through Do Not Disturb if you
allow it); recoveries and flap notices arrive quietly.

## Later: the native app

The SwiftUI app (roadmap Phase 1) will subscribe to the same ntfy topic, so
this setup is not throwaway. Phase 2 swaps ntfy for a direct APNs relay for
the store app while the free PWA track keeps using ntfy.
