package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gosuda/portal-tunnel/v2/cmd/portal-tunnel/agent"
	"github.com/gosuda/portal-tunnel/v2/cmd/portal-tunnel/agent/service"
	"github.com/gosuda/portal-tunnel/v2/types"
	"github.com/gosuda/portal-tunnel/v2/utils"
)

func runAgentCommand(args []string) error {
	return utils.RunCommands(args, os.Stdout, os.Stderr, printAgentUsage, map[string]utils.CommandFunc{
		"run":     runAgentRunCommand,
		"status":  runAgentStatusCommand,
		"stop":    runAgentStopCommand,
		"reload":  runAgentReloadCommand,
		"restart": runAgentRestartCommand,
		"add":     runAgentRelayAddCommand,
		"remove":  runAgentRelayRemoveCommand,
		"help": utils.MakeHelpCommand(printAgentUsage, []utils.HelpTopic{
			{Name: "run", Usage: printAgentRunUsage},
			{Name: "status", Usage: printAgentStatusUsage},
			{Name: "stop", Usage: printAgentStopUsage},
		}),
	})
}

func runAgentRunCommand(args []string) error {
	var configPath string
	var serviceMode bool
	var foreground bool
	fs := utils.NewFlagSet("agent run", printAgentRunUsage)
	utils.StringFlag(fs, &configPath, "config", service.DefaultConfigPath(), "Portal agent TOML config path")
	utils.BoolFlag(fs, &serviceMode, "service", false, "Run the foreground service process")
	utils.BoolFlag(fs, &foreground, "foreground", false, "Run in the current process without installing the OS service")
	if err := utils.ParseFlagSet(fs, args, printAgentRunUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "agent run"); err != nil {
		printAgentRunUsage(os.Stderr)
		return err
	}

	cfg, err := agent.LoadConfig(configPath)
	if err != nil {
		return err
	}
	if serviceMode || foreground {
		ctx, stop := utils.SignalContext()
		defer stop()
		return service.Run(ctx, cfg.Agent.ServiceName, func(ctx context.Context) error {
			return agent.Run(ctx, cfg)
		})
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return err
	}
	configPath, err = filepath.Abs(strings.TrimSpace(configPath))
	if err != nil {
		return err
	}
	def := service.Definition{
		Name:        strings.TrimSpace(cfg.Agent.ServiceName),
		DisplayName: "Portal Agent",
		Description: "Manages Portal tunnel definitions and relay membership.",
		Executable:  executable,
		Args:        []string{"agent", "run", "--service", "--config", configPath},
		WorkingDir:  filepath.Dir(configPath),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := service.Install(ctx, def); err != nil {
		return fmt.Errorf("install portal agent service: %w", err)
	}
	if err := service.Start(ctx, cfg.Agent.ServiceName); err != nil {
		return fmt.Errorf("start portal agent service: %w", err)
	}
	status, err := waitAgentStatus(ctx, cfg.Agent.StateDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Portal agent running at %s with %d tunnel(s).\n", status.ControlAddr, len(status.Tunnels))
	return nil
}

func runAgentStatusCommand(args []string) error {
	var configPath string
	var stateDir string
	var jsonOutput bool
	fs := utils.NewFlagSet("agent status", printAgentStatusUsage)
	utils.StringFlag(fs, &configPath, "config", "", "Portal agent TOML config path")
	utils.StringFlag(fs, &stateDir, "state-dir", "", "Portal agent state directory")
	utils.BoolFlag(fs, &jsonOutput, "json", false, "Print raw JSON status")
	if err := utils.ParseFlagSet(fs, args, printAgentStatusUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "agent status"); err != nil {
		printAgentStatusUsage(os.Stderr)
		return err
	}

	status, err := agentStatusFromFlags(context.Background(), configPath, stateDir)
	if err != nil {
		return err
	}
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}
	printAgentStatus(os.Stdout, status)
	return nil
}

func runAgentStopCommand(args []string) error {
	var configPath string
	var stateDir string
	fs := utils.NewFlagSet("agent stop", printAgentStopUsage)
	utils.StringFlag(fs, &configPath, "config", "", "Portal agent TOML config path")
	utils.StringFlag(fs, &stateDir, "state-dir", "", "Portal agent state directory")
	if err := utils.ParseFlagSet(fs, args, printAgentStopUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "agent stop"); err != nil {
		printAgentStopUsage(os.Stderr)
		return err
	}

	cfg, resolvedStateDir, err := loadAgentCommandConfig(configPath, stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_ = agent.Shutdown(ctx, resolvedStateDir)
	if err := service.StopDisable(ctx, cfg.Agent.ServiceName); err != nil {
		return fmt.Errorf("stop portal agent service: %w", err)
	}
	fmt.Fprintln(os.Stdout, "Portal agent stopped.")
	return nil
}

func runAgentRestartCommand(args []string) error {
	configPath, stateDir, tunnelID, err := parseAgentTunnelCommand("agent restart", args, printAgentUsage)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withAgentControl(configPath, stateDir, func(ctx context.Context, stateDir string) error {
		return agent.RestartTunnel(ctx, stateDir, tunnelID)
	})
}

func runAgentReloadCommand(args []string) error {
	var configPath, stateDir string
	fs := utils.NewFlagSet("agent reload", printAgentUsage)
	utils.StringFlag(fs, &configPath, "config", "", "Portal agent TOML config path")
	utils.StringFlag(fs, &stateDir, "state-dir", "", "Portal agent state directory")
	if err := utils.ParseFlagSet(fs, args, printAgentUsage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := utils.RequireNoArgs(fs.Args(), "agent reload"); err != nil {
		return err
	}
	return withAgentControl(configPath, stateDir, func(ctx context.Context, stateDir string) error {
		return agent.Reload(ctx, stateDir)
	})
}

func runAgentRelayAddCommand(args []string) error {
	configPath, stateDir, tunnelID, relayURL, err := parseAgentRelayCommand("agent relay-add", args, printAgentUsage)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withAgentControl(configPath, stateDir, func(ctx context.Context, stateDir string) error {
		return agent.AddRelay(ctx, stateDir, tunnelID, relayURL)
	})
}

func runAgentRelayRemoveCommand(args []string) error {
	configPath, stateDir, tunnelID, relayURL, err := parseAgentRelayCommand("agent relay-remove", args, printAgentUsage)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return withAgentControl(configPath, stateDir, func(ctx context.Context, stateDir string) error {
		return agent.RemoveRelay(ctx, stateDir, tunnelID, relayURL)
	})
}

func parseAgentTunnelCommand(name string, args []string, usage func(io.Writer)) (string, string, string, error) {
	var configPath, stateDir string
	fs := utils.NewFlagSet(name, usage)
	utils.StringFlag(fs, &configPath, "config", "", "Portal agent TOML config path")
	utils.StringFlag(fs, &stateDir, "state-dir", "", "Portal agent state directory")
	if err := utils.ParseFlagSet(fs, args, usage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", "", "", flag.ErrHelp
		}
		return "", "", "", err
	}
	if len(fs.Args()) != 1 {
		return "", "", "", errors.New(name + " requires tunnel id")
	}
	return configPath, stateDir, fs.Args()[0], nil
}

func parseAgentRelayCommand(name string, args []string, usage func(io.Writer)) (string, string, string, string, error) {
	var configPath, stateDir string
	fs := utils.NewFlagSet(name, usage)
	utils.StringFlag(fs, &configPath, "config", "", "Portal agent TOML config path")
	utils.StringFlag(fs, &stateDir, "state-dir", "", "Portal agent state directory")
	if err := utils.ParseFlagSet(fs, args, usage); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return "", "", "", "", flag.ErrHelp
		}
		return "", "", "", "", err
	}
	if len(fs.Args()) != 2 {
		return "", "", "", "", errors.New(name + " requires tunnel id and relay url")
	}
	return configPath, stateDir, fs.Args()[0], fs.Args()[1], nil
}

