package main

import (
	"fmt"
	"os"
)

// handleService is the cross-platform dispatcher for `aeolus service`.
// The per-OS implementations live in service_<goos>.go files and
// expose the same set of functions: installService, startService,
// stopService, restartService, statusService, uninstallService,
// logsService, and printServiceUsage.
func handleService(args []string) {
	if len(args) == 0 {
		printServiceUsage()
		os.Exit(2)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "install":
		installService(rest)
	case "start":
		startService()
	case "stop":
		stopService(true)
	case "restart":
		restartService()
	case "status":
		statusService()
	case "uninstall":
		uninstallService()
	case "logs":
		logsService(rest)
	case "help", "--help", "-h":
		printServiceUsage()
	default:
		fmt.Fprintf(os.Stderr, "aeolus service: unknown subcommand %q\n", sub)
		printServiceUsage()
		os.Exit(2)
	}
}
