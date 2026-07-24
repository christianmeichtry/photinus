package notify

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// The relay sender pages phones through a photinus relay instead of holding
// an APNs signing key. A lantern on a box the operator does not fully trust,
// or a stranger's lantern once photinus is public, sends the alert to the
// relay; the relay holds the key, signs, and forwards to Apple. Several
// relay urls give client-side failover, the same idiom as the app walking
// its doors: try the first, move down the list on transport failure.

// RelayRequest is the one shape a relay accepts: a single alert for a
// single device. Anything else is rejected on the relay side, which is what
// keeps a public relay from being an arbitrary push gateway.
type RelayRequest struct {
	Token    string `json:"token"`
	Env      string `json:"env"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Collapse string `json:"collapse,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// Relay builds a Sender that delivers through the first relay that answers.
// source yields the current registrations at send time, late-bound and
// nil-safe like the direct APNs sender's.
func Relay(urls []string, source func() []PushRegistration, logger *log.Logger) Sender {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	return func(e Event) {
		go func() {
			regs := source()
			if len(regs) == 0 {
				logger.Printf("push for %s %s on %s has nowhere to go: no phone has registered", e.Kind, e.Check, e.Target)
				return
			}
			for _, r := range regs {
				relayOne(client, urls, r, e, logger)
			}
		}()
	}
}

// relayOne walks the relay list for one registration. A transport error or
// a relay-side failure (5xx) moves to the next relay; an answer that made
// it to Apple, good or bad, is final, because a second relay would only
// repeat it.
func relayOne(client *http.Client, urls []string, r PushRegistration, e Event, logger *log.Logger) {
	body, err := json.Marshal(RelayRequest{
		Token:    r.Token,
		Env:      r.Env,
		Title:    e.Kind + ": " + e.Check + " " + e.Target,
		Body:     e.Detail,
		Collapse: collapseID(e),
		Kind:     e.Kind,
	})
	if err != nil {
		return
	}
	for _, u := range urls {
		u = strings.TrimSuffix(u, "/") + "/relay"
		resp, err := client.Post(u, "application/json", bytes.NewReader(body))
		if err != nil {
			logger.Printf("relay %s unreachable for %s %s on %s: %v", u, e.Kind, e.Check, e.Target, err)
			continue
		}
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusOK:
			logger.Printf("pushed via relay: %s, %s", e.Kind, e.Detail)
			return
		case resp.StatusCode >= 500:
			logger.Printf("relay %s failed for %s %s on %s: %s: %s", u, e.Kind, e.Check, e.Target, resp.Status, bytes.TrimSpace(reason))
			continue
		case resp.StatusCode == http.StatusGone:
			logger.Printf("push token …%s is gone (410); it ages out after %s unless the phone re-registers", tail(r.Token), PushTTL)
			return
		default:
			logger.Printf("relay push failed for %s %s on %s: %s: %s", e.Kind, e.Check, e.Target, resp.Status, bytes.TrimSpace(reason))
			return
		}
	}
	logger.Printf("no relay answered for %s %s on %s, tried %d", e.Kind, e.Check, e.Target, len(urls))
}
