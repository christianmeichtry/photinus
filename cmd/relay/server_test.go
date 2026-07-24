package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/christianmeichtry/photinus/internal/notify"
)

type fakeApple struct {
	status int
	reason string
	pushes []notify.RelayRequest
	bodies []map[string]any
}

func (f *fakeApple) Push(env, token string, body []byte, kind, collapse string) (int, string, error) {
	var payload map[string]any
	json.Unmarshal(body, &payload)
	f.pushes = append(f.pushes, notify.RelayRequest{Token: token, Env: env, Kind: kind, Collapse: collapse})
	f.bodies = append(f.bodies, payload)
	return f.status, f.reason, nil
}

func post(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/relay", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.9:4711"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

const goodToken = "aa11bb22cc33dd44ee55"

func TestRelayRejectsWhatIsNotAnAlert(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"happy path", `{"token":"` + goodToken + `","env":"sandbox","title":"down: http x","body":"x is down","collapse":"http x","kind":"down"}`, http.StatusOK},
		{"kind is optional", `{"token":"` + goodToken + `","env":"production","title":"t","body":"b"}`, http.StatusOK},
		{"not hex", `{"token":"zz!!","env":"sandbox","title":"t","body":"b"}`, http.StatusBadRequest},
		{"token too short", `{"token":"aa11","env":"sandbox","title":"t","body":"b"}`, http.StatusBadRequest},
		{"bad env", `{"token":"` + goodToken + `","env":"prod","title":"t","body":"b"}`, http.StatusBadRequest},
		{"missing title", `{"token":"` + goodToken + `","env":"sandbox","body":"b"}`, http.StatusBadRequest},
		{"unknown kind", `{"token":"` + goodToken + `","env":"sandbox","title":"t","body":"b","kind":"emergency"}`, http.StatusBadRequest},
		{"unknown field refused", `{"token":"` + goodToken + `","env":"sandbox","title":"t","body":"b","badge":9}`, http.StatusBadRequest},
		{"oversize collapse", `{"token":"` + goodToken + `","env":"sandbox","title":"t","body":"b","collapse":"` + strings.Repeat("c", 65) + `"}`, http.StatusBadRequest},
		{"oversize body", `{"token":"` + goodToken + `","env":"sandbox","title":"t","body":"` + strings.Repeat("b", 3000) + `"}`, http.StatusBadRequest},
		{"not json", `hello`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServer(&fakeApple{status: 200}, log.New(io.Discard, "", 0))
			w := post(t, s.routes(), tt.body)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.want, w.Body.String())
			}
		})
	}
}

func TestRelayForwardsTheOneShape(t *testing.T) {
	apple := &fakeApple{status: 200}
	s := newServer(apple, log.New(io.Discard, "", 0))
	w := post(t, s.routes(), `{"token":"`+goodToken+`","env":"sandbox","title":"down: http x","body":"x is down","collapse":"http x","kind":"down"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if len(apple.pushes) != 1 {
		t.Fatalf("apple saw %d pushes, want 1", len(apple.pushes))
	}
	p := apple.pushes[0]
	if p.Token != goodToken || p.Env != "sandbox" || p.Kind != "down" || p.Collapse != "http x" {
		t.Errorf("forwarded %+v", p)
	}
	aps, _ := apple.bodies[0]["aps"].(map[string]any)
	if aps == nil {
		t.Fatalf("no aps in forwarded payload: %v", apple.bodies[0])
	}
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "down: http x" || alert["body"] != "x is down" {
		t.Errorf("alert %v", alert)
	}
	if aps["interruption-level"] != "time-sensitive" {
		t.Errorf("a down must interrupt, got %v", aps["interruption-level"])
	}
}

func TestRelayMirrorsApple(t *testing.T) {
	apple := &fakeApple{status: http.StatusGone, reason: `{"reason":"Unregistered"}`}
	s := newServer(apple, log.New(io.Discard, "", 0))
	w := post(t, s.routes(), `{"token":"`+goodToken+`","env":"sandbox","title":"t","body":"b"}`)
	if w.Code != http.StatusGone {
		t.Errorf("apple's 410 must reach the client, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Unregistered") {
		t.Errorf("apple's reason must reach the client, got %q", w.Body.String())
	}
}

func TestRelayRateLimits(t *testing.T) {
	s := newServer(&fakeApple{status: 200}, log.New(io.Discard, "", 0))
	h := s.routes()
	body := `{"token":"` + goodToken + `","env":"sandbox","title":"t","body":"b"}`
	var limited bool
	for i := 0; i < perTokenHourly+1; i++ {
		if w := post(t, h, body); w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("the per-token limit never engaged")
	}
}

func TestClientIPBehindProxy(t *testing.T) {
	direct := httptest.NewRequest(http.MethodPost, "/relay", nil)
	direct.RemoteAddr = "203.0.113.9:4711"
	direct.Header.Set("X-Forwarded-For", "198.51.100.7") // forged: peer is not loopback
	if ip := clientIP(direct); ip != "203.0.113.9" {
		t.Errorf("a non-loopback peer's forwarded header was trusted: %q", ip)
	}
	proxied := httptest.NewRequest(http.MethodPost, "/relay", nil)
	proxied.RemoteAddr = "127.0.0.1:9000"
	proxied.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	if ip := clientIP(proxied); ip != "198.51.100.7" {
		t.Errorf("behind the local proxy the first forwarded hop is the client: %q", ip)
	}
}

func TestHealthz(t *testing.T) {
	s := newServer(&fakeApple{status: 200}, log.New(io.Discard, "", 0))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "photinus-relay") {
		t.Errorf("healthz: %d %q", w.Code, w.Body.String())
	}
}
