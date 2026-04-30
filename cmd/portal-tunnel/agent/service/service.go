package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const defaultConfigFilename = "config.toml"

type Definition struct {
	Name        string
	DisplayName string
	Description string
	Executable  string
	Args        []string
	WorkingDir  string
}

func DefaultConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(windowsProgramDataDir(), "Portal Tunnel", "Agent", defaultConfigFilename)
	case "darwin":
		return filepath.Join(string(filepath.Separator), "Library", "Application Support", "Portal Tunnel", "Agent", defaultConfigFilename)
	default:
		return filepath.Join(string(filepath.Separator), "etc", "portal-tunnel", "agent", defaultConfigFilename)
	}
}

func DefaultDataDir() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(windowsProgramDataDir(), "Portal Tunnel", "Agent")
	case "darwin":
		return filepath.Join(string(filepath.Separator), "Library", "Application Support", "Portal Tunnel", "Agent")
	default:
		return filepath.Join(string(filepath.Separator), "var", "lib", "portal-tunnel", "agent")
	}
}

func windowsProgramDataDir() string {
	programData := strings.TrimSpace(os.Getenv("ProgramData"))
	if programData == "" {
		return `C:\ProgramData`
	}
	return programData
}
