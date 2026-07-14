package notify

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// captured is what one request looked like from the server's side.
type captured struct {
	method   string
	body     string
	title    string
	priority string
	tags     string
	auth     string
}

// captureServer records every request and hands it over on a channel, so a
// test can wait for the Sender's goroutine without sharing memory with it.
func captureServer(t *testing.T, status int) (*httptest.Server, chan captured) {
	t.Helper()
	ch := make(chan captured, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ch <- captured{
			method:   r.Method,
			body:     string(body),
			title:    r.Header.Get("X-Title"),
			priority: r.Header.Get("X-Priority"),
			tags:     r.Header.Get("X-Tags"),
			auth:     r.Header.Get("Authorization"),
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func waitFor(t *testing.T, ch chan captured) captured {
	t.Helper()
	select {
	case got := <-ch:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the poster never reached the server")
		return captured{}
	}
}

func TestHTTPPosterKindMapping(t *testing.T) {
	tests := []struct {
		kind     string
		priority string
		tags     string
	}{
		{"down", "urgent", "rotating_light"},
		{"flapping", "high", "warning"},
		{"warning", "default", "warning"},
		{"recovered", "default", "white_check_mark"},
		{"cleared", "low", "white_check_mark"},
		{"settled", "low", "white_check_mark"},
		// A kind this build does not know must still go out: the notify
		// contract is append-only, so an old lantern may relay a new word.
		{"escalated", "default", ""},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			srv, ch := captureServer(t, http.StatusOK)
			send := HTTPPoster(srv.URL, "", nil)
			send(Event{Kind: tt.kind, Check: "tcp", Target: "db:5432",
				Detail: "tcp on db:5432 changed state"})
			got := waitFor(t, ch)
			if got.method != http.MethodPost {
				t.Errorf("method = %s, want POST", got.method)
			}
			if got.body != "tcp on db:5432 changed state" {
				t.Errorf("body = %q, want the detail sentence", got.body)
			}
			if want := tt.kind + ": tcp db:5432"; got.title != want {
				t.Errorf("X-Title = %q, want %q", got.title, want)
			}
			if got.priority != tt.priority {
				t.Errorf("X-Priority = %q, want %q", got.priority, tt.priority)
			}
			if got.tags != tt.tags {
				t.Errorf("X-Tags = %q, want %q", got.tags, tt.tags)
			}
		})
	}
}

func TestHTTPPosterAuthorization(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  string
	}{
		{"no token sends no header", "", ""},
		{"token rides as bearer", "tk_secret", "Bearer tk_secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, ch := captureServer(t, http.StatusOK)
			send := HTTPPoster(srv.URL, tt.token, nil)
			send(Event{Kind: "down", Check: "tcp", Target: "db:5432", Detail: "down"})
			if got := waitFor(t, ch); got.auth != tt.want {
				t.Errorf("Authorization = %q, want %q", got.auth, tt.want)
			}
		})
	}
}

// The failure cases call post directly: it is the synchronous heart of the
// Sender, and asserting on a log buffer needs the call to have returned.
func TestHTTPPosterFailureIsLogged(t *testing.T) {
	ev := Event{Kind: "down", Check: "tcp", Target: "db:5432", Detail: "down"}

	t.Run("non-2xx counts as failure", func(t *testing.T) {
		srv, ch := captureServer(t, http.StatusForbidden)
		var buf bytes.Buffer
		post(&http.Client{Timeout: time.Second}, srv.URL, "", ev, log.New(&buf, "", 0))
		waitFor(t, ch)
		if !strings.Contains(buf.String(), "notification post failed for down tcp on db:5432") {
			t.Errorf("log = %q, want a failure line the operator can act on", buf.String())
		}
		if !strings.Contains(buf.String(), "403") {
			t.Errorf("log = %q, want the status in it", buf.String())
		}
		if strings.Contains(buf.String(), "notified:") {
			t.Errorf("log = %q, claims success on a 403", buf.String())
		}
	})

	t.Run("a slow server does not hang past the timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		// Deferred calls run last in, first out: the handler must be released
		// before srv.Close waits for it.
		defer close(release)
		var buf bytes.Buffer
		done := make(chan struct{})
		go func() {
			post(&http.Client{Timeout: 100 * time.Millisecond}, srv.URL, "", ev, log.New(&buf, "", 0))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("post is still waiting on a server that will never answer")
		}
		if !strings.Contains(buf.String(), "notification post failed for down tcp on db:5432") {
			t.Errorf("log = %q, want a failure line for the timeout", buf.String())
		}
	})
}
