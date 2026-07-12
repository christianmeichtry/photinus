package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/christianmeichtry/photinus/internal/lantern"
)

func statusCmd(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	hostname, _ := os.Hostname()
	id := fs.String("id", hostname, "name of the local lantern to ask")
	socket := fs.String("socket", "", "unix socket of the local lantern (default: photinus-<id>.sock in the temp dir)")
	asJSON := fs.Bool("json", false, "print the raw status as JSON")
	if err := fs.Parse(args); err != nil {
		return err
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

	fmt.Printf("lantern    %s\n", st.ID)
	fmt.Printf("swarm      %d lit", len(st.Swarm))
	if st.LastKnownSize > len(st.Swarm) {
		fmt.Printf(" of %d last known", st.LastKnownSize)
	}
	fmt.Println()
	for _, m := range st.Swarm {
		fmt.Printf("           %s\n", m)
	}
	if len(st.Subjects) == 0 {
		fmt.Println("checks     none yet")
		return nil
	}
	fmt.Println()
	for _, s := range st.Subjects {
		verdict := "up"
		if s.Down {
			verdict = "DOWN"
		} else if s.Votes > 0 {
			verdict = fmt.Sprintf("suspected by %d, quorum needs %d", s.Votes, s.Needed)
		}
		fmt.Printf("%-5s %-24s %s (%d/%d lanterns agree, swarm of %d)\n",
			s.Check, s.Target, verdict, s.Votes, s.Needed, s.SwarmSize)
		for _, o := range s.Observations {
			state := "up"
			if !o.Healthy {
				state = "down"
			}
			fmt.Printf("      %-24s %s says %s: %s\n", "", o.Observer, state, o.Detail)
		}
	}
	return nil
}
