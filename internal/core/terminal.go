package core

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The LokalKit terminal: a cmd.exe with LokalKit's tools on PATH, a small
// banner, a coloured prompt and an "art" alias. On Windows Terminal it also
// gets a "LokalKit" profile with the LokalKit colours and icon.

const (
	terminalProfileName = "LokalKit"
	terminalProfileGUID = "{6b0c5a2e-3f4d-4c8e-9a17-4c6f6b17a0e1}"
)

// TerminalIcon is the icon shown on the Windows Terminal tab (set by the app).
var TerminalIcon []byte

func (m *Manager) terminalDir() string { return filepath.Join(m.P.Etc, "terminal") }

const esc = "\x1b"

// BuildTerminalInit writes the batch file every LokalKit terminal runs first.
func BuildTerminalInit(pathDirs []string, phpVersion, nodeVersion string, mysqlPort int) string {
	yellow := esc + "[38;2;250;204;21m"
	grey := esc + "[38;2;148;156;171m"
	bold := esc + "[1m"
	reset := esc + "[0m"
	var b strings.Builder
	w := func(s string) { b.WriteString(s + "\r\n") }
	w("@echo off")
	w("chcp 65001 >nul")
	if len(pathDirs) > 0 {
		w(`set "PATH=` + strings.Join(pathDirs, ";") + `;%PATH%"`)
	}
	w("title LokalKit Terminal")
	w(`doskey art=php artisan $*`)
	w("echo.")
	w("echo  " + yellow + "●" + reset + " " + bold + "LokalKit" + reset + " Terminal " + grey + "by Codeslab" + reset)
	var info []string
	if phpVersion != "" {
		info = append(info, "PHP "+phpVersion)
	}
	if nodeVersion != "" {
		info = append(info, "Node "+nodeVersion)
	}
	info = append(info, fmt.Sprintf("MySQL 127.0.0.1:%d", mysqlPort))
	w("echo  " + grey + strings.Join(info, "  ·  ") + reset)
	w("echo  " + grey + "Tip: " + reset + "art = php artisan  " + grey + "(e.g. art migrate)" + reset)
	w("echo.")
	// $E = escape, $P = current folder, $_ = new line.
	w("prompt $E[38;2;250;204;21m●$E[0m $E[38;2;148;156;171m$P$E[0m$_$E[38;2;250;204;21m❯$E[0m ")
	return b.String()
}

// TerminalScheme is the LokalKit colour scheme for Windows Terminal.
var TerminalScheme = map[string]string{
	"name":                "LokalKit",
	"background":          "#15171C",
	"foreground":          "#E6E7EA",
	"cursorColor":         "#FACC15",
	"selectionBackground": "#3A3D45",
	"black":               "#15171C",
	"red":                 "#F06363",
	"green":               "#34C56A",
	"yellow":              "#FACC15",
	"blue":                "#6CB6E0",
	"purple":              "#B392F0",
	"cyan":                "#2DD4BF",
	"white":               "#E6E7EA",
	"brightBlack":         "#5A5F6B",
	"brightRed":           "#FF8A8A",
	"brightGreen":         "#5BE38C",
	"brightYellow":        "#FDE047",
	"brightBlue":          "#93C5FD",
	"brightPurple":        "#D2B8FF",
	"brightCyan":          "#5EEAD4",
	"brightWhite":         "#FFFFFF",
}

// BuildTerminalFragment is a Windows Terminal "fragment" adding the LokalKit
// profile and colour scheme (Windows Terminal reads these automatically).
func BuildTerminalFragment(initPath, iconPath, startDir string) ([]byte, error) {
	profile := map[string]any{
		"guid":                     terminalProfileGUID,
		"name":                     terminalProfileName,
		"commandline":              `cmd.exe /K "` + initPath + `"`,
		"startingDirectory":        startDir,
		"colorScheme":              "LokalKit",
		"tabTitle":                 "LokalKit",
		"suppressApplicationTitle": true,
		"tabColor":                 "#15171C",
		"cursorShape":              "bar",
		"padding":                  "12",
		"font":                     map[string]any{"face": "Cascadia Mono", "size": 11},
	}
	if iconPath != "" {
		profile["icon"] = iconPath
	}
	return json.MarshalIndent(map[string]any{
		"profiles": []any{profile},
		"schemes":  []any{TerminalScheme},
	}, "", "  ")
}

// prepareTerminal writes the init script (and on Windows the Windows
// Terminal fragment + icon) and returns the init script path.
func (m *Manager) prepareTerminal() (string, error) {
	dir := m.terminalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	phpV := ""
	if p, ok := m.CurrentPHP(); ok {
		phpV = p.Version
		_ = m.EnsurePHPIni(p) // also repairs extension_dir / duplicate extensions
	}
	nodeV := ""
	if node := m.nodeBin(); node != "" {
		nodeV = probeVersion(node, "-v")
	}
	initPath := filepath.Join(dir, "lokalkit-init.cmd")
	if err := os.WriteFile(initPath, []byte(BuildTerminalInit(m.toolDirs(), phpV, nodeV, m.config().MySQLPort)), 0o644); err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		iconPath := ""
		if len(TerminalIcon) > 0 {
			iconPath = filepath.Join(dir, "lokalkit.ico")
			_ = os.WriteFile(iconPath, TerminalIcon, 0o644)
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			frag, err := BuildTerminalFragment(initPath, iconPath, m.P.WWW)
			if err == nil {
				fdir := filepath.Join(local, "Microsoft", "Windows Terminal", "Fragments", "LokalKit")
				if os.MkdirAll(fdir, 0o755) == nil {
					_ = os.WriteFile(filepath.Join(fdir, "lokalkit.json"), frag, 0o644)
				}
			}
		}
	}
	return initPath, nil
}

// windowsTerminal returns wt.exe when Windows Terminal is installed.
func windowsTerminal() string {
	if p, err := exec.LookPath("wt.exe"); err == nil {
		return p
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		p := filepath.Join(local, "Microsoft", "WindowsApps", "wt.exe")
		if exists(p) {
			return p
		}
	}
	return ""
}
