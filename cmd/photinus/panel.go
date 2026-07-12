package main

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/christianmeichtry/photinus/internal/lantern"
)

//go:embed panel.html manifest.json sw.js icon-180.png icon-192.png icon-512.png
var panelFS embed.FS

// statusHandler serves the panel and its status, the same facts everywhere.
// It is mounted on both the shared gossip port and the optional -panel
// listener, so a browser and the app reach identical data.
//
// A token, when set, guards the data (/status.json) with a bearer check;
// the HTML shell and static assets carry no data and stay open so the page
// can load and then ask for the token. CORS is permissive by design: any
// door may be queried cross-origin by a client that holds the token, which
// is exactly how the app fails over from one lantern to the next.
func statusHandler(token string, lan *lantern.Lantern) http.Handler {
	page := mustAsset("panel.html")
	mux := http.NewServeMux()

	static := func(path, ctype string) {
		body := mustAsset(path)
		mux.HandleFunc("/"+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Write(body)
		})
	}
	static("manifest.json", "application/manifest+json")
	static("sw.js", "text/javascript")
	static("icon-180.png", "image/png")
	static("icon-192.png", "image/png")
	static("icon-512.png", "image/png")

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})

	mux.HandleFunc("/status.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
		// A preflight carries no credentials, so it must pass before any
		// auth check or a cross-origin fetch never gets to send its token.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if token != "" && !bearerOK(r, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lan.Status())
	})

	return mux
}

// bearerOK checks the Authorization header against the token in constant
// time, so a wrong token leaks no timing about how wrong it was.
func bearerOK(r *http.Request, token string) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(token)) == 1
}

func mustAsset(name string) []byte {
	b, err := panelFS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("embedded asset %s missing: %v", name, err))
	}
	return b
}

// servePanel runs the status handler on its own TCP address, the -panel
// listener. The shared-gossip-port path uses the same handler via the swarm
// transport instead.
func servePanel(addr, token string, lan *lantern.Lantern) (*http.Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("opening panel address %s: %w", addr, err)
	}
	srv := &http.Server{Handler: statusHandler(token, lan)}
	go srv.Serve(ln)
	return srv, nil
}
