package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A lantern's socket is named after its id, and a box's hostname is not always
// its id: bespin's hostname is "Bespin.local" while its lantern runs -id
// bespin. The default guess then points at nothing, and status used to answer
// "no lantern answering here" with a perfectly healthy lantern beside it.
func TestResolveSocket(t *testing.T) {
	touch := func(t *testing.T, dir, id string) string {
		t.Helper()
		p := filepath.Join(dir, "photinus-"+id+".sock")
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatalf("staging %s: %v", p, err)
		}
		return p
	}
	// flagNamed reads a real FlagSet, so build one the way the commands do.
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
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		want := touch(t, dir, "Bespin.local")
		touch(t, dir, "bespin")
		got, err := resolveSocket("Bespin.local", false)
		if err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	})

	t.Run("one lantern under another name is found anyway", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		want := touch(t, dir, "bespin")
		got, err := resolveSocket("Bespin.local", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != want {
			t.Errorf("got %q, want the one socket that exists, %q", got, want)
		}
	})

	t.Run("an id the operator typed is never second-guessed", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		touch(t, dir, "scarif")
		_, idNamed := named(t, "-id", "ewok")
		got, err := resolveSocket("ewok", idNamed)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasSuffix(got, "photinus-ewok.sock") {
			t.Errorf("got %q, want the named id's socket even though it is absent", got)
		}
	})

	t.Run("several lanterns are ambiguous, not a coin toss", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		touch(t, dir, "bespin")
		touch(t, dir, "test-swarm-a")
		got, err := resolveSocket("Bespin.local", false)
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
		dir := t.TempDir()
		t.Setenv("TMPDIR", dir)
		got, err := resolveSocket("bespin", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasSuffix(got, "photinus-bespin.sock") {
			t.Errorf("got %q, want the expected name so the error reads well", got)
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
}
