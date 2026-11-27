package main

import (
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A lantern's socket is named after its id, and a box's hostname is not always
// its id: bespin reports "Bespin.local" and runs -id bespin. The default guess
// then points at nothing, and status used to answer "no lantern answering
// here" with a healthy lantern beside it.
//
// The directory is a parameter rather than TMPDIR, because os.TempDir reads
// TMP and TEMP on Windows and would make these tests glob the real temp
// directory there. Short parent: a unix socket path is capped near 104 bytes.
func TestResolveSocket(t *testing.T) {
	tempDir := func(t *testing.T) string {
		t.Helper()
		dir, err := os.MkdirTemp("", "ph")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		return dir
	}
	// lantern is a socket something actually listens on.
	lantern := func(t *testing.T, dir, id string) string {
		t.Helper()
		p := socketFor(dir, id)
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatalf("listening on %s: %v", p, err)
		}
		t.Cleanup(func() { ln.Close() })
		return p
	}
	// corpse is what a kill -9 leaves behind: the socket file, with nothing
	// behind it. Only a graceful shutdown removes it.
	corpse := func(t *testing.T, dir, id string) string {
		t.Helper()
		p := socketFor(dir, id)
		ln, err := net.Listen("unix", p)
		if err != nil {
			t.Fatalf("listening on %s: %v", p, err)
		}
		ln.Close()
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			// Closing a unix listener removes the file; put a plain one back,
			// which is also what a stray file in /tmp looks like.
			t.Fatalf("staging a dead socket: %v", err)
		}
		return p
	}
	named := func(t *testing.T, args ...string) (*flag.FlagSet, bool) {
		t.Helper()
		fs := flag.NewFlagSet("status", flag.ContinueOnError)
		fs.String("id", "Bespin.local", "")
		if err := fs.Parse(args); err != nil {
			t.Fatalf("parsing %v: %v", args, err)
		}
		return fs, flagNamed(fs, "id")
	}

	t.Run("the socket named after the id wins", func(t *testing.T) {
		dir := tempDir(t)
		want := lantern(t, dir, "Bespin.local")
		lantern(t, dir, "bespin")
		got, err := resolveSocket(dir, "Bespin.local", false)
		if err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})

	t.Run("one lantern under another name is found anyway", func(t *testing.T) {
		dir := tempDir(t)
		want := lantern(t, dir, "bespin")
		got, err := resolveSocket(dir, "Bespin.local", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != want {
			t.Errorf("got %q, want the one lantern that answers, %q", got, want)
		}
	})

	t.Run("a dead lantern's socket is not a lantern", func(t *testing.T) {
		dir := tempDir(t)
		corpse(t, dir, "ghost") // a kill -9 drill, or a disposable swarm
		want := lantern(t, dir, "bespin")
		got, err := resolveSocket(dir, "Bespin.local", false)
		if err != nil {
			t.Fatalf("a leftover socket file produced an error: %v", err)
		}
		if got != want {
			t.Errorf("got %q, want the live lantern %q: a socket file is not a lantern", got, want)
		}
	})

	t.Run("only corpses means no lantern, and the expected name is reported", func(t *testing.T) {
		dir := tempDir(t)
		corpse(t, dir, "ghost")
		corpse(t, dir, "test-swarm-a")
		got, err := resolveSocket(dir, "bespin", false)
		if err != nil {
			t.Fatalf("leftover files read as an ambiguity: %v", err)
		}
		if got != socketFor(dir, "bespin") {
			t.Errorf("got %q, want the expected name %q so the error reads well", got, socketFor(dir, "bespin"))
		}
	})

	t.Run("a plain file that is not a socket is ignored", func(t *testing.T) {
		dir := tempDir(t)
		if err := os.WriteFile(socketFor(dir, "planted"), []byte("not a socket"), 0o600); err != nil {
			t.Fatal(err)
		}
		want := lantern(t, dir, "bespin")
		got, err := resolveSocket(dir, "Bespin.local", false)
		if err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})

	t.Run("an id the operator typed is never second-guessed", func(t *testing.T) {
		dir := tempDir(t)
		lantern(t, dir, "scarif")
		_, idNamed := named(t, "-id", "ewok")
		got, err := resolveSocket(dir, "ewok", idNamed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasSuffix(got, "photinus-ewok.sock") {
			t.Errorf("got %q, want the named id's socket even though it is absent", got)
		}
	})

	t.Run("several live lanterns are ambiguous, not a coin toss", func(t *testing.T) {
		dir := tempDir(t)
		lantern(t, dir, "bespin")
		lantern(t, dir, "test-swarm-a")
		got, err := resolveSocket(dir, "Bespin.local", false)
		if err == nil {
			t.Fatalf("two lanterns resolved to %q instead of erroring", got)
		}
		for _, want := range []string{"bespin", "test-swarm-a", "-id"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("no lantern at all still names what was expected", func(t *testing.T) {
		dir := tempDir(t)
		got, err := resolveSocket(dir, "bespin", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != socketFor(dir, "bespin") {
			t.Errorf("got %q, want %q", got, socketFor(dir, "bespin"))
		}
	})

	t.Run("flagNamed tells a typed flag from its default", func(t *testing.T) {
		if _, yes := named(t, "-id", "bespin"); !yes {
			t.Error("a typed -id read as not named")
		}
		if _, no := named(t); no {
			t.Error("an untouched -id read as named")
		}
	})

	t.Run("socketFor does not double the separator", func(t *testing.T) {
		if got := socketFor("/tmp/", "bespin"); got != filepath.Join("/tmp", "photinus-bespin.sock") {
			t.Errorf("got %q, want a clean path", got)
		}
	})
}
