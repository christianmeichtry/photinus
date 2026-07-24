package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/christianmeichtry/photinus/internal/notify"
	"github.com/christianmeichtry/photinus/internal/version"
)

// The relay exists so a lantern can page a phone without holding the APNs
// signing key: boxes the operator does not fully trust, and one day
// strangers' fleets, send one fixed alert shape here and the relay signs
// and forwards it. Stateless on purpose: no accounts, no storage, nothing
// to breach but the key it guards, and rate limits instead of identities.

// maxBody bounds a request. The largest honest alert (a 160-hex token, a
// long subject, a full sentence) is a few hundred bytes; two KiB is roomy.
const maxBody = 2048

// Field bounds: generous for honest alerts, tight enough that the relay
// cannot be used as a free broadcast channel.
const (
	maxTitle    = 128
	maxBodyText = 1024
	maxCollapse = 64
)

// kinds the relay recognizes; it only sets delivery priority, so unknown
// values are rejected rather than guessed at.
var knownKinds = map[string]bool{
	"": true, "down": true, "warning": true, "recovered": true,
	"cleared": true, "flapping": true, "settled": true,
}

// pusher is the seam to Apple; tests inject a fake.
type pusher interface {
	Push(env, deviceToken string, body []byte, kind, collapse string) (int, string, error)
}

type server struct {
	apple pusher
	log   *log.Logger

	// Fixed-window rate limits, in memory, wiped hourly. Per token so one
	// phone cannot be spammed, per IP so one client cannot exhaust the
	// relay. If the maps ever grow absurd the whole window resets early:
	// bounded memory beats precise accounting on a free public endpoint.
	mu      sync.Mutex
	windows time.Time
	byIP    map[string]int
	byToken map[string]int
}

const (
	perIPHourly    = 120
	perTokenHourly = 60
	maxTracked     = 4096
)

func newServer(apple pusher, logger *log.Logger) *server {
	return &server{
		apple:   apple,
		log:     logger,
		windows: time.Now(),
		byIP:    make(map[string]int),
		byToken: make(map[string]int),
	}
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/relay", s.handleRelay)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok photinus-relay %s\n", version.Release)
	})
	return mux
}

func (s *server) handleRelay(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req notify.RelayRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if reason := validate(req); reason != "" {
		http.Error(w, reason, http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	if !s.allow(ip, req.Token) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}

	body, err := json.Marshal(notify.AlertPayload(req.Title, req.Body, req.Kind))
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	status, reason, err := s.apple.Push(req.Env, req.Token, body, req.Kind, req.Collapse)
	if err != nil {
		s.log.Printf("forwarding for %s failed: %v", ip, err)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	// Mirror Apple's verdict so the client can react (a 410 ages the token
	// out client-side, exactly as with a direct push).
	s.log.Printf("relayed for %s: token …%s, apple said %d", ip, tail(req.Token), status)
	w.WriteHeader(status)
	if reason != "" {
		fmt.Fprintln(w, reason)
	}
}

// validate enforces the one accepted shape. The relay never guesses: a
// request that is not exactly an alert is refused, not repaired.
func validate(req notify.RelayRequest) string {
	if !hexToken(req.Token) {
		return "token must be 16..200 hex characters"
	}
	if req.Env != "sandbox" && req.Env != "production" {
		return `env must be "sandbox" or "production"`
	}
	if req.Title == "" || len(req.Title) > maxTitle {
		return fmt.Sprintf("title must be 1..%d bytes", maxTitle)
	}
	if req.Body == "" || len(req.Body) > maxBodyText {
		return fmt.Sprintf("body must be 1..%d bytes", maxBodyText)
	}
	if len(req.Collapse) > maxCollapse {
		return fmt.Sprintf("collapse is capped at %d bytes", maxCollapse)
	}
	if !knownKinds[req.Kind] {
		return "unknown kind"
	}
	return ""
}

func hexToken(t string) bool {
	if len(t) < 16 || len(t) > 200 {
		return false
	}
	for _, c := range t {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// allow charges one request against both windows. The hour resets lazily,
// and a map grown past its bound resets early rather than growing forever.
func (s *server) allow(ip, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.windows) > time.Hour || len(s.byIP) > maxTracked || len(s.byToken) > maxTracked {
		s.windows = now
		s.byIP = make(map[string]int)
		s.byToken = make(map[string]int)
	}
	if s.byIP[ip] >= perIPHourly || s.byToken[token] >= perTokenHourly {
		return false
	}
	s.byIP[ip]++
	s.byToken[token]++
	return true
}

// clientIP is the peer address, except behind the box's own reverse proxy,
// where the loopback connection carries the real client in X-Forwarded-For.
// A forwarded header from a non-loopback peer is untrusted and ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			return strings.TrimSpace(first)
		}
	}
	return host
}

// tail is the loggable end of a token, enough to correlate, never to page.
func tail(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[len(token)-8:]
}
