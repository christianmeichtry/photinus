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
	interval := fs.Duration("interval", 2*time.Second, "time between flashes")
	skewMax := fs.Duration("skew-max", 5*time.Second, "peer clock drift that trips the skew check, 0 disables it")
	socket := fs.String("socket", "", "unix socket for local status queries (default: photinus-<id>.sock in the temp dir)")
	var seeds, watches stringList
	fs.Var(&seeds, "seed", "address of a lantern to join through (repeatable)")
	fs.Var(&watches, "watch", "a check to run (repeatable): tcp:host:port, disk:/path[:percent], cpu[:percent], memory[:percent], swap[:percent], uptime[:duration]")
	if err := fs.Parse(args); err != nil {
		return err
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

	logger := log.New(os.Stderr, "", log.Ltime)

	lan := lantern.New(lantern.Config{
		ID:       *id,
		Interval: *interval,
		SkewMax:  *skewMax,
		Checks:   checks,
		Logger:   logger,
	})

	sw, err := swarm.Join(swarm.Config{
		ID:      *id,
		Bind:    *bind,
		Seeds:   seeds,
		OnFlash: lan.ReceiveFlash,
		Logger:  logger,
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

	logger.Printf("lantern %s is lit, gossiping on %s, status socket %s", *id, *bind, sockPath)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	lan.Run(ctx)

	logger.Printf("lantern %s going dark", *id)
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
