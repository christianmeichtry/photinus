// photinus-relay signs and forwards push notifications for lanterns that do
// not hold the APNs key. One endpoint, one accepted shape, no state. See
// docs/design.md for why it exists and what it refuses to be.
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/christianmeichtry/photinus/internal/notify"
	"github.com/christianmeichtry/photinus/internal/version"
)

func main() {
	logger := log.New(os.Stderr, "", log.Ltime)
	if err := run(logger); err != nil {
		logger.Fatal(err)
	}
}

func run(logger *log.Logger) error {
	// Env vars, not flags: the relay runs as a service, and its key path
	// belongs next to the process definition, not in a shell history.
	cfg := notify.APNSConfig{
		KeyPath: os.Getenv("RELAY_APNS_KEY"),
		KeyID:   os.Getenv("RELAY_APNS_KEY_ID"),
		TeamID:  os.Getenv("RELAY_APNS_TEAM_ID"),
		Topic:   os.Getenv("RELAY_APNS_TOPIC"),
	}
	if cfg.KeyPath == "" || cfg.KeyID == "" || cfg.TeamID == "" || cfg.Topic == "" {
		return fmt.Errorf("the relay needs all four of RELAY_APNS_KEY, RELAY_APNS_KEY_ID, RELAY_APNS_TEAM_ID, RELAY_APNS_TOPIC")
	}
	listen := os.Getenv("RELAY_LISTEN")
	if listen == "" {
		// Loopback by default: the box's reverse proxy owns the public side
		// and the TLS. Exposing the relay directly is an explicit choice.
		listen = "127.0.0.1:8964"
	}

	apple, err := notify.NewAPNSClient(cfg)
	if err != nil {
		return err
	}
	s := newServer(apple, logger)
	srv := &http.Server{
		Addr:              listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	logger.Printf("photinus-relay %s listening on %s, topic %s", version.Release, listen, cfg.Topic)
	return srv.ListenAndServe()
}
