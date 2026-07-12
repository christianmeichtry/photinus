package check

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestTCP(t *testing.T) {
	// A live listener for the passing case.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("starting listener: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	// A closed port for the failing case: listen, grab the port, close.
	dead, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving dead port: %v", err)
	}
	deadAddr := dead.Addr().String()
	dead.Close()

	tests := []struct {
		name string
		addr string
		want Verdict
	}{
		{"open port connects", ln.Addr().String(), OK},
		{"closed port fails", deadAddr, Failed},
		{"unresolvable host fails", "host.invalid:1", Failed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := TCP{Addr: tt.addr, Timeout: 2 * time.Second}
			got := c.Run(context.Background())
			if got.Verdict != tt.want {
				t.Errorf("TCP(%s) verdict = %s, want %s (detail: %s)",
					tt.addr, got.Verdict, tt.want, got.Detail)
			}
			if got.Detail == "" {
				t.Errorf("TCP(%s) returned empty detail, operators need a sentence", tt.addr)
			}
		})
	}

	if got, want := (TCP{Addr: "x:1"}).Name(), "tcp"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
	if got, want := (TCP{Addr: "x:1"}).Target(), "x:1"; got != want {
		t.Errorf("Target() = %q, want %q", got, want)
	}
}
