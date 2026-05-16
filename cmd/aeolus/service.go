package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// macOS-only for now. Linux systemd / Windows service variants are
// documented in RELEASING.md and can land later.
const launchAgentLabel = "com.aeolus-labs.aeolus"

func handleService(args []string) {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr,
			"aeolus service: only macOS launchd is wired up today.\n"+
				"On Linux: write a systemd user unit; on Windows: register a Service.\n"+
				"See RELEASING.md for templates.")
		os.Exit(1)
	}
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
		stopService(false)
		startService()
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

func printServiceUsage() {
	fmt.Print(`aeolus service — manage the aeolus daemon via macOS launchd.

Subcommands:
  install [--force]      Write the launchd plist (~/Library/LaunchAgents).
  start                  Load the plist into launchd and start the daemon.
  stop                   Bootout the daemon.
  restart                stop then start.
  status                 Report whether the daemon is running.
  uninstall              Bootout + remove the plist.
  logs [-f]              Print recent daemon stderr (~/Library/Logs/aeolus/).
                         -f follows the log like tail -f.
`)
}

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchAgentLabel+".plist")
}

func logPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Logs", "aeolus", "aeolus.log")
}

// plistContent renders the launchd plist with the running binary's
// absolute path and the canonical config location.
func plistContent() string {
	exe := findSelfPath()
	cfg := defaultConfigPath()
	log := logPath()
	path := resolveLaunchdPath()
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>--config</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
        <key>Crashed</key>
        <true/>
    </dict>
    <key>StandardOutPath</key>
    <string>%s</string>
    <key>StandardErrorPath</key>
    <string>%s</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>%s</string>
    </dict>
</dict>
</plist>
`, launchAgentLabel, exe, cfg, log, log, path)
}

// resolveLaunchdPath returns a PATH suitable for embedding in the
// launchd plist. launchd-spawned processes inherit only a minimal
// environment, so common Node/Python managers (nvm, fnm, Volta, pyenv,
// asdf) installed via the user's shell rc files are invisible. We
// resolve PATH by launching the user's interactive login shell once at
// `service install` time and capturing what it exports — same trick
// VS Code and other Mac GUI apps use. The fallback list is the
// pre-fix behavior in case the shell probe fails or returns garbage.
func resolveLaunchdPath() string {
	const fallback = "/usr/local/bin:/usr/bin:/bin:/opt/homebrew/bin"

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/zsh"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '%s' "$PATH"`)
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"service install: couldn't read your shell's PATH (%v); falling back to the minimal default.\n", err)
		return fallback
	}

	path := strings.TrimSpace(string(out))
	if path == "" || strings.ContainsAny(path, "\n\r<>&'\"") {
		fmt.Fprintln(os.Stderr,
			"service install: shell returned an unusable PATH; falling back to the minimal default.")
		return fallback
	}
	return path
}

func installService(args []string) {
	fs := flag.NewFlagSet("service install", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing plist")
	_ = fs.Parse(args)

	p := plistPath()
	if _, err := os.Stat(p); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "%s already exists. Use --force to overwrite.\n", p)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(logPath()), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "service install: couldn't create log directory:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "service install: couldn't create LaunchAgents directory:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(p, []byte(plistContent()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "service install: couldn't write plist:", err)
		os.Exit(1)
	}

	// Make sure a config exists. If not, drop a starter alongside the plist
	// so `aeolus service start` actually has something to load.
	cfg := defaultConfigPath()
	if _, err := os.Stat(cfg); err != nil && os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(cfg), 0o755)
		if err := os.WriteFile(cfg, []byte(starterConfig), 0o600); err == nil {
			fmt.Printf("Created %s with a starter template.\n", cfg)
		}
	}

	fmt.Printf("Installed %s\n\n", p)
	fmt.Println("Next steps:")
	fmt.Println("  aeolus service start    (loads the plist and starts the daemon)")
	fmt.Println("  aeolus open             (opens the dashboard once it's running)")
}

func startService() {
	if _, err := os.Stat(plistPath()); err != nil {
		fmt.Fprintln(os.Stderr, "service: plist not found — run `aeolus service install` first.")
		os.Exit(1)
	}
	target := fmt.Sprintf("gui/%d", os.Getuid())
	out, err := runCmd("launchctl", "bootstrap", target, plistPath())
	if err != nil {
		if alreadyBootstrapped(out) {
			fmt.Println("Already loaded — kickstarting...")
			if _, kerr := runCmd("launchctl", "kickstart", "-k", target+"/"+launchAgentLabel); kerr != nil {
				fmt.Fprintln(os.Stderr, "service start: kickstart failed:", kerr)
				os.Exit(1)
			}
			fmt.Println("Restarted.")
			return
		}
		fmt.Fprintln(os.Stderr, "service start failed:", err)
		fmt.Fprintln(os.Stderr, strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	fmt.Println("Started.")
}

func stopService(printOnSuccess bool) {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)
	out, err := runCmd("launchctl", "bootout", target)
	if err != nil {
		// "could not find service" / "no such process" → already stopped.
		s := strings.ToLower(string(out))
		if strings.Contains(s, "could not find") || strings.Contains(s, "no such") {
			if printOnSuccess {
				fmt.Println("Not running.")
			}
			return
		}
		fmt.Fprintln(os.Stderr, "service stop:", err)
		fmt.Fprintln(os.Stderr, strings.TrimSpace(string(out)))
		// Don't exit — restart still wants to call start after.
		return
	}
	if printOnSuccess {
		fmt.Println("Stopped.")
	}
}

func statusService() {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel)
	out, err := runCmd("launchctl", "print", target)
	if err != nil {
		fmt.Println("not loaded")
		os.Exit(1)
	}
	text := string(out)
	switch {
	case strings.Contains(text, "state = running"):
		fmt.Println("running")
	case strings.Contains(text, "state = exited") || strings.Contains(text, "state = not running"):
		fmt.Println("loaded, not running")
	default:
		fmt.Println("loaded")
	}
}

func uninstallService() {
	stopService(false)
	p := plistPath()
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "service uninstall:", err)
		os.Exit(1)
	}
	fmt.Println("Uninstalled.")
}

func logsService(args []string) {
	fs := flag.NewFlagSet("service logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "follow the log (tail -f)")
	lines := fs.Int("n", 50, "number of trailing lines to print")
	_ = fs.Parse(args)

	p := logPath()
	if _, err := os.Stat(p); err != nil {
		fmt.Fprintln(os.Stderr, "service logs: no log file at", p)
		os.Exit(1)
	}

	args2 := []string{}
	if *follow {
		args2 = append(args2, "-f")
	}
	args2 = append(args2, "-n", fmt.Sprintf("%d", *lines), p)
	cmd := exec.Command("tail", args2...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

func runCmd(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}

func alreadyBootstrapped(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "already") || strings.Contains(s, "bootstrap failed")
}
