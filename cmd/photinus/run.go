package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/christianmeichtry/photinus/internal/check"
	"github.com/christianmeichtry/photinus/internal/lantern"
	"github.com/christianmeichtry/photinus/internal/notify"
	"github.com/christianmeichtry/photinus/internal/swarm"
)

// stringList collects a repeatable flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	hostname, _ := os.Hostname()
	id := fs.String("id", hostname, "this lantern's name, unique in the swarm")
	bind := fs.String("bind", "0.0.0.0:7946", "host:port the gossip layer listens on")
	advertise := fs.String("advertise", "", "host[:port] peers should reach this lantern on, when that differs from -bind (NAT, several interfaces)")
	key := fs.String("key", os.Getenv("PHOTINUS_KEY"), "shared swarm secret: encrypts gossip and keeps strangers out (defaults to $PHOTINUS_KEY, empty runs open)")
	interval := fs.Duration("interval", 2*time.Second, "time between flashes")
	skewMax := fs.Duration("skew-max", 5*time.Second, "peer clock drift that trips the skew check, 0 disables it")
	notifyCmd := fs.String("notify", "", "command the elected lantern runs when the swarm agrees something changed; gets kind, check, target, and a sentence as arguments")
	socket := fs.String("socket", "", "unix socket for local status queries (default: photinus-<id>.sock in the temp dir)")
	panel := fs.String("panel", "", "also serve the read-only web status panel on this extra address (e.g. 127.0.0.1:8946); unauthenticated, put a reverse proxy with auth in front of anything public")
	panelToken := fs.String("panel-token", os.Getenv("PHOTINUS_PANEL_TOKEN"), "bearer token guarding status data; when set, the gossip port also answers the panel and /status.json (app and browser reach the swarm through the one open port). Empty leaves the gossip port gossip-only. Defaults to $PHOTINUS_PANEL_TOKEN")
	defaults := fs.Bool("defaults", true, "run the standard local checks (disk:/, cpu, memory, swap, uptime) without naming them")
	var seeds, watches stringList
	fs.Var(&seeds, "seed", "address of a lantern to join through (repeatable)")
	fs.Var(&watches, "watch", "an extra check to run (repeatable): tcp:host:port, http:url, cert:host[:port[:days]], disk:/path[:percent], cpu[:percent], memory[:percent], swap[:percent], uptime[:duration]; naming a default check overrides it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		// The flag package stops at the first thing that does not look
		// like a flag; silently ignoring the rest hides broken commands.
		return fmt.Errorf("unexpected argument %q, flags must come before it", fs.Arg(0))
	}

	if *id == "" {
		return errors.New("this lantern needs a name, pass -id")
	}
	sockPath := *socket
	if sockPath == "" {
		sockPath = defaultSocket(*id)
	}

	checks, err := parseWatches(*id, watches)
	if err != nil {
		return err
	}
	if *defaults {
		// The named checks came first, so a -watch that names a default
		// subject (say disk:/ with its own threshold) wins over it.
		std, err := parseWatches(*id, []string{"disk:/", "cpu", "memory", "swap", "uptime"})
		if err != nil {
			return err
		}
		have := make(map[string]bool, len(checks))
		for _, c := range checks {
			have[c.Name()+"|"+c.Target()] = true
		}
		for _, c := range std {
			if !have[c.Name()+"|"+c.Target()] {
				checks = append(checks, c)
			}
		}
	}

	logger := log.New(os.Stderr, "", log.Ltime)

	var tracker *notify.Tracker
	if *notifyCmd != "" {
		// The warmup matches the observation aging window: by then the
		// swarm has formed and a lantern is no longer a quorum of one.
		tracker = notify.New(*id, 5*(*interval), notify.Exec(*notifyCmd, logger), logger)
	}

	lan := lantern.New(lantern.Config{
		ID:       *id,
		Interval: *interval,
		SkewMax:  *skewMax,
		Checks:   checks,
		Notify:   tracker,
		Logger:   logger,
	})

	// A token opens the panel on the gossip port itself, so the app and a
	// browser reach the swarm through the one port every box already opens.
	var muxHTTP http.Handler
	if *panelToken != "" {
		muxHTTP = statusHandler(*panelToken, lan)
	}

	sw, err := swarm.Join(swarm.Config{
		ID:        *id,
		Bind:      *bind,
		Seeds:     seeds,
		Advertise: *advertise,
		Key:       *key,
		HTTP:      muxHTTP,
		OnFlash:   lan.ReceiveFlash,
		Logger:    logger,
	})
	if err != nil {
		return err
	}
	lan.AttachSwarm(sw)

	statusSrv, err := serveStatus(sockPath, lan)
	if err != nil {
		sw.Leave()
		return err
	}

	var panelSrv *http.Server
	if *panel != "" {
		// The -panel listener is meant to sit behind a reverse proxy that
		// already authenticates (Caddy basic auth), or on loopback, so it
		// stays token-free. The token guards only the gossip-port door,
		// which is directly exposed.
		panelSrv, err = servePanel(*panel, "", lan)
		if err != nil {
			statusSrv.Close()
			sw.Leave()
			return err
		}
		logger.Printf("web panel lit at http://%s", *panel)
	}
	if *panelToken != "" {
		logger.Printf("panel and status answered on the gossip port %s, bearer token required", *bind)
	}

	logger.Printf("lantern %s is lit, gossiping on %s, status socket %s", *id, *bind, sockPath)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	lan.Run(ctx)

	logger.Printf("lantern %s going dark", *id)
	lan.Farewell()
	if panelSrv != nil {
		panelSrv.Close()
	}
	statusSrv.Close()
	os.Remove(sockPath)
	if err := sw.Leave(); err != nil {
		logger.Printf("did not leave cleanly: %v", err)
	}
	return nil
}

