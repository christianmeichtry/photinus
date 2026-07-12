package check

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"
)

// Cert watches a TLS certificate the way an operator wishes someone had:
// a broken or expired certificate is an outage, because browsers block
// users on the spot, and one about to expire is a warning while there is
// still time to fix the renewal.
type Cert struct {
	// Addr is the host:port to handshake with; 443 is implied elsewhere.
	Addr string
	// WarnWithin is how close to expiry the warning starts.
	// Zero means 7 days.
	WarnWithin time.Duration
	// Timeout bounds the handshake. Zero means 10 seconds.
	Timeout time.Duration
}

func (c Cert) Name() string   { return "cert" }
func (c Cert) Target() string { return c.Addr }

func (c Cert) Run(ctx context.Context) Result {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return Result{Verdict: Failed, Detail: fmt.Sprintf("bad address: %v", err)}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	d := net.Dialer{Timeout: timeout}
	raw, err := d.DialContext(ctx, "tcp", c.Addr)
	if err != nil {
		return Result{Verdict: Failed, Detail: "cannot connect: " + err.Error()}
	}
	conn := tls.Client(raw, &tls.Config{ServerName: host})
	defer conn.Close()
	if err := conn.HandshakeContext(ctx); err != nil {
		// Verification failures land here too: wrong host, unknown
		// authority, expired. All of them block users, all of them are
		// down.
		reason := err.Error()
		reason = strings.TrimPrefix(reason, "tls: failed to verify certificate: ")
		return Result{Verdict: Failed, Detail: "certificate refused: " + reason}
	}

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return Result{Verdict: Failed, Detail: "no certificate presented"}
	}
	return certVerdict(certs[0], c.warnWithin(), time.Now())
}

func (c Cert) warnWithin() time.Duration {
	if c.WarnWithin <= 0 {
		return 7 * 24 * time.Hour
	}
	return c.WarnWithin
}

// certVerdict judges a leaf certificate that already passed verification.
func certVerdict(leaf *x509.Certificate, warnWithin time.Duration, now time.Time) Result {
	left := leaf.NotAfter.Sub(now)
	issuer := leaf.Issuer.CommonName
	if issuer == "" && len(leaf.Issuer.Organization) > 0 {
		issuer = leaf.Issuer.Organization[0]
	}
	if issuer == "" {
		issuer = "unknown issuer"
	}
	switch {
	case left <= 0:
		return Result{Verdict: Failed, Detail: fmt.Sprintf("certificate expired %s ago", humanDuration(-left))}
	case left <= warnWithin:
		return Result{Verdict: Warn, Detail: fmt.Sprintf("certificate expires in %s, renew now (%s)", humanDuration(left), issuer)}
	default:
		return Result{Verdict: OK, Detail: fmt.Sprintf("valid, %s left (%s)", humanDuration(left), issuer)}
	}
}

// Every reflects how often certificates actually change: renewals land
// daily at most, and the warning window is measured in days.
func (c Cert) Every() time.Duration { return time.Hour }
