package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/christianmeichtry/photinus/internal/lantern"
)

//go:embed panel.html
var panelFS embed.FS

// servePanel answers a read-only web panel from the lantern's memory, the
// same facts the unix socket serves, with an HTML face. Any lantern can
// serve it, which is the point: there is no dashboard host to lose. It is
// read-only and unauthenticated; anything public goes behind a reverse
// proxy with auth, and the flag help says so.
func servePanel(addr string, lan *lantern.Lantern) (*http.Server, error) {
	page, err := panelFS.ReadFile("panel.html")
	if err != nil {
		return nil, fmt.Errorf("loading the embedded panel: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("/status.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lan.Status())
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("opening panel address %s: %w", addr, err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return srv, nil
}
