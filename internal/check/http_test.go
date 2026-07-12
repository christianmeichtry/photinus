package check

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
		case "/moved":
			http.Redirect(w, r, "/ok", http.StatusMovedPermanently)
		case "/missing":
			w.WriteHeader(http.StatusNotFound)
		case "/broken":
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	// A refused port for the connection-level failure.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving dead port: %v", err)
	}
	deadURL := "http://" + dead.Addr().String()
	dead.Close()

	tests := []struct {
		name string
		url  string
		want Verdict
	}{
		{"200 is up", srv.URL + "/ok", OK},
		{"redirect to 200 is up", srv.URL + "/moved", OK},
		{"404 is down, the page you watch is broken", srv.URL + "/missing", Failed},
		{"500 is down", srv.URL + "/broken", Failed},
		{"connection refused is down", deadURL, Failed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HTTP{URL: tt.url}.Run(context.Background())
			if got.Verdict != tt.want {
				t.Errorf("HTTP(%s) = %s, want %s (detail: %s)", tt.url, got.Verdict, tt.want, got.Detail)
			}
			if got.Detail == "" {
				t.Error("empty detail, operators need a sentence")
			}
		})
	}

	t.Run("a certificate browsers reject is down", func(t *testing.T) {
		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		defer tlsSrv.Close()
		got := HTTP{URL: tlsSrv.URL}.Run(context.Background())
		if got.Verdict != Failed {
			t.Errorf("self-signed https = %s, want %s (detail: %s)", got.Verdict, Failed, got.Detail)
		}
		if !strings.Contains(got.Detail, "certificate") && !strings.Contains(got.Detail, "x509") {
			t.Errorf("detail %q does not say it was the certificate", got.Detail)
		}
	})
}
