package main

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/christianmeichtry/photinus/internal/lantern"
	"github.com/christianmeichtry/photinus/internal/version"
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
	// The worker's cache name carries the release, so a browser that cached
	// an older panel sees a byte-different worker after an upgrade and
	// replaces its shell instead of serving the old one forever.
	swJS := bytes.ReplaceAll(mustAsset("sw.js"), []byte("@RELEASE@"), []byte(version.Release))
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		w.Write(swJS)
	})
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

	// The dead man's switch door: a job pings /pulse/<name> on any lantern
	// when it finishes, and the receipt gossips from there. GET and POST
	// both work so plain curl in a cron line is enough. The token rule is
	// the same as /status.json: a ping writes an observation, so it is
	// guarded like the data, not like the shell.
	mux.HandleFunc("/pulse/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if token != "" && !bearerOK(r, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			http.Error(w, "ping a pulse with GET or POST", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/pulse/")
		if err := validPulseName(name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		declared := lan.Pulse(name, time.Now().UTC())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if declared {
			fmt.Fprintf(w, "pulse %s recorded\n", name)
			return
		}
		// Recording first and declaring after is a fine order of work, so
		// the ping still lands; the hint tells the operator the silence is
		// not being watched from here yet.
		fmt.Fprintf(w, "pulse %s recorded, not declared on this lantern\n", name)
	})

	// The push door: the app hands over its APNs device token here, at any
	// lantern, and the registration gossips to every box, because the
	// lantern elected to page is rarely the one the phone reached. Guarded
	// like the data: a registration means pages, so it needs the token.
	mux.HandleFunc("/push/register", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if token != "" && !bearerOK(r, token) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "a bearer token is required", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "register a push token with POST", http.StatusMethodNotAllowed)
			return
		}
		var reg struct {
			Token string `json:"token"`
			Env   string `json:"env"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&reg); err != nil {
			http.Error(w, "the body is JSON: {\"token\": hex, \"env\": \"sandbox\"|\"production\"}", http.StatusBadRequest)
			return
		}
		if err := validPushToken(reg.Token); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if reg.Env != "sandbox" && reg.Env != "production" {
			http.Error(w, "env is \"sandbox\" or \"production\"", http.StatusBadRequest)
			return
		}
		lan.RegisterPush(reg.Token, reg.Env, time.Now().UTC())
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "push registration recorded")
	})

	return mux
}

// validPushToken gates what may claim to be an APNs device token: hex,
// with generous length bounds because Apple documents the format as
// opaque, small enough that a registration always fits a gossip packet.
func validPushToken(token string) error {
	if len(token) < 16 || len(token) > 200 {
		return fmt.Errorf("a device token is 16 to 200 hex characters")
	}
	for _, r := range token {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return fmt.Errorf("a device token is hex; %q is not", token)
		}
	}
	return nil
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
