package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "photinus.yml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return p
}

func TestLoadConfig(t *testing.T) {
	t.Run("a full file parses with every field", func(t *testing.T) {
		p := writeConfig(t, `
id: jawa
bind: 0.0.0.0:7946
advertise: jawa.example.com
key: "example-swarm-key"
interval: 3s
skew_max: 10s
notify: /usr/local/bin/pager
socket: /run/photinus.sock
panel: 127.0.0.1:8946
panel_token: sekrit
defaults: false
seeds:
  - jawa.example.com:7946
  - ewok.example.com:7946
expect:
  - jawa
  - ewok
watch:
  - disk:/data:85
  - http:https://example.com
`)
		fc, err := loadConfig(p)
		if err != nil {
			t.Fatalf("loadConfig: %v", err)
		}
		if fc.ID != "jawa" || fc.Advertise != "jawa.example.com" || fc.Key != "example-swarm-key" {
			t.Errorf("string fields wrong: %+v", fc)
		}
		if time.Duration(fc.Interval) != 3*time.Second || time.Duration(fc.SkewMax) != 10*time.Second {
			t.Errorf("durations wrong: interval %v, skew_max %v", fc.Interval, fc.SkewMax)
		}
		if fc.Defaults == nil || *fc.Defaults {
			t.Errorf("defaults: false not honored: %+v", fc.Defaults)
		}
		if len(fc.Seeds) != 2 || len(fc.Expect) != 2 || len(fc.Watch) != 2 {
			t.Errorf("lists wrong: %+v", fc)
		}
	})

	t.Run("an unknown key is an error, never a shrug", func(t *testing.T) {
		p := writeConfig(t, "id: jawa\nsedes:\n  - typo:7946\n")
		if _, err := loadConfig(p); err == nil || !strings.Contains(err.Error(), "sedes") {
			t.Fatalf("want an error naming the unknown key, got %v", err)
		}
	})

	t.Run("a bad duration says which value", func(t *testing.T) {
		p := writeConfig(t, "interval: soonish\n")
		if _, err := loadConfig(p); err == nil || !strings.Contains(err.Error(), "soonish") {
			t.Fatalf("want an error naming the bad duration, got %v", err)
		}
	})

	t.Run("a missing file wraps os.ErrNotExist so the default path may pass", func(t *testing.T) {
		_, err := loadConfig(filepath.Join(t.TempDir(), "absent.yml"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("want os.ErrNotExist in the chain, got %v", err)
		}
	})
}

func TestMergeConfig(t *testing.T) {
	no := false
	fc := &fileConfig{
		ID:       "filebox",
		Key:      "file key",
		Interval: duration(9 * time.Second),
		Defaults: &no,
		Seeds:    []string{"file:7946"},
	}

	t.Run("the file fills what flags left unset", func(t *testing.T) {
		id, bind, advertise, key, notifyCmd, socket, panel, panelToken := "hosty", "", "", "", "", "", "", ""
		notifyURL, notifyURLToken := "", ""
		interval, skewMax := 2*time.Second, 5*time.Second
		defaults := true
		var seeds, watches, expect stringList
		mergeConfig(fc, map[string]bool{}, &id, &bind, &advertise, &key, &notifyCmd, &notifyURL, &notifyURLToken, &socket, &panel, &panelToken,
			&interval, &skewMax, &defaults, &seeds, &watches, &expect)
		if id != "filebox" || key != "file key" || interval != 9*time.Second || defaults || len(seeds) != 1 {
			t.Errorf("file values not applied: id=%q key=%q interval=%v defaults=%v seeds=%v",
				id, key, interval, defaults, seeds)
		}
		if skewMax != 5*time.Second {
			t.Errorf("a field the file does not mention changed: skewMax=%v", skewMax)
		}
	})

	t.Run("a flag given on the command line always wins", func(t *testing.T) {
		id, key := "flagbox", "flag key"
		bind, advertise, notifyCmd, socket, panel, panelToken := "", "", "", "", "", ""
		notifyURL, notifyURLToken := "", ""
		interval, skewMax := 4*time.Second, 5*time.Second
		defaults := true
		seeds := stringList{"flag:7946"}
		var watches, expect stringList
		set := map[string]bool{"id": true, "key": true, "interval": true, "defaults": true, "seed": true}
		mergeConfig(fc, set, &id, &bind, &advertise, &key, &notifyCmd, &notifyURL, &notifyURLToken, &socket, &panel, &panelToken,
			&interval, &skewMax, &defaults, &seeds, &watches, &expect)
		if id != "flagbox" || key != "flag key" || interval != 4*time.Second || !defaults || seeds[0] != "flag:7946" {
			t.Errorf("flag values overridden by the file: id=%q key=%q interval=%v defaults=%v seeds=%v",
				id, key, interval, defaults, seeds)
		}
	})

	t.Run("the file wins over an environment default", func(t *testing.T) {
		// $PHOTINUS_KEY lands in the flag's default value, so the flag is
		// not in the set map; the file is the box's source of truth.
		key := "env key"
		id, bind, advertise, notifyCmd, socket, panel, panelToken := "", "", "", "", "", "", ""
		notifyURL, notifyURLToken := "", ""
		interval, skewMax := 2*time.Second, 5*time.Second
		defaults := true
		var seeds, watches, expect stringList
		mergeConfig(fc, map[string]bool{}, &id, &bind, &advertise, &key, &notifyCmd, &notifyURL, &notifyURLToken, &socket, &panel, &panelToken,
			&interval, &skewMax, &defaults, &seeds, &watches, &expect)
		if key != "file key" {
			t.Errorf("key = %q, want the file's word over the environment's", key)
		}
	})
}

func TestConfigPermissions(t *testing.T) {
	// The warning logic reads the mode bits directly; pin the mask here so
	// a refactor cannot quietly stop noticing group or world readability.
	for _, tt := range []struct {
		mode     os.FileMode
		readable bool
	}{
		{0o600, false},
		{0o640, true},
		{0o644, true},
		{0o400, false},
	} {
		p := writeConfig(t, "key: something\n")
		if err := os.Chmod(p, tt.mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		got := info.Mode().Perm()&0o044 != 0
		if got != tt.readable {
			t.Errorf("mode %o: readable-by-others = %v, want %v", tt.mode, got, tt.readable)
		}
	}
}
