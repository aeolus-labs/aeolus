package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.0.1-dev"

func main() {
	var (
		showVersion = flag.Bool("version", false, "print version and exit")
		configPath  = flag.String("config", "", "path to config file")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("aeolus", version)
		return
	}

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "error: --config is required")
		os.Exit(2)
	}

	fmt.Fprintln(os.Stderr, "aeolus: not yet implemented")
	os.Exit(1)
}
