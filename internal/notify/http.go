package notify

import (
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// HTTPPoster returns a Sender that POSTs one event to a url: the sentence as
// the body, and the kind rendered into X-Title, X-Priority, and X-Tags
// headers. Those are ntfy's conventions, but any webhook receiver can read a
// body and three headers. A non-empty token rides along as a bearer token.
// One attempt, no retry, for the same reason Exec gives: a notification
// channel that needs retries needs fixing, not retrying.
func HTTPPoster(url, token string, logger *log.Logger) Sender {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	return func(e Event) {
		go post(client, url, token, e, logger)
	}
}

// post delivers one event and logs the outcome. The elected lantern is the
// only one paging, so a failure here must be visible in its log.
func post(client *http.Client, url, token string, e Event, logger *log.Logger) {
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(e.Detail))
	if err != nil {
		logger.Printf("notification post failed for %s %s on %s: %v", e.Kind, e.Check, e.Target, err)
		return
	}
	req.Header.Set("X-Title", e.Kind+": "+e.Check+" "+e.Target)
	req.Header.Set("X-Priority", priorityFor(e.Kind))
	if tags := tagsFor(e.Kind); tags != "" {
		req.Header.Set("X-Tags", tags)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		logger.Printf("notification post failed for %s %s on %s: %v", e.Kind, e.Check, e.Target, err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		logger.Printf("notification post failed for %s %s on %s: %s answered %s", e.Kind, e.Check, e.Target, url, resp.Status)
		return
	}
	logger.Printf("notified: %s, %s", e.Kind, e.Detail)
}

// priorityFor maps an event kind to an ntfy priority. Down is the page that
// breaks through Do Not Disturb; the way back down the ladder gets quieter.
// An unknown future kind falls through to default rather than being dropped,
// since the notify contract is append-only.
func priorityFor(kind string) string {
	switch kind {
	case "down":
		return "urgent"
	case "flapping":
		return "high"
	case "cleared", "settled":
		return "low"
	default:
		return "default"
	}
}

// tagsFor maps an event kind to an ntfy tag, which the app shows as an emoji.
// Unknown kinds carry no tag.
func tagsFor(kind string) string {
	switch kind {
	case "down":
		return "rotating_light"
	case "warning", "flapping":
		return "warning"
	case "recovered", "cleared", "settled":
		return "white_check_mark"
	default:
		return ""
	}
}
