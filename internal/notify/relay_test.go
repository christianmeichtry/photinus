package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestRelaySenderFailsOver proves the doors idiom: the first relay eats the
// request or errors, the second one delivers, and the request carries the
// full alert shape with the registration's environment.
func TestRelaySenderFailsOver(t *testing.T) {
	var mu sync.Mutex
	var got []RelayRequest
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "on fire", http.StatusInternalServerError)
	}))
	defer dead.Close()
	var wg sync.WaitGroup
	alive := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer wg.Done()
		if r.URL.Path != "/relay" {
			t.Errorf("posted to %s, want /relay", r.URL.Path)
		}
		var req RelayRequest
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		got = append(got, req)
		mu.Unlock()
	}))
	defer alive.Close()

	regs := []PushRegistration{
		{Token: "aa11bb22cc33dd44ee55", Env: "sandbox", Seen: time.Now()},
		{Token: "ff66ee55dd44cc33bb22", Env: "production", Seen: time.Now()},
	}
	s := Relay([]string{dead.URL, alive.URL}, func() []PushRegistration { return regs }, nil)
	wg.Add(2)
	s(Event{Kind: "down", Check: "http", Target: "https://x.example", Detail: "http on https://x.example is down", Critical: true})
	wg.Wait()

	if len(got) != 2 {
		t.Fatalf("the second relay saw %d requests, want 2", len(got))
	}
	envs := map[string]bool{}
	for _, r := range got {
		envs[r.Env] = true
		if r.Title != "down: http https://x.example" || r.Body != "http on https://x.example is down" {
			t.Errorf("alert shape wrong: %+v", r)
		}
		if r.Kind != "down" || r.Collapse != "http https://x.example" {
			t.Errorf("kind/collapse wrong: %+v", r)
		}
	}
	if !envs["sandbox"] || !envs["production"] {
		t.Errorf("each registration's environment must ride along: %v", envs)
	}
}
