package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"gopkg.in/yaml.v3"
)

// The config file is the deployment story: a systemd unit or launchd plist
// says `photinus run` and nothing else, and everything the flags do today
// lives in one reviewable YAML file. Precedence, from strongest: a flag
// given on the command line, then the file, then a flag's environment
// default ($PHOTINUS_KEY and friends). Flags stay and win, so nothing
// breaks for anyone running flags today.
//
// Parsing is strict: an unknown key is an error, not a shrug. A config
// file that silently ignores a typo monitors less than the operator
// believes it does, and wrong monitoring conclusions are worse than
// missing ones.

// fileConfig mirrors the run flags one to one. List fields replace their
// flag entirely when the flag is absent; they never merge, so what runs is
// always readable from one place.
type fileConfig struct {
	ID             string   `yaml:"id"`
	Bind           string   `yaml:"bind"`
	Advertise      string   `yaml:"advertise"`
	Key            string   `yaml:"key"`
	Interval       duration `yaml:"interval"`
	SkewMax        duration `yaml:"skew_max"`
	Notify         string   `yaml:"notify"`
	NotifyURL      string   `yaml:"notify_url"`
	NotifyURLToken string   `yaml:"notify_url_token"`
	Socket         string   `yaml:"socket"`
	Panel          string   `yaml:"panel"`
	PanelToken     string   `yaml:"panel_token"`
	Defaults       *bool    `yaml:"defaults"`
	Seeds          []string `yaml:"seeds"`
	Expect         []string `yaml:"expect"`
	Watch          []string `yaml:"watch"`
}

// duration lets YAML carry Go duration strings like "2s" or "25h".
type duration time.Duration

func (d *duration) UnmarshalYAML(value *yaml.Node) error {
	v, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("parsing duration %q: %w", value.Value, err)
	}
	*d = duration(v)
	return nil
}

// defaultConfigPath names the per-OS location `photinus run` looks at when
// -config is not given. On macOS the per-user location wins if it exists,
// otherwise the machine-wide one is the default.
func defaultConfigPath() string {
	switch runtime.GOOS {
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, "Library", "Application Support", "photinus", "photinus.yml")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return "/usr/local/etc/photinus/photinus.yml"
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "photinus", "photinus.yml")
	default:
		return "/etc/photinus/photinus.yml"
	}
}

// loadConfig reads and strictly parses one YAML config file. A missing
// file surfaces as an error wrapping os.ErrNotExist; the caller decides
// whether that is fine (default path) or fatal (explicit -config).
func loadConfig(path string) (*fileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return &fc, nil
}

// mergeConfig folds the file underneath the flags: every value the
// operator did not set on the command line (per the set map from
// fs.Visit) takes the file's word when the file says anything.
func mergeConfig(fc *fileConfig, set map[string]bool,
	id, bind, advertise, key, notifyCmd, notifyURL, notifyURLToken, socket, panel, panelToken *string,
	interval, skewMax *time.Duration, defaults *bool,
	seeds, watches, expect *stringList) {

	str := func(flagName string, dst *string, v string) {
		if !set[flagName] && v != "" {
			*dst = v
		}
	}
	str("id", id, fc.ID)
	str("bind", bind, fc.Bind)
	str("advertise", advertise, fc.Advertise)
	str("key", key, fc.Key)
	str("notify", notifyCmd, fc.Notify)
	str("notify-url", notifyURL, fc.NotifyURL)
	str("notify-url-token", notifyURLToken, fc.NotifyURLToken)
	str("socket", socket, fc.Socket)
	str("panel", panel, fc.Panel)
	str("panel-token", panelToken, fc.PanelToken)
	if !set["interval"] && fc.Interval != 0 {
		*interval = time.Duration(fc.Interval)
	}
	if !set["skew-max"] && fc.SkewMax != 0 {
		*skewMax = time.Duration(fc.SkewMax)
	}
	if !set["defaults"] && fc.Defaults != nil {
		*defaults = *fc.Defaults
	}
	if !set["seed"] && len(fc.Seeds) > 0 {
		*seeds = fc.Seeds
	}
	if !set["watch"] && len(fc.Watch) > 0 {
		*watches = fc.Watch
	}
	if !set["expect"] && len(fc.Expect) > 0 {
		*expect = fc.Expect
	}
}
