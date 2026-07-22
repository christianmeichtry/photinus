package notify

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writeP8 makes a throwaway APNs-shaped signing key on disk.
func writeP8(t *testing.T) (string, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func TestAPNSRejectsABadKey(t *testing.T) {
	if _, err := APNS(APNSConfig{KeyPath: filepath.Join(t.TempDir(), "absent.p8")}, nil, nil); err == nil {
		t.Fatal("a missing key file must fail at startup, not at the first outage")
	}
	bad := filepath.Join(t.TempDir(), "bad.p8")
	os.WriteFile(bad, []byte("not a key"), 0o600)
	if _, err := APNS(APNSConfig{KeyPath: bad}, nil, nil); err == nil {
		t.Fatal("a mangled key file must fail at startup")
	}
}

func TestAPNSBearerSignsAndCaches(t *testing.T) {
	path, key := writeP8(t)
	if _, err := APNS(APNSConfig{KeyPath: path, KeyID: "K1", TeamID: "T1", Topic: "app"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	s := &apnsSender{key: key, keyID: "K1", teamID: "T1"}
	now := time.Now()
	tok, err := s.bearer(now)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyJWT(tok, &key.PublicKey) {
		t.Fatal("the provider token does not verify against the public key")
	}
	// Within fifty minutes the same token comes back; past it, a fresh one.
	again, _ := s.bearer(now.Add(49 * time.Minute))
	if again != tok {
		t.Error("token re-signed before its time")
	}
	fresh, _ := s.bearer(now.Add(51 * time.Minute))
	if fresh == tok {
		t.Error("token not re-signed after fifty minutes")
	}
}

// TestAPNSSendsToEveryPhone drives the sender against two local mock APNs
// hosts and checks the request Apple would see: path, topic, priority,
// collapse id, payload.
func TestAPNSSendsToEveryPhone(t *testing.T) {
	_, key := writeP8(t)

	type hit struct {
		path, topic, pushType, priority, collapse string
		body                                      map[string]any
	}
	var mu sync.Mutex
	var hits []hit
	var wg sync.WaitGroup
	handler := func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(b, &body)
		mu.Lock()
		hits = append(hits, hit{
			path:     r.URL.Path,
			topic:    r.Header.Get("apns-topic"),
			pushType: r.Header.Get("apns-push-type"),
			priority: r.Header.Get("apns-priority"),
			collapse: r.Header.Get("apns-collapse-id"),
			body:     body,
		})
		mu.Unlock()
	}
	sandbox := httptest.NewServer(http.HandlerFunc(handler))
	defer sandbox.Close()
	production := httptest.NewServer(http.HandlerFunc(handler))
	defer production.Close()

	regs := []PushRegistration{
		{Token: "aa11", Env: "sandbox", Seen: time.Now()},
		{Token: "bb22", Env: "production", Seen: time.Now()},
	}
	s := &apnsSender{
		key: key, keyID: "K1", teamID: "T1", topic: "ch.example.app",
		hosts:  map[string]string{"sandbox": sandbox.URL, "production": production.URL},
		client: sandbox.Client(),
		source: func() []PushRegistration { return regs },
		log:    log.New(io.Discard, "", 0),
	}
	wg.Add(2)
	s.send(Event{Kind: "down", Check: "lantern", Target: "vega", Detail: "lantern on vega is down"})
	wg.Wait()

	if len(hits) != 2 {
		t.Fatalf("wanted 2 pushes, got %d", len(hits))
	}
	paths := map[string]bool{}
	for _, h := range hits {
		paths[h.path] = true
		if h.topic != "ch.example.app" || h.pushType != "alert" {
			t.Errorf("wrong headers: topic=%q type=%q", h.topic, h.pushType)
		}
		if h.priority != "10" {
			t.Errorf("a down pages immediately: priority=%q", h.priority)
		}
		if h.collapse != "lantern vega" {
			t.Errorf("collapse id %q", h.collapse)
		}
		aps, _ := h.body["aps"].(map[string]any)
		if aps == nil {
			t.Fatalf("no aps in payload: %v", h.body)
		}
		alert, _ := aps["alert"].(map[string]any)
		if alert["title"] != "down: lantern vega" || alert["body"] != "lantern on vega is down" {
			t.Errorf("alert %v", alert)
		}
		if aps["interruption-level"] != "time-sensitive" {
			t.Errorf("a down is time-sensitive, got %v", aps["interruption-level"])
		}
	}
	if !paths["/3/device/aa11"] || !paths["/3/device/bb22"] {
		t.Errorf("pushes did not reach both phones: %v", paths)
	}
}

func TestAPNSRecoveredIsQuieter(t *testing.T) {
	if apnsPriority("recovered") != "5" || apnsPriority("down") != "10" {
		t.Error("priority ladder wrong")
	}
	p := payload(Event{Kind: "recovered", Check: "tcp", Target: "db:5432", Detail: "tcp on db:5432 recovered"})
	aps := p["aps"].(map[string]any)
	if _, timeSensitive := aps["interruption-level"]; timeSensitive {
		t.Error("only a down interrupts")
	}
}
