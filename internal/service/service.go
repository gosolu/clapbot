package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	macOSLabel      = "com.gosolu.clapbot"
	linuxUnitName   = "clapbot.service"
)

// Manager handles installing, starting, stopping, and checking the OS background daemon.
type Manager struct {
	HomeDir string
	LogFile string
}

// Install generates and registers the launchd plist (macOS) or systemd user service (Linux).
func (m *Manager) Install() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	_ = os.MkdirAll(filepath.Dir(m.LogFile), 0755)

	switch runtime.GOOS {
	case "darwin":
		plistPath, err := m.plistPath()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(plistPath), 0755); err != nil {
			return "", err
		}
		content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
		<string>--home</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, macOSLabel, exe, m.HomeDir, m.LogFile, m.LogFile)
		if err := os.WriteFile(plistPath, []byte(content), 0644); err != nil {
			return "", err
		}
		return fmt.Sprintf("Installed launchd agent at %s", plistPath), nil

	case "linux":
		unitPath, err := m.systemdPath()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(unitPath), 0755); err != nil {
			return "", err
		}
		content := fmt.Sprintf(`[Unit]
Description=Clapbot Personal AI Agent Gateway
After=network.target

[Service]
Type=simple
ExecStart=%s serve --home %s
Restart=always
RestartSec=3
StandardOutput=append:%s
StandardError=append:%s

[Install]
WantedBy=default.target
`, exe, m.HomeDir, m.LogFile, m.LogFile)
		if err := os.WriteFile(unitPath, []byte(content), 0644); err != nil {
			return "", err
		}
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		_ = exec.Command("systemctl", "--user", "enable", linuxUnitName).Run()
		return fmt.Sprintf("Installed systemd user service at %s", unitPath), nil

	default:
		return "", fmt.Errorf("service installation is not supported on %s (use `clapbot serve` directly)", runtime.GOOS)
	}
}

// Start starts the background service.
func (m *Manager) Start() error {
	switch runtime.GOOS {
	case "darwin":
		plistPath, err := m.plistPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(plistPath); os.IsNotExist(err) {
			if _, err := m.Install(); err != nil {
				return err
			}
		}
		out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl load: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "linux":
		out, err := exec.Command("systemctl", "--user", "start", linuxUnitName).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl start: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

// Stop stops the background service.
func (m *Manager) Stop() error {
	switch runtime.GOOS {
	case "darwin":
		plistPath, err := m.plistPath()
		if err != nil {
			return err
		}
		out, err := exec.Command("launchctl", "unload", plistPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("launchctl unload: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "linux":
		out, err := exec.Command("systemctl", "--user", "stop", linuxUnitName).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl stop: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

// Uninstall stops and removes the service definition file.
func (m *Manager) Uninstall() error {
	_ = m.Stop()
	switch runtime.GOOS {
	case "darwin":
		p, err := m.plistPath()
		if err != nil {
			return err
		}
		_ = os.Remove(p)
		return nil
	case "linux":
		_ = exec.Command("systemctl", "--user", "disable", linuxUnitName).Run()
		p, err := m.systemdPath()
		if err != nil {
			return err
		}
		_ = os.Remove(p)
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
		return nil
	default:
		return fmt.Errorf("unsupported OS %s", runtime.GOOS)
	}
}

// Status returns a human-readable service status string.
func (m *Manager) Status() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "list", macOSLabel).CombinedOutput()
		if err != nil {
			return "stopped (not loaded in launchd)"
		}
		return "running (launchd):\n" + strings.TrimSpace(string(out))
	case "linux":
		out, _ := exec.Command("systemctl", "--user", "status", linuxUnitName, "--no-pager").CombinedOutput()
		return strings.TrimSpace(string(out))
	default:
		return "unsupported OS"
	}
}

func (m *Manager) plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", macOSLabel+".plist"), nil
}

func (m *Manager) systemdPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", linuxUnitName), nil
}
