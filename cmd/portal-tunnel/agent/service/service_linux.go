//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func Install(ctx context.Context, def Definition) error {
	unitPath, userMode, err := linuxUnitPath(def.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(systemdUnit(def)), 0o644); err != nil {
		return err
	}
	args := systemctlArgs(userMode, "daemon-reload")
	if err := exec.CommandContext(ctx, "systemctl", args...).Run(); err != nil {
		return err
	}
	args = systemctlArgs(userMode, "enable", def.Name+".service")
	return exec.CommandContext(ctx, "systemctl", args...).Run()
}

func Start(ctx context.Context, name string) error {
	_, userMode, err := linuxUnitPath(name)
	if err != nil {
		return err
	}
	args := systemctlArgs(userMode, "start", name+".service")
	return exec.CommandContext(ctx, "systemctl", args...).Run()
}

func StopDisable(ctx context.Context, name string) error {
	_, userMode, err := linuxUnitPath(name)
	if err != nil {
		return err
	}
	args := systemctlArgs(userMode, "disable", "--now", name+".service")
	return exec.CommandContext(ctx, "systemctl", args...).Run()
}

func Run(ctx context.Context, name string, run func(context.Context) error) error {
	return run(ctx)
}

func linuxUnitPath(name string) (string, bool, error) {
	if os.Geteuid() == 0 {
		return filepath.Join("/etc/systemd/system", name+".service"), false, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false, err
	}
	return filepath.Join(home, ".config", "systemd", "user", name+".service"), true, nil
}

func systemctlArgs(userMode bool, args ...string) []string {
	if userMode {
		return append([]string{"--user"}, args...)
	}
	return args
}

func systemdUnit(def Definition) string {
	parts := append([]string{def.Executable}, def.Args...)
	for i := range parts {
		parts[i] = shellQuote(parts[i])
	}
	return fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=%s
ExecStart=%s
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, def.Description, shellQuote(def.WorkingDir), strings.Join(parts, " "))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
