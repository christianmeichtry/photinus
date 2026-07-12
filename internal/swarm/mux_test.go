package swarm

import (
	"bufio"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestIsHTTPStart(t *testing.T) {
	for _, b := range []byte{'G', 'P', 'H', 'D', 'O', 'C', 'T'} {
		if !isHTTPStart(b) {
			t.Errorf("byte %q should start HTTP", b)
		}
	}
	// memberlist message types live below 0x20; none are printable letters.
	for _, b := range []byte{0, 1, 5, 12, 17, 0x1f} {
		if isHTTPStart(b) {
			t.Errorf("gossip byte %d misread as HTTP", b)
		}
	}
}

// A muxTransport not driven by memberlist: we feed it connections directly
// through its inner NetTransport's listener by dialing the bound port, and
// check where each lands.
func TestMuxRoutes(t *testing.T) {
	var httpHit bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHit = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux, err := newMuxTransport("127.0.0.1", 0, handler, nil)
	if err != nil {
		t.Fatalf("newMuxTransport: %v", err)
	}
	defer mux.Shutdown()

	addr := net.JoinHostPort("127.0.0.1", itoa(mux.GetAutoBindPort()))

	t.Run("HTTP reaches the handler", func(t *testing.T) {
		httpHit = false
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		conn.Write([]byte("GET /status.json HTTP/1.1\r\nHost: x\r\n\r\n"))
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatalf("reading response: %v", err)
		}
		if line[:12] != "HTTP/1.1 200" {
			t.Errorf("status line = %q, want 200", line)
		}
		if !httpHit {
			t.Error("handler was not called")
		}
	})

	t.Run("gossip bytes reach the stream channel", func(t *testing.T) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		// A memberlist stream begins with a low message-type byte.
		conn.Write([]byte{5, 0, 0, 0})
		select {
		case got := <-mux.StreamCh():
			defer got.Close()
			buf := make([]byte, 1)
			got.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := got.Read(buf); err != nil {
				t.Fatalf("reading routed conn: %v", err)
			}
			if buf[0] != 5 {
				t.Errorf("first byte = %d, want 5 (the peeked byte must be replayed)", buf[0])
			}
		case <-time.After(2 * time.Second):
			t.Fatal("gossip connection never reached StreamCh")
		}
	})

	t.Run("a silent connection does not wedge the accept loop", func(t *testing.T) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		// Send nothing; the classify deadline should close it. A following
		// HTTP request must still be served, proving the loop lives.
		time.Sleep(100 * time.Millisecond)
		conn2, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("second dial: %v", err)
		}
		defer conn2.Close()
		conn2.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
		conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := bufio.NewReader(conn2).ReadString('\n'); err != nil {
			t.Fatalf("accept loop wedged: %v", err)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [6]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
