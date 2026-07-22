package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/christianmeichtry/photinus/internal/lantern"
	"github.com/christianmeichtry/photinus/internal/version"
)

func TestStatusHandlerAuthAndCORS(t *testing.T) {
	lan := lantern.New(lantern.Config{ID: "l1"})
	const token = "s3cret"

	tests := []struct {
		name       string
		token      string
		method     string
		authHeader string
		wantCode   int
	}{
		{"no token, open access", "", "GET", "", 200},
		{"token set, no header", token, "GET", "", 401},
		{"token set, wrong header", token, "GET", "Bearer nope", 401},
		{"token set, right header", token, "GET", "Bearer s3cret", 200},
		{"preflight passes without a token", token, "OPTIONS", "", 204},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := statusHandler(tt.token, lan)
			req := httptest.NewRequest(tt.method, "/status.json", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Errorf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("CORS origin = %q, want *", got)
			}
			if tt.wantCode == 401 && rec.Header().Get("WWW-Authenticate") == "" {
				t.Error("401 without a WWW-Authenticate header")
			}
		})
	}
}

func TestPulseHandler(t *testing.T) {
	const token = "s3cret"

	tests := []struct {
		name       string
		token      string
		method     string
		path       string
		authHeader string
		wantCode   int
		wantBody   string
	}{
		{"POST with the token records", token, "POST", "/pulse/backup-db", "Bearer s3cret", 200, "pulse backup-db recorded"},
		{"GET works too, cron lines are curl", token, "GET", "/pulse/backup-db", "Bearer s3cret", 200, "pulse backup-db recorded"},
		{"undeclared name still records, with a hint", token, "GET", "/pulse/mystery-job", "Bearer s3cret", 200, "pulse mystery-job recorded, not declared on this lantern"},
		{"no token set, open access", "", "GET", "/pulse/backup-db", "", 200, "pulse backup-db recorded"},
		{"token set, no header", token, "POST", "/pulse/backup-db", "", 401, ""},
		{"token set, wrong header", token, "POST", "/pulse/backup-db", "Bearer nope", 401, ""},
		{"preflight passes without a token", token, "OPTIONS", "/pulse/backup-db", "", 204, ""},
		{"a pulse needs a name", token, "GET", "/pulse/", "Bearer s3cret", 400, ""},
		{"a nested path is not a name", token, "GET", "/pulse/backup/db", "Bearer s3cret", 400, ""},
		{"other methods are refused", token, "DELETE", "/pulse/backup-db", "Bearer s3cret", 405, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lan := lantern.New(lantern.Config{
				ID:     "l1",
				Pulses: map[string]time.Duration{"backup-db": time.Hour},
			})
			h := statusHandler(tt.token, lan)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rec.Code, tt.wantCode)
			}
			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
				t.Errorf("CORS origin = %q, want *", got)
			}
			if tt.wantBody != "" {
				if got := strings.TrimSpace(rec.Body.String()); got != tt.wantBody {
					t.Errorf("body = %q, want %q", got, tt.wantBody)
				}
			}
		})
	}
}

func TestStatusHandlerServesShell(t *testing.T) {
	lan := lantern.New(lantern.Config{ID: "l1"})
	h := statusHandler("tok", lan)
	for path, ctype := range map[string]string{
		"/":              "text/html",
		"/manifest.json": "application/manifest+json",
		"/sw.js":         "text/javascript",
		"/icon-192.png":  "image/png",
	} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("%s: code %d, want 200 (shell must load before the token prompt)", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct[:len(ctype)] != ctype {
			t.Errorf("%s: content-type %q, want %q", path, ct, ctype)
		}
	}
	// An unknown path is a 404, not the shell.
	req := httptest.NewRequest("GET", "/nope", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path code = %d, want 404", rec.Code)
	}
}

// TestServiceWorkerCarriesRelease pins the lesson of 0.0.8: a service
// worker whose bytes never change between releases leaves every browser on
// the shell it first cached, forever. The cache name must carry the
// release, stamped in at serve time.
func TestServiceWorkerCarriesRelease(t *testing.T) {
	lan := lantern.New(lantern.Config{ID: "l1"})
	h := statusHandler("", lan)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/sw.js", nil))
	body := rr.Body.String()
	if strings.Contains(body, "@RELEASE@") {
		t.Error("sw.js went out with its placeholder unstamped")
	}
	if !strings.Contains(body, "photinus-shell-"+version.Release) {
		t.Errorf("sw.js cache name does not carry release %s", version.Release)
	}
}

func TestPushRegisterHandler(t *testing.T) {
	const token = "s3cret"

	post := func(lan *lantern.Lantern, auth, body string) *httptest.ResponseRecorder {
		h := statusHandler(token, lan)
		req := httptest.NewRequest("POST", "/push/register", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	t.Run("a good registration lands in the lantern", func(t *testing.T) {
		lan := lantern.New(lantern.Config{ID: "l1"})
		rec := post(lan, "Bearer s3cret", `{"token":"ab12cd34ef56ab12cd34","env":"sandbox"}`)
		if rec.Code != 200 {
			t.Fatalf("code = %d: %s", rec.Code, rec.Body.String())
		}
		regs := lan.PushRegistrations()
		if len(regs) != 1 || regs[0].Token != "ab12cd34ef56ab12cd34" || regs[0].Env != "sandbox" {
			t.Fatalf("registration not stored: %+v", regs)
		}
	})

	t.Run("the door is guarded like the data", func(t *testing.T) {
		lan := lantern.New(lantern.Config{ID: "l1"})
		if rec := post(lan, "", `{"token":"ab12cd34ef56ab12cd34","env":"sandbox"}`); rec.Code != 401 {
			t.Errorf("no bearer, code = %d", rec.Code)
		}
		if len(lan.PushRegistrations()) != 0 {
			t.Error("an unauthorized registration was stored")
		}
	})

	t.Run("garbage is refused", func(t *testing.T) {
		lan := lantern.New(lantern.Config{ID: "l1"})
		for name, body := range map[string]string{
			"not json":        "hello",
			"token not hex":   `{"token":"zz!!zz!!zz!!zz!!zz!!","env":"sandbox"}`,
			"token too short": `{"token":"ab12","env":"sandbox"}`,
			"unknown env":     `{"token":"ab12cd34ef56ab12cd34","env":"carrier-pigeon"}`,
		} {
			if rec := post(lan, "Bearer s3cret", body); rec.Code != 400 {
				t.Errorf("%s: code = %d, want 400", name, rec.Code)
			}
		}
		if len(lan.PushRegistrations()) != 0 {
			t.Error("a refused registration was stored")
		}
	})

	t.Run("GET is not a registration", func(t *testing.T) {
		lan := lantern.New(lantern.Config{ID: "l1"})
		h := statusHandler(token, lan)
		req := httptest.NewRequest("GET", "/push/register", nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 405 {
			t.Errorf("code = %d, want 405", rec.Code)
		}
	})
}
