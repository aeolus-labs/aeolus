//go:build !darwin && !linux

package main

import (
	"fmt"
	"os"
	"runtime"
)

// Stub service handler for platforms without a native runtime
// integration. The aeolus daemon still runs in the foreground via
// `aeolus` — only the auto-start-on-login wiring is unavailable here.

func printServiceUsage() {
	fmt.Printf(`aeolus service is not yet implemented on %s.

Run the daemon in the foreground instead:

    aeolus --config %s

Auto-start integration is currently available on macOS (launchd) and
Linux (systemd user units). PRs adding native Windows support are
welcome — see CONTRIBUTING.md.
`, runtime.GOOS, defaultConfigPath())
}

func unsupported() {
	fmt.Fprintf(os.Stderr, "aeolus service is not supported on %s — run `aeolus` in the foreground instead.\n", runtime.GOOS)
	os.Exit(1)
}

func installService(args []string)   { unsupported() }
func startService()                  { unsupported() }
func stopService(printOnSuccess bool) { unsupported() }
func restartService()                { unsupported() }
func statusService()                 { unsupported() }
func uninstallService()              { unsupported() }
func logsService(args []string)      { unsupported() }
