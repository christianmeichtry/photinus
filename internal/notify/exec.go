package notify

import (
	"context"
	"io"
	"log"
	"os/exec"
	"strings"
	"time"
)

// Exec returns a Sender that runs a command with four arguments: the event
// kind ("down" or "recovered"), the check, the target, and a sentence for a
// human. Exit status and output go to the log; there is no retry, because a
// notification channel that needs retries needs fixing, not retrying.
func Exec(command string, logger *log.Logger) Sender {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return func(e Event) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, command, e.Kind, e.Check, e.Target, e.Detail)
			out, err := cmd.CombinedOutput()
			if err != nil {
				logger.Printf("notification command failed for %s %s on %s: %v: %s",
					e.Kind, e.Check, e.Target, err, strings.TrimSpace(string(out)))
				return
			}
			logger.Printf("notified: %s, %s", e.Kind, e.Detail)
		}()
	}
}