func withAgentControl(configPath, stateDir string, run func(context.Context, string) error) error {
	_, resolvedStateDir, err := loadAgentCommandConfig(configPath, stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx, resolvedStateDir); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "Accepted.")
	return nil
}

func agentStatusFromFlags(ctx context.Context, configPath, stateDir string) (types.AgentStatusResponse, error) {
	_, resolvedStateDir, err := loadAgentCommandConfig(configPath, stateDir)
	if err != nil {
		return types.AgentStatusResponse{}, err
	}
	return agent.Status(ctx, resolvedStateDir)
}

func waitAgentStatus(ctx context.Context, stateDir string) (types.AgentStatusResponse, error) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		status, err := agentStatusFromFlags(ctx, "", stateDir)
		if err == nil {
			return status, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return types.AgentStatusResponse{}, fmt.Errorf("wait for portal agent status: %w", lastErr)
		case <-ticker.C:
		}
	}
}

func loadAgentCommandConfig(configPath, stateDir string) (agent.Config, string, error) {
	if stateDir != "" && configPath == "" {
		cfg := agent.Config{Agent: agent.AgentConfig{StateDir: stateDir, ServiceName: agent.DefaultServiceName}}
		return cfg, stateDir, nil
	}
	if configPath != "" {
		cfg, err := agent.LoadConfig(configPath)
		if err != nil {
			return agent.Config{}, "", err
		}
		if stateDir != "" {
			cfg.Agent.StateDir = stateDir
		}
		return cfg, cfg.Agent.StateDir, nil
	}
	defaultStateDir := service.DefaultDataDir()
	cfg := agent.Config{Agent: agent.AgentConfig{StateDir: defaultStateDir, ServiceName: agent.DefaultServiceName}}
	return cfg, defaultStateDir, nil
}

