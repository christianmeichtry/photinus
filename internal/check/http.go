package check

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTP asks a website for its page and passes only on a happy answer.
// TLS verification stays on: a certificate browsers reject means users
// cannot reach the site, and the check must not be more forgiving than
// the users are.
type HTTP struct {
	// URL is what to fetch, https assumed when the scheme is missing.
	URL string
	// Timeout bounds the whole request. Zero means 10 seconds.
	Timeout time.Duration
}

func (h HTTP) Name() string   { return "http" }
func (h HTTP) Target() string { return h.URL }

func (h HTTP) Run(ctx context.Context) Result {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.URL, nil)
	if err != nil {
		return Result{Verdict: Failed, Detail: fmt.Sprintf("bad url: %v", err)}
	}
	req.Header.Set("User-Agent", "photinus")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// The error usually embeds the full URL; the subject line already
		// names it, so keep the reason.
		reason := err.Error()
		if idx := strings.LastIndex(reason, ": "); idx >= 0 {
			reason = reason[idx+2:]
		}
		return Result{Verdict: Failed, Detail: "cannot fetch: " + reason}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	elapsed := time.Since(start).Round(time.Millisecond)
	if resp.StatusCode >= 400 {
		return Result{Verdict: Failed, Detail: fmt.Sprintf("answered %s", resp.Status)}
	}
	return Result{Verdict: OK, Detail: fmt.Sprintf("%s in %s", resp.Status, elapsed)}
}
