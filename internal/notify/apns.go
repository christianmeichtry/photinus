package notify

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"
)

// The APNs sender pages the operator's phone directly through Apple's push
// service: no third-party relay, no new daemon. It is one more Sender in
// the fan-out, so the election, flap damping, and the alert delay all sit
// above it and it inherits them for free. The phone hands its device token
// to any lantern (POST /push/register); the token gossips like everything
// else, so whichever lantern wins the election holds it.

// PushRegistration is one phone's standing request to be paged. Env names
// the APNs environment the app was built for: a Debug build talks to
// "sandbox", a release build to "production". Seen is when the phone last
// re-registered; the newest word per token wins the gossip merge, and a
// token nobody has renewed within pushTTL is forgotten.
type PushRegistration struct {
	Token string    `json:"token"`
	Env   string    `json:"env"`
	Seen  time.Time `json:"seen"`
}

// PushTTL is how long a registration lives without renewal. The app
// re-registers on every launch, so thirty days of silence means the phone
// is gone, not sleeping.
const PushTTL = 30 * 24 * time.Hour

// APNSConfig is everything Apple needs to accept a push: the .p8 signing
// key downloaded once from the developer account, its key id, the team id,
// and the app's bundle id as the topic.
type APNSConfig struct {
	KeyPath string
	KeyID   string
	TeamID  string
	Topic   string
}

type apnsSender struct {
	key    *ecdsa.PrivateKey
	keyID  string
	teamID string
	topic  string
	// hosts maps an environment to its APNs base URL; tests point these at
	// a local server.
	hosts  map[string]string
	client *http.Client
	source func() []PushRegistration
	log    *log.Logger

	mu    sync.Mutex
	jwt   string
	jwtAt time.Time
}

// APNS builds the Sender. The key is parsed at startup so a bad path or a
// mangled file fails the lantern loudly, not the first outage quietly.
// source yields the current registrations at send time; it may be bound
// late (the lantern is built after the tracker) and nil-safe.
func APNS(cfg APNSConfig, source func() []PushRegistration, logger *log.Logger) (Sender, error) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	raw, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("reading the APNs key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("the APNs key %s is not PEM", cfg.KeyPath)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing the APNs key: %w", err)
	}
	ec, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the APNs key %s is not an EC key", cfg.KeyPath)
	}
	s := &apnsSender{
		key:    ec,
		keyID:  cfg.KeyID,
		teamID: cfg.TeamID,
		topic:  cfg.Topic,
		hosts: map[string]string{
			"sandbox":    "https://api.sandbox.push.apple.com",
			"production": "https://api.push.apple.com",
		},
		client: &http.Client{Timeout: 10 * time.Second},
		source: source,
		log:    logger,
	}
	return s.send, nil
}

// send delivers one event to every registered phone. Like every Sender it
// returns immediately and does its waiting in a goroutine; one attempt per
// token, no retry, for the reason Exec gives.
func (s *apnsSender) send(e Event) {
	go func() {
		regs := s.source()
		if len(regs) == 0 {
			s.log.Printf("push for %s %s on %s has nowhere to go: no phone has registered", e.Kind, e.Check, e.Target)
			return
		}
		token, err := s.bearer(time.Now())
		if err != nil {
			s.log.Printf("push failed for %s %s on %s: signing the token: %v", e.Kind, e.Check, e.Target, err)
			return
		}
		body, err := json.Marshal(payload(e))
		if err != nil {
			return
		}
		for _, r := range regs {
			s.push(r, e, token, body)
		}
	}()
}

func (s *apnsSender) push(r PushRegistration, e Event, bearer string, body []byte) {
	host, ok := s.hosts[r.Env]
	if !ok {
		s.log.Printf("push skipped a registration with unknown environment %q", r.Env)
		return
	}
	req, err := http.NewRequest(http.MethodPost, host+"/3/device/"+r.Token, bytes.NewReader(body))
	if err != nil {
		s.log.Printf("push failed for %s %s on %s: %v", e.Kind, e.Check, e.Target, err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apns-topic", s.topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", apnsPriority(e.Kind))
	req.Header.Set("apns-collapse-id", collapseID(e))
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Printf("push failed for %s %s on %s: %v", e.Kind, e.Check, e.Target, err)
		return
	}
	reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		s.log.Printf("pushed: %s, %s", e.Kind, e.Detail)
	case resp.StatusCode == http.StatusGone:
		// The phone uninstalled the app or its token rolled. The app
		// re-registers with the fresh token on its next launch and this one
		// ages out after PushTTL; nothing to do but say so.
		s.log.Printf("push token …%s is gone (410); it ages out after %s unless the phone re-registers", tail(r.Token), PushTTL)
	default:
		s.log.Printf("push failed for %s %s on %s: APNs answered %s: %s", e.Kind, e.Check, e.Target, resp.Status, bytes.TrimSpace(reason))
	}
}

// bearer returns the cached provider JWT, re-signing once it is fifty
// minutes old: Apple wants tokens between twenty minutes and one hour.
func (s *apnsSender) bearer(now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jwt != "" && now.Sub(s.jwtAt) < 50*time.Minute {
		return s.jwt, nil
	}
	b64 := func(v []byte) string { return base64.RawURLEncoding.EncodeToString(v) }
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": s.keyID})
	claims, _ := json.Marshal(map[string]any{"iss": s.teamID, "iat": now.Unix()})
	signing := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signing))
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest[:])
	if err != nil {
		return "", err
	}
	// JOSE wants the raw 64-byte r||s, each padded to 32, not ASN.1.
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	sig.FillBytes(raw[32:])
	s.jwt = signing + "." + b64(raw)
	s.jwtAt = now
	return s.jwt, nil
}

// verifyJWT checks a provider token against the public key; tests use it
// so the signing math is proven, not trusted.
func verifyJWT(token string, pub *ecdsa.PublicKey) bool {
	parts := bytes.Split([]byte(token), []byte("."))
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(string(parts[2]))
	if err != nil || len(sig) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(string(parts[0]) + "." + string(parts[1])))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	return ecdsa.Verify(pub, digest[:], r, s)
}

// payload is the notification itself: the kind and subject as the title,
// the sentence as the body. Down interrupts; the way back is quieter.
func payload(e Event) map[string]any {
	aps := map[string]any{
		"alert": map[string]string{
			"title": e.Kind + ": " + e.Check + " " + e.Target,
			"body":  e.Detail,
		},
		"sound": "default",
	}
	if e.Kind == "down" {
		aps["interruption-level"] = "time-sensitive"
	}
	return map[string]any{"aps": aps}
}

// apnsPriority mirrors the ntfy ladder: a down is delivered immediately,
// everything else may coalesce for battery.
func apnsPriority(kind string) string {
	if kind == "down" || kind == "flapping" {
		return "10"
	}
	return "5"
}

// collapseID folds repeated pages about one subject into the newest, the
// way the panel's matrix shows one square per subject. APNs caps it at 64
// bytes.
func collapseID(e Event) string {
	id := e.Check + " " + e.Target
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}

// tail is the loggable end of a token: enough to match against the phone,
// not enough to page it.
func tail(token string) string {
	if len(token) <= 8 {
		return token
	}
	return token[len(token)-8:]
}