func printAgentStatus(w io.Writer, status types.AgentStatusResponse) {
	fmt.Fprintf(w, "Portal agent %s at %s\n", status.ReleaseVersion, status.ControlAddr)
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "TUNNEL\tSTATE\tPUBLIC URLS\tLAST ERROR")
	for _, tunnel := range status.Tunnels {
		publicURLs := "-"
		if len(tunnel.PublicURLs) > 0 {
			publicURLs = strings.Join(tunnel.PublicURLs, ",")
		}
		lastError := tunnel.LastError
		if lastError == "" {
			lastError = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", tunnel.ID, tunnel.State, publicURLs, lastError)
	}
	_ = table.Flush()
}

func printAgentUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{
			"portal agent run [flags]",
			"portal agent status [flags]",
			"portal agent stop [flags]",
			"portal agent reload [flags]",
			"portal agent restart [flags] <tunnel-id>",
			"portal agent relay-add [flags] <tunnel-id> <relay-url>",
			"portal agent relay-remove [flags] <tunnel-id> <relay-url>",
		},
		[]string{
			"portal agent run",
			"portal agent run --config config.toml --foreground",
			"portal agent status",
			"portal agent stop",
			"portal agent reload",
			"portal agent relay-add web https://portal.example.com",
		},
	)
}

func printAgentRunUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{"portal agent run [flags]"},
		[]string{
			"portal agent run",
			"portal agent run --config config.toml --foreground",
		},
	)
}

func printAgentStatusUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{"portal agent status [flags]"},
		[]string{
			"portal agent status",
			"portal agent status --json",
			"portal agent status --config config.toml",
		},
	)
}

func printAgentStopUsage(w io.Writer) {
	utils.WriteCommandUsage(w,
		[]string{"portal agent stop [flags]"},
		[]string{
			"portal agent stop",
			"portal agent stop --config config.toml",
		},
	)
}
