package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/christianmeichtry/photinus/internal/lantern"
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
