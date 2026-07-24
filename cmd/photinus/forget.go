package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"
)

// forgetCmd retires one subject across the swarm through the local lantern's
// socket. A subject is a check and a target, exactly as status prints them:
//
//	photinus forget cert aslec.ch:443
//	photinus forget http https://aslec.ch
//
// The lantern drops its own observations, tombstones the subject so
// anti-entropy cannot resurrect it, and gossips the same to every peer. Use
// it after removing a watch, or to clear a decommissioned box, instead of
// waiting out the observation's TTL (five hours for a cert).
func forgetCmd(args []string) error {
	fs := flag.NewFlagSet("forget", flag.ExitOnError)
	hostname, _ := os.Hostname()
	id := fs.String("id", hostname, "name of the local lantern to ask")
	socket := fs.String("socket", "", "unix socket of the local lantern (default: photinus-<id>.sock in the temp dir)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: photinus forget <check> <target>, e.g. photinus forget cert aslec.ch:443")
	}
	check, target := fs.Arg(0), fs.Arg(1)

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
	u := "http://photinus/forget?check=" + url.QueryEscape(check) + "&target=" + url.QueryEscape(target)
	resp, err := client.Post(u, "", nil)
	if err != nil {
		return fmt.Errorf("no lantern answering on %s, is one running here: %w", sockPath, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the lantern refused: %s: %s", resp.Status, body)
	}
	fmt.Print(string(body))
	return nil
}
