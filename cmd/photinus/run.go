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
	socket := fs.String("socket", "", "unix socket for local status queries (default: photinus-<id>.sock in the temp dir)")
	var seeds, watches stringList
	fs.Var(&seeds, "seed", "address of a lantern to join through (repeatable)")
	fs.Var(&watches, "watch", "a check to run, currently only tcp:host:port (repeatable)")
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

	checks, err := parseWatches(watches)
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "", log.Ltime)

	lan := lantern.New(lantern.Config{
		ID:       *id,
		Interval: *interval,
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

// parseWatches turns -watch flags into checks. Only tcp exists yet.
func parseWatches(watches []string) ([]check.Check, error) {
	var checks []check.Check
	for _, w := range watches {
		kind, target, ok := strings.Cut(w, ":")
		if !ok || kind != "tcp" {
			return nil, fmt.Errorf("cannot watch %q: only tcp:host:port checks exist yet", w)
		}
		if _, _, err := net.SplitHostPort(target); err != nil {
			return nil, fmt.Errorf("cannot watch %q: %w", w, err)
		}
		checks = append(checks, check.TCP{Addr: target})
	}
	return checks, nil
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
