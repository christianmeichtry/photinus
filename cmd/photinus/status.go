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

	printTable(st, *verbose)
	return nil
}

// Colors are decoration, never information: everything they say is also in
// the words, so a pipe or NO_COLOR loses nothing.
type palette struct{ down, warn, up, dim, reset string }

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
		reset: "\033[0m",
	}
}

func stateRank(s string) int {
	switch s {
	case quorum.StateDown:
		return 0
	case quorum.StateWarn:
		return 1
	default:
		return 2
	}
}

func printTable(st lantern.Status, verbose bool) {
	c := colors()

	downs, warns := 0, 0
	for _, s := range st.Subjects {
		switch s.State {
		case quorum.StateDown:
			downs++
		case quorum.StateWarn:
			warns++
		}
	}

	head := fmt.Sprintf("lantern %s %s·%s swarm %d lit of %d known", st.ID, c.dim, c.reset, len(st.Swarm), st.LastKnownSize)
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
	fmt.Println()

	sort.Slice(st.Subjects, func(i, j int) bool {
		a, b := st.Subjects[i], st.Subjects[j]
		if r1, r2 := stateRank(a.State), stateRank(b.State); r1 != r2 {
			return r1 < r2
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Target < b.Target
	})

	checkW, targetW, agreeW := len("CHECK"), len("TARGET"), len("AGREEMENT")
	for _, s := range st.Subjects {
		checkW = max(checkW, len(s.Check))
		targetW = max(targetW, len(s.Target))
		agreeW = max(agreeW, len(agreement(s.Decision)))
	}

	fmt.Printf("%s%-6s %-*s %-*s %-*s %s%s\n", c.dim, "STATE", checkW, "CHECK", targetW, "TARGET", agreeW, "AGREEMENT", "DETAIL", c.reset)
	for _, s := range st.Subjects {
		state, color := "up", c.up
		switch {
		case s.Voters == 0:
			// Nobody has a fresh observation. Stale is unknown, and
			// unknown must not dress up as healthy.
			state, color = "?", c.dim
		case s.State == quorum.StateDown:
			state, color = "DOWN", c.down
		case s.State == quorum.StateWarn:
			state, color = "WARN", c.warn
		case s.Votes > 0:
			// Accused but short of quorum. One lantern's opinion is not
			// an outage, but pairing a green up with an accusation would
			// contradict the detail column.
			state, color = "sus", c.warn
		}
		fmt.Printf("%s%-6s%s %-*s %-*s %-*s %s\n",
			color, state, c.reset, checkW, s.Check, targetW, s.Target, agreeW, agreement(s.Decision), s.Detail)
		if verbose {
			for _, o := range s.Observations {
				fmt.Printf("%s       %-*s %s says %s: %s%s\n",
					c.dim, checkW, "", o.Observer, o.State, o.Detail, c.reset)
			}
		}
	}
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
