//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const systemdUnitName = "aeolus.service"

func printServiceUsage() {
	fmt.Print(`aeolus service — manage the aeolus daemon via systemd (user scope).

Subcommands:
  install [--force] [--no-start]
                         Write the systemd user unit (~/.config/systemd/user/),
                         create a starter config if none exists, then
                         start the daemon. Pass --no-start to skip the
                         auto-start (useful for CI/scripts).
  start                  Start the service via systemctl --user.
  stop                   Stop the service.
  restart                Restart in place.
  status                 Report whether the service is running.
  uninstall              Stop + disable + remove the unit.
  logs [-f]              Print recent daemon stderr (journalctl --user).

The unit is installed in user scope, so it runs while you're logged in.
To keep it running across logouts, enable lingering for your user:
    sudo loginctl enable-linger $USER
`)
}

func unitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", systemdUnitName)
}

// unitContent renders the systemd user unit. We inherit the user's
// interactive shell PATH at install time so nvm / pyenv / asdf /
// volta-managed binaries (npx, uvx, pipx) resolve correctly when
// systemd starts the daemon.
func unitContent() string {
	exe := findSelfPath()
	cfg := defaultConfigPath()
	path := resolveUserShellPath()

	return fmt.Sprintf(`[Unit]
Description=Aeolus — local MCP gateway
After=network.target

[Service]
Type=simple
ExecStart=%s --config %s
Restart=on-failure
RestartSec=1
StandardOutput=journal
StandardError=journal
Environment=PATH=%s

[Install]
WantedBy=default.target
`, exe, cfg, path)
}

// resolveUserShellPath captures the user's interactive shell PATH so
// the systemd unit can spawn commands that need PATH entries from
// .bashrc / .zshrc (nvm, pyenv, asdf, volta). systemd's default user
// PATH is /usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin which
// misses most version-managed tools.
func resolveUserShellPath() string {
	const fallback = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-ilc", `printf '%s' "$PATH"`)
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"service install: couldn't read your shell's PATH (%v); falling back to a minimal default.\n", err)
		return fallback
	}
	p := strings.TrimSpace(string(out))
	if p == "" || strings.ContainsAny(p, "\n\r\"") {
		return fallback
	}
	return p
}

func installService(args []string) {
	fs := flag.NewFlagSet("service install", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite an existing unit")
	noStart := fs.Bool("no-start", false, "skip auto-starting the daemon after install")
	_ = fs.Parse(args)

	p := unitPath()
	if _, err := os.Stat(p); err == nil && !*force {
		fmt.Fprintf(os.Stderr, "%s already exists. Use --force to overwrite.\n", p)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "service install: couldn't create unit directory:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(p, []byte(unitContent()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "service install: couldn't write unit:", err)
		os.Exit(1)
	}

	// Auto-create a starter config if none exists, same as macOS.
	cfg := defaultConfigPath()
	if _, err := os.Stat(cfg); err != nil && os.IsNotExist(err) {
		_ = os.MkdirAll(filepath.Dir(cfg), 0o755)
		if err := os.WriteFile(cfg, []byte(starterConfig), 0o600); err == nil {
			fmt.Printf("Created %s with a starter template.\n", cfg)
		}
	}

	// daemon-reload picks up the new unit.
	if _, err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		fmt.Fprintln(os.Stderr, "service install: systemctl daemon-reload failed:", err)
		os.Exit(1)
	}

	fmt.Printf("Installed %s\n", p)

	if *noStart {
		fmt.Println()
		fmt.Println("Skipping auto-start (--no-start). To start later:")
		fmt.Println("  aeolus service start")
		fmt.Println()
		fmt.Println("To keep the daemon running when you're logged out:")
		fmt.Println("  sudo loginctl enable-linger $USER")
		return
	}

	// Most users want install + start in one shot.
	startService()
	fmt.Println()
	fmt.Println("Open the dashboard:")
	fmt.Println("  aeolus open")
	fmt.Println()
	fmt.Println("To keep the daemon running when you're logged out:")
	fmt.Println("  sudo loginctl enable-linger $USER")
}

func startService() {
	if _, err := os.Stat(unitPath()); err != nil {
		fmt.Fprintln(os.Stderr, "service: unit not found — run `aeolus service install` first.")
		os.Exit(1)
	}
	// `systemctl --user enable --now` enables on-login + starts immediately.
	out, err := runCmd("systemctl", "--user", "enable", "--now", systemdUnitName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "service start failed:", err)
		fmt.Fprintln(os.Stderr, strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	fmt.Println("Started.")
}

func stopService(printOnSuccess bool) {
	out, err := runCmd("systemctl", "--user", "stop", systemdUnitName)
	if err != nil {
		s := strings.ToLower(string(out))
		if strings.Contains(s, "not loaded") || strings.Contains(s, "not active") {
			if printOnSuccess {
				fmt.Println("Not running.")
			}
			return
		}
		fmt.Fprintln(os.Stderr, "service stop:", err)
		fmt.Fprintln(os.Stderr, strings.TrimSpace(string(out)))
		return
	}
	if printOnSuccess {
		fmt.Println("Stopped.")
	}
}

func restartService() {
	out, err := runCmd("systemctl", "--user", "restart", systemdUnitName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "service restart:", err)
		fmt.Fprintln(os.Stderr, strings.TrimSpace(string(out)))
		os.Exit(1)
	}
	fmt.Println("Restarted.")
}

func statusService() {
	out, err := runCmd("systemctl", "--user", "is-active", systemdUnitName)
	state := strings.TrimSpace(string(out))
	if err != nil && state == "" {
		fmt.Println("not loaded")
		os.Exit(1)
	}
	// systemctl is-active prints: active | inactive | failed | activating | ...
	fmt.Println(state)
	if state != "active" {
		os.Exit(1)
	}
}

func uninstallService() {
	stopService(false)
	if _, err := runCmd("systemctl", "--user", "disable", systemdUnitName); err != nil {
		// Disable can fail if unit isn't enabled — non-fatal.
		_ = err
	}
	if err := os.Remove(unitPath()); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "service uninstall:", err)
		os.Exit(1)
	}
	_, _ = runCmd("systemctl", "--user", "daemon-reload")
	fmt.Println("Uninstalled.")
}

func logsService(args []string) {
	fs := flag.NewFlagSet("service logs", flag.ExitOnError)
	follow := fs.Bool("f", false, "follow the journal (like tail -f)")
	lines := fs.Int("n", 50, "number of trailing lines to print")
	_ = fs.Parse(args)

	jArgs := []string{"--user", "-u", systemdUnitName, "-n", fmt.Sprintf("%d", *lines)}
	if *follow {
		jArgs = append(jArgs, "-f")
	}
	cmd := exec.Command("journalctl", jArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// runCmd runs a command and returns its combined output + error.
// Shared by every service subcommand so we don't repeat the boilerplate.
func runCmd(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}
