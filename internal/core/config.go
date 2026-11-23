package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
)

// Config is saved as lokalkit.json in the root folder.
type Config struct {
	ApachePort       int    `json:"apachePort"`
	MySQLPort        int    `json:"mysqlPort"`
	MailpitSMTPPort  int    `json:"mailpitSmtpPort"`
	MailpitHTTPPort  int    `json:"mailpitHttpPort"`
	PHPVersion       string `json:"phpVersion"` // folder name inside bin/php; empty = newest
	TLD              string `json:"tld"`
	AutoVhost        bool   `json:"autoVhost"`
	StartAllOnLaunch bool   `json:"startAllOnLaunch"`
	StopAllOnExit    bool   `json:"stopAllOnExit"`
	MinimizeToTray   bool   `json:"minimizeToTray"` // close button hides to the system tray
}

func DefaultConfig() Config {
	return Config{
		ApachePort:       80,
		MySQLPort:        3306,
		MailpitSMTPPort:  1025,
		MailpitHTTPPort:  8025,
		TLD:              "test",
		AutoVhost:        true,
		StartAllOnLaunch: false,
		StopAllOnExit:    true,
		MinimizeToTray:   true,
	}
}

var tldRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,20}$`)

// Real TLDs that would break normal browsing, plus ones browsers force to HTTPS.
var blockedTLDs = map[string]string{
	"com": "domain sebenar", "net": "domain sebenar", "org": "domain sebenar",
	"my": "domain sebenar", "io": "domain sebenar",
	"dev": "pelayar paksa HTTPS (HSTS)", "app": "pelayar paksa HTTPS (HSTS)",
	"localhost": "dikhaskan",
}

func (c Config) Validate() error {
	ports := map[string]int{
		"Apache":       c.ApachePort,
		"MySQL":        c.MySQLPort,
		"Mailpit SMTP": c.MailpitSMTPPort,
		"Mailpit Web":  c.MailpitHTTPPort,
	}
	seen := map[int]string{}
	for name, p := range ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("port %s tidak sah: %d", name, p)
		}
		if other, ok := seen[p]; ok {
			return fmt.Errorf("port %d digunakan oleh %s dan %s", p, other, name)
		}
		seen[p] = name
	}
	if !tldRe.MatchString(c.TLD) {
		return errors.New("TLD mesti huruf kecil, contoh: test")
	}
	if why, bad := blockedTLDs[c.TLD]; bad {
		return fmt.Errorf("TLD .%s tidak boleh digunakan (%s). Cadangan: .test", c.TLD, why)
	}
	return nil
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, SaveConfig(path, cfg)
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return DefaultConfig(), fmt.Errorf("lokalkit.json rosak: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return DefaultConfig(), fmt.Errorf("lokalkit.json: %w", err)
	}
	return cfg, nil
}

func SaveConfig(path string, cfg Config) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
