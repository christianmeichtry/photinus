package check

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCertVerdict(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	week := 7 * 24 * time.Hour
	leaf := func(notAfter time.Time) *x509.Certificate {
		return &x509.Certificate{
			NotAfter: notAfter,
			Issuer:   pkix.Name{CommonName: "R11", Organization: []string{"Let's Encrypt"}},
		}
	}

	tests := []struct {
		name     string
		notAfter time.Time
		want     Verdict
		inDetail string
	}{
		{"plenty of time is ok", now.Add(60 * 24 * time.Hour), OK, "60 days left"},
		{"inside the window warns", now.Add(3 * 24 * time.Hour), Warn, "renew now"},
		{"expired is down", now.Add(-48 * time.Hour), Failed, "expired 2 days ago"},
		{"about to expire still warns", now.Add(time.Hour), Warn, "expires in"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := certVerdict(leaf(tt.notAfter), week, now)
			if got.Verdict != tt.want {
				t.Errorf("verdict = %s, want %s (detail: %s)", got.Verdict, tt.want, got.Detail)
			}
			if !strings.Contains(got.Detail, tt.inDetail) {
				t.Errorf("detail %q does not contain %q", got.Detail, tt.inDetail)
			}
		})
	}

	t.Run("issuer named in the ok detail", func(t *testing.T) {
		got := certVerdict(leaf(now.Add(60*24*time.Hour)), week, now)
		if !strings.Contains(got.Detail, "R11") {
			t.Errorf("detail %q does not name the issuer", got.Detail)
		}
	})
}

func TestCertHandshake(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "https://")

	got := Cert{Addr: addr}.Run(context.Background())
	if got.Verdict != Failed {
		t.Errorf("self-signed cert = %s, want %s (detail: %s)", got.Verdict, Failed, got.Detail)
	}
	if !strings.Contains(got.Detail, "certificate") {
		t.Errorf("detail %q does not say it was the certificate", got.Detail)
	}

	if got := (Cert{Addr: "not-an-address"}).Run(context.Background()); got.Verdict != Failed {
		t.Errorf("bad address = %s, want %s", got.Verdict, Failed)
	}
}
