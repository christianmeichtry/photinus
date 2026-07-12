package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/christianmeichtry/photinus/internal/lantern"
	"github.com/christianmeichtry/photinus/internal/quorum"
)

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	hostname, _ := os.Hostname()
	id := fs.String("id", hostname, "name of the local lantern to ask")
	socket := fs.String("socket", "", "unix socket of the local lantern (default: photinus-<id>.sock in the temp dir)")
	asJSON := fs.Bool("json", false, "print the raw status as JSON")
	verbose := fs.Bool("v", false, "also list every lantern's observation under each subject")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q, flags must come before it", fs.Arg(0))
	}

	sockPath := *socket
	if sockPath == "" {
		sockPath = defaultSocket(*id)
	}

	client := http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sockPath)
			},
		},
	}
	resp, err := client.Get("http://photinus/status")
	if err != nil {
		return fmt.Errorf("no lantern answering on %s, is one running here: %w", sockPath, err)
	}
	defer resp.Body.Close()

	var st lantern.Status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return fmt.Errorf("reading status from %s: %w", sockPath, err)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}

	printStatus(st, *verbose)
	return nil
}

// Colors are decoration, never information: everything they say is also in
// the words, so a pipe or NO_COLOR loses nothing.
type palette struct{ down, warn, up, dim, bold, reset string }

func colors() palette {
	fi, err := os.Stdout.Stat()
	tty := err == nil && fi.Mode()&os.ModeCharDevice != 0
	if !tty || os.Getenv("NO_COLOR") != "" {
		return palette{}
	}
	return palette{
		down:  "\033[1;31m",
		warn:  "\033[33m",
		up:    "\033[32m",
		dim:   "\033[2m",
		bold:  "\033[1m",
		reset: "\033[0m",
	}
}

func stateRank(s lantern.SubjectStatus) int {
	switch {
	case s.Voters == 0:
		return 3
	case s.State == quorum.StateDown:
		return 0
	case s.State == quorum.StateWarn:
		return 1
	case s.Votes > 0:
		return 2
	default:
		return 4
	}
}

// hostSection groups everything the swarm knows about one box: the liveness
// and clock verdicts feed the header, the checks become rows.
type hostSection struct {
	target string
	live   *lantern.SubjectStatus
	skew   *lantern.SubjectStatus
	rows   []lantern.SubjectStatus
}

func printStatus(st lantern.Status, verbose bool) {
	c := colors()

	downs, warns := 0, 0
	for _, s := range st.Subjects {
		switch {
		case s.Voters == 0:
		case s.State == quorum.StateDown:
			downs++
		case s.State == quorum.StateWarn:
			warns++
		}
	}

	head := fmt.Sprintf("%slantern %s%s %s·%s swarm %d lit of %d known", c.bold, st.ID, c.reset, c.dim, c.reset, len(st.Swarm), st.LastKnownSize)
	switch {
	case downs == 0 && warns == 0 && len(st.Subjects) > 0:
		head += fmt.Sprintf(" %s·%s %sall quiet%s", c.dim, c.reset, c.up, c.reset)
	case downs > 0 || warns > 0:
		var parts []string
		if downs > 0 {
			parts = append(parts, fmt.Sprintf("%s%d down%s", c.down, downs, c.reset))
		}
		if warns > 0 {
			parts = append(parts, fmt.Sprintf("%s%d warning%s%s", c.warn, warns, plural(warns), c.reset))
		}
		head += fmt.Sprintf(" %s·%s %s", c.dim, c.reset, strings.Join(parts, ", "))
	}
	fmt.Println(head)

	if len(st.Subjects) == 0 {
		fmt.Println("no checks reporting yet")
		return
	}

	// A target is a box when the swarm holds host-shaped knowledge about
	// it: liveness, clock, or its own local reports. Everything else is a
	// watched service.
	sections := make(map[string]*hostSection)
	for _, s := range st.Subjects {
		if s.Check == "lantern" || s.Check == "skew" || s.Authority {
			if sections[s.Target] == nil {
				sections[s.Target] = &hostSection{target: s.Target}
			}
		}
	}
	var services []lantern.SubjectStatus
	for _, s := range st.Subjects {
		sec := sections[s.Target]
		if sec == nil {
			services = append(services, s)
			continue
		}
		switch s.Check {
		case "lantern":
			v := s
			sec.live = &v
		case "skew":
			v := s
			sec.skew = &v
		default:
			sec.rows = append(sec.rows, s)
		}
	}

	names := make([]string, 0, len(sections))
	for name := range sections {
		names = append(names, name)
	}
	sort.Strings(names)

	// Version skew is operator information during rolling upgrades; a
	// uniform swarm keeps quiet about it.
	mixed := false
	var first string
	for _, v := range st.Versions {
		if first == "" {
			first = v
		} else if v != first {
			mixed = true
		}
	}

	for _, name := range names {
		sec := sections[name]
		fmt.Println()
		head := hostHeader(sec, c)
		if mixed {
			if v := st.Versions[name]; v != "" {
				head += fmt.Sprintf(" %s· runs %s%s", c.dim, v, c.reset)
			}
		}
		fmt.Println(head)
		if verbose {
			for _, s := range []*lantern.SubjectStatus{sec.live, sec.skew} {
				if s == nil {
					continue
				}
				for _, o := range s.Observations {
					fmt.Printf("  %s%s says %s %s: %s%s\n", c.dim, o.Observer, s.Check, o.State, o.Detail, c.reset)
				}
			}
		}
		sort.Slice(sec.rows, func(i, j int) bool {
			a, b := sec.rows[i], sec.rows[j]
			if r1, r2 := stateRank(a), stateRank(b); r1 != r2 {
				return r1 < r2
			}
			return a.Check < b.Check
		})
		checkW := 0
		for _, s := range sec.rows {
			checkW = max(checkW, len(s.Check))
		}
		for _, s := range sec.rows {
			word, color := stateWord(s, c)
			fmt.Printf("  %s%-5s%s %-*s %s\n", color, word, c.reset, checkW, s.Check, trimDetail(s))
			if verbose {
				for _, o := range s.Observations {
					fmt.Printf("        %s%s says %s: %s%s\n", c.dim, o.Observer, o.State, o.Detail, c.reset)
				}
			}
		}
	}

	if len(services) > 0 {
		fmt.Println()
		fmt.Printf("%swatched services%s\n", c.bold, c.reset)
		sort.Slice(services, func(i, j int) bool {
			a, b := services[i], services[j]
			if r1, r2 := stateRank(a), stateRank(b); r1 != r2 {
				return r1 < r2
			}
			return a.Target < b.Target
		})
		targetW := 0
		for _, s := range services {
			targetW = max(targetW, len(s.Target))
		}
		for _, s := range services {
			word, color := stateWord(s, c)
			fmt.Printf("  %s%-5s%s %-*s %s %s(%s)%s\n",
				color, word, c.reset, targetW, s.Target, s.Detail, c.dim, agreement(s.Decision), c.reset)
			if verbose {
				for _, o := range s.Observations {
					fmt.Printf("        %s%s says %s: %s%s\n", c.dim, o.Observer, o.State, o.Detail, c.reset)
				}
			}
		}
	}
}