// parseWatches turns -watch flags into checks. The id names this host: the
// local resource checks are about it and it becomes their target.
func parseWatches(id string, watches []string) ([]check.Check, error) {
	var checks []check.Check
	for _, w := range watches {
		kind, rest, _ := strings.Cut(w, ":")
		switch kind {
		case "tcp":
			if _, _, err := net.SplitHostPort(rest); err != nil {
				return nil, fmt.Errorf("cannot watch %q: %w", w, err)
			}
			checks = append(checks, check.TCP{Addr: rest})
		case "http", "https":
			u := rest
			switch {
			case strings.HasPrefix(u, "//"):
				// The flag was a bare url like https://example.com and
				// the cut took its scheme.
				u = kind + ":" + u
			case !strings.Contains(u, "://"):
				u = "https://" + u
			}
			if strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://") == "" {
				return nil, fmt.Errorf("cannot watch %q: http needs a url", w)
			}
			checks = append(checks, check.HTTP{URL: u})
		case "cert":
			// cert:host, cert:host:port, cert:host:port:days. With one
			// colon the number is a port, never a day count.
			addr, days := rest, 0
			if strings.Count(rest, ":") >= 2 {
				idx := strings.LastIndex(rest, ":")
				d, err := strconv.Atoi(rest[idx+1:])
				if err != nil || d <= 0 || d > 365 {
					return nil, fmt.Errorf("cannot watch %q: %q is not a day count", w, rest[idx+1:])
				}
				addr, days = rest[:idx], d
			}
			if addr == "" {
				return nil, fmt.Errorf("cannot watch %q: cert needs a host", w)
			}
			if !strings.Contains(addr, ":") {
				addr = net.JoinHostPort(addr, "443")
			}
			if _, _, err := net.SplitHostPort(addr); err != nil {
				return nil, fmt.Errorf("cannot watch %q: %w", w, err)
			}
			checks = append(checks, check.Cert{Addr: addr, WarnWithin: time.Duration(days) * 24 * time.Hour})
		case "disk":
			path, pct, err := splitThreshold(rest)
			if err != nil {
				return nil, fmt.Errorf("cannot watch %q: %w", w, err)
			}
			if path == "" {
				return nil, fmt.Errorf("cannot watch %q: disk needs a path, like disk:/", w)
			}
			checks = append(checks, &check.Disk{Host: id, Path: path, Max: pct})
		case "cpu":
			pct, err := parsePercent(rest)
			if err != nil {
				return nil, fmt.Errorf("cannot watch %q: %w", w, err)
			}
			checks = append(checks, &check.CPU{Host: id, Max: pct})
		case "memory":
			pct, err := parsePercent(rest)
			if err != nil {
				return nil, fmt.Errorf("cannot watch %q: %w", w, err)
			}
			checks = append(checks, &check.Memory{Host: id, Max: pct})
		case "swap":
			pct, err := parsePercent(rest)
			if err != nil {
				return nil, fmt.Errorf("cannot watch %q: %w", w, err)
			}
			checks = append(checks, &check.Swap{Host: id, Max: pct})
		case "uptime":
			var min time.Duration
			if rest != "" {
				var err error
				if min, err = time.ParseDuration(rest); err != nil {
					return nil, fmt.Errorf("cannot watch %q: %w", w, err)
				}
			}
			checks = append(checks, &check.Uptime{Host: id, Min: min})
		default:
			return nil, fmt.Errorf("cannot watch %q: known checks are tcp, disk, cpu, memory, swap, uptime", w)
		}
	}
	return checks, nil
}

// splitThreshold peels an optional trailing :percent off a path, so that
// disk:/data:85 keeps /data even though paths may contain colons.
func splitThreshold(rest string) (string, float64, error) {
	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return rest, 0, nil
	}
	pct, err := strconv.ParseFloat(rest[idx+1:], 64)
	if err != nil {
		// The tail is not a number, treat the whole thing as the path.
		return rest, 0, nil
	}
	if pct <= 0 || pct > 100 {
		return "", 0, fmt.Errorf("threshold %.0f is not a percentage", pct)
	}
	return rest[:idx], pct, nil
}

func parsePercent(rest string) (float64, error) {
	if rest == "" {
		return 0, nil
	}
	pct, err := strconv.ParseFloat(rest, 64)
	if err != nil {
		return 0, err
	}
	if pct <= 0 || pct > 100 {
		return 0, fmt.Errorf("threshold %.0f is not a percentage", pct)
	}
	return pct, nil
}

func defaultSocket(id string) string {
	return fmt.Sprintf("%s/photinus-%s.sock", os.TempDir(), id)
}

// serveStatus answers status queries over a local unix socket, straight from
// the lantern's memory. No network is involved and none may ever be: status
// must keep working with the network on fire.
func serveStatus(path string, lan *lantern.Lantern) (*http.Server, error) {
	// A socket left behind by a dead lantern would block the bind.
	if _, err := os.Stat(path); err == nil {
		if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
			conn.Close()
			return nil, fmt.Errorf("another lantern is already answering on %s", path)
		}
		os.Remove(path)
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("opening status socket %s: %w", path, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lan.Status())
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return srv, nil
}
