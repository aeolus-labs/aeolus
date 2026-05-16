package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/aeolus-labs/aeolus/internal/config"
)

// handleOpen opens the running daemon's dashboard in the user's default
// browser. It reads the dashboard address from config (or --config) so
// it follows any custom port the user configured.
func handleOpen(args []string) {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	cfgFlag := fs.String("config", "", "path to config file (default: search ~/.config/aeolus/, ./aeolus.yaml)")
	_ = fs.Parse(args)

	cfgPath, err := resolveConfigPath(*cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aeolus open:", err)
		os.Exit(1)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "aeolus open:", err)
		os.Exit(1)
	}
	if !cfg.Dashboard.Enabled {
		fmt.Fprintln(os.Stderr, "aeolus open: dashboard is disabled in", cfgPath)
		os.Exit(1)
	}

	url := "http://" + cfg.Dashboard.Addr
	if err := openBrowser(url); err != nil {
		fmt.Fprintln(os.Stderr, "aeolus open: couldn't launch a browser:", err)
		fmt.Fprintln(os.Stderr, "Open this URL manually:", url)
		os.Exit(1)
	}
	fmt.Println("Opening", url)
}

// openBrowser launches the OS-default browser at url.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
