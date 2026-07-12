package swarm

import (
	"bufio"
	"errors"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/hashicorp/memberlist"
)

// The mux lets HTTP and gossip share the one port the swarm already opens,
// so a status client needs exactly that port and nothing else. The two
// protocols are self-identifying from their first byte: memberlist's stream
// messages begin with a binary message type in the low teens (see
// memberlist's messageType constants), while an HTTP request begins with an
// ASCII method letter. The transport reads that first byte, classifies the
// connection, and hands each side a byte-identical stream. This is a
// standard connection-multiplexing pattern, the same idea as cmux or
// serving gRPC and HTTP on one port.

// muxTransport wraps memberlist's NetTransport. UDP passes through
// untouched; each TCP stream connection is classified by its first byte and
// delivered to memberlist (gossip) or to an embedded HTTP server (status
// clients).
type muxTransport struct {
	*memberlist.NetTransport
	gossip chan net.Conn
	httpLn *chanListener
	log    *log.Logger
	stop   chan struct{}
}

// newMuxTransport builds the transport and, when handler is non-nil,
// starts the HTTP server sharing the gossip port.
func newMuxTransport(bindAddr string, bindPort int, handler http.Handler, logger *log.Logger) (*muxTransport, error) {
	inner, err := memberlist.NewNetTransport(&memberlist.NetTransportConfig{
		BindAddrs: []string{bindAddr},
		BindPort:  bindPort,
		Logger:    logger,
	})
	if err != nil {
		return nil, err
	}
	t := &muxTransport{
		NetTransport: inner,
		gossip:       make(chan net.Conn),
		log:          logger,
		stop:         make(chan struct{}),
	}
	if handler != nil {
		t.httpLn = &chanListener{ch: make(chan net.Conn), done: make(chan struct{})}
		srv := &http.Server{Handler: handler}
		go srv.Serve(t.httpLn)
	}
	go t.route()
	return t, nil
}

// StreamCh hands memberlist only the connections that speak its protocol.
func (t *muxTransport) StreamCh() <-chan net.Conn {
	return t.gossip
}

func (t *muxTransport) Shutdown() error {
	close(t.stop)
	if t.httpLn != nil {
		t.httpLn.Close()
	}
	return t.NetTransport.Shutdown()
}

// route drains the inner transport's stream channel, classifies each
// connection by its first byte, and delivers it to the right consumer.
func (t *muxTransport) route() {
	inner := t.NetTransport.StreamCh()
	for {
		select {
		case <-t.stop:
			return
		case conn, ok := <-inner:
			if !ok {
				close(t.gossip)
				return
			}
			go t.classify(conn)
		}
	}
}

// classify reads the first byte and routes accordingly. The deadline keeps
// a silent connection from holding a goroutine forever; the first byte is
// put back so both consumers see the stream from the start.
func (t *muxTransport) classify(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{})
	wrapped := &prefixConn{Conn: conn, r: br}

	if isHTTPStart(first[0]) && t.httpLn != nil {
		if !t.httpLn.deliver(wrapped) {
			conn.Close()
		}
		return
	}
	select {
	case t.gossip <- wrapped:
	case <-t.stop:
		conn.Close()
	}
}

// isHTTPStart says whether a first byte can begin an HTTP request line.
// GET PUT POST HEAD PATCH DELETE OPTIONS CONNECT TRACE cover every method
// a client would send; memberlist's message types are all below 0x20.
func isHTTPStart(b byte) bool {
	switch b {
	case 'G', 'P', 'H', 'D', 'O', 'C', 'T':
		return true
	}
	return false
}

// prefixConn replays the bytes buffered during classification before
// handing the connection on, so the reader sees the full stream.
type prefixConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *prefixConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// chanListener adapts a channel of connections to net.Listener so an
// http.Server can serve connections the mux routes to it.
type chanListener struct {
	ch   chan net.Conn
	done chan struct{}
}

func (l *chanListener) deliver(c net.Conn) bool {
	select {
	case l.ch <- c:
		return true
	case <-l.done:
		return false
	}
}

func (l *chanListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.done:
		return nil, errors.New("listener closed")
	}
}

func (l *chanListener) Close() error {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	return nil
}

func (l *chanListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4zero, Port: 0}
}