// hostHeader compresses a box's liveness and clock into one line:
// "jawa · lit, 2 vouch · clock in step".
func hostHeader(sec *hostSection, c palette) string {
	head := c.bold + sec.target + c.reset
	if s := sec.live; s != nil {
		switch {
		case s.Voters == 0:
			head += fmt.Sprintf(" %s· no fresh word%s", c.dim, c.reset)
		case s.State == quorum.StateDown:
			head += fmt.Sprintf(" %s·%s %sDARK, %d/%d agree, quorum %d%s", c.dim, c.reset, c.down, s.Votes, s.Voters, s.Needed, c.reset)
		case s.Votes > 0:
			head += fmt.Sprintf(" %s·%s %ssuspected by %d of %d, quorum %d%s", c.dim, c.reset, c.warn, s.Votes, s.Voters, s.Needed, c.reset)
		default:
			head += fmt.Sprintf(" %s·%s %slit%s%s, %d vouch%s", c.dim, c.reset, c.up, c.reset, c.dim, s.Voters, c.reset)
		}
	}
	if s := sec.skew; s != nil && s.Voters > 0 {
		clock := "clock in step"
		color := c.dim
		if s.State != quorum.StateUp || s.Votes > 0 {
			// The detail says "clock of jawa runs about 40s behind mine";
			// inside jawa's own section the name and the mine are noise.
			clock = strings.ReplaceAll(s.Detail, "clock of "+sec.target+" ", "clock ")
			clock = strings.TrimSuffix(strings.ReplaceAll(clock, " with mine", ""), " mine")
			color = c.warn
		}
		head += fmt.Sprintf(" %s·%s %s%s%s", c.dim, c.reset, color, clock, c.reset)
	}
	return head
}

func stateWord(s lantern.SubjectStatus, c palette) (string, string) {
	switch {
	case s.Voters == 0:
		return "?", c.dim
	case s.State == quorum.StateDown:
		return "DOWN", c.down
	case s.State == quorum.StateWarn:
		return "WARN", c.warn
	case s.Votes > 0:
		return "sus", c.warn
	default:
		return "up", c.up
	}
}

// trimDetail drops the words the row already says: inside a host section a
// row named cpu does not need its detail to begin with "cpu is".
func trimDetail(s lantern.SubjectStatus) string {
	d := s.Detail
	base := s.Check
	if strings.HasPrefix(base, "disk:") {
		path := strings.TrimPrefix(base, "disk:")
		d = strings.TrimPrefix(d, "disk "+path+" is ")
		return d
	}
	d = strings.TrimPrefix(d, base+" is ")
	return d
}

func agreement(d quorum.Decision) string {
	if d.Voters == 0 {
		return "no fresh word"
	}
	if d.Authority {
		return "own report"
	}
	if d.State != quorum.StateUp || d.Votes > 0 {
		return fmt.Sprintf("%d/%d, quorum %d", d.Votes, d.Voters, d.Needed)
	}
	return fmt.Sprintf("%d lantern%s", d.Voters, plural(d.Voters))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
