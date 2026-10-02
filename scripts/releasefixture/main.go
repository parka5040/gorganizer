package main

import (
	"flag"
	"fmt"
	"os"
)

// main runs the local signed-release fixture tool.
func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: releasefixture bundle|serve [options]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "bundle":
		flags := flag.NewFlagSet("bundle", flag.ExitOnError)
		version := flags.String("version", "", "release version")
		bin := flags.String("bin", "", "directory containing fixture-tagged binaries")
		gui := flags.String("gui", "", "GUI executable")
		out := flags.String("out", "", "output directory")
		flags.Parse(os.Args[2:])
		if flags.NArg() != 0 || *bin == "" || *gui == "" || *out == "" {
			err = fmt.Errorf("bundle requires --version, --bin, --gui and --out")
		} else {
			err = makeBundle(*version, *bin, *gui, *out)
		}
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ExitOnError)
		dir := flags.String("dir", "", "bundle directory")
		listen := flags.String("listen", "127.0.0.1:0", "loopback listener")
		caOut := flags.String("ca-out", "", "CA certificate path")
		stateDir := flags.String("state-dir", "", "request log directory")
		mode := flags.String("mode", "normal", "response mode")
		flags.Parse(os.Args[2:])
		if flags.NArg() != 0 || *dir == "" || *caOut == "" || *stateDir == "" {
			err = fmt.Errorf("serve requires --dir, --ca-out and --state-dir")
		} else {
			err = serve(*dir, *listen, *caOut, *stateDir, *mode)
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
