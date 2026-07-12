// photinus is decentralized mesh monitoring. Every host runs a lantern; the
// lanterns watch each other and alert only when the swarm agrees.
package main

import (
	"fmt"
	"os"

	"github.com/christianmeichtry/photinus/internal/version"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(os.Args[2:])
	case "status":
		err = statusCmd(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Println("photinus " + version.Release)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "photinus: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "photinus: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `photinus, mesh monitoring with no center

Usage:
  photinus run      start a lantern on this host
  photinus status   ask the local lantern what the swarm sees
  photinus version  print the release

Run 'photinus <command> -h' for the flags of each command.
`)
}
