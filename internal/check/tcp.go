package check

import (
	"context"
	"fmt"
	"net"
	"time"
)

// TCP dials an address and passes when the connection opens.
type TCP struct {
	// Addr is the host:port to dial.
	Addr string
	// Timeout bounds the dial. Zero means a 3 second default.
	Timeout time.Duration
}

func (t TCP) Name() string   { return "tcp" }
func (t TCP) Target() string { return t.Addr }

func (t TCP) Run(ctx context.Context) Result {
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return Result{Verdict: Failed, Detail: fmt.Sprintf("dialing %s: %v", t.Addr, err)}
	}
	conn.Close()
	return Result{Verdict: OK, Detail: fmt.Sprintf("connected to %s", t.Addr)}
}
