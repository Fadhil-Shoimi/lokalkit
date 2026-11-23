package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Mailpit struct {
	m    *Manager
	proc *Proc
}

func (s *Mailpit) ID() string { return "mailpit" }

func (s *Mailpit) bin() string {
	return findUnder(filepath.Join(s.m.P.Bin, "mailpit"), exeName("mailpit"))
}

func (s *Mailpit) Info() ServiceInfo {
	bin := s.bin()
	cfg := s.m.config()
	return ServiceInfo{
		ID: "mailpit", Name: "Mailpit", Installed: bin != "",
		Version: probeVersion(bin, "version"), Running: s.proc.Running(),
		Ports: fmt.Sprintf("%d/%d", cfg.MailpitSMTPPort, cfg.MailpitHTTPPort),
		Hint:  `bin\mailpit\mailpit.exe`,
	}
}

func (s *Mailpit) Running() bool { return s.proc.Running() }

func (s *Mailpit) Start() error {
	if s.proc.Running() {
		return nil
	}
	bin := s.bin()
	if bin == "" {
		return errors.New("Mailpit tidak dijumpai. Letak mailpit.exe dalam folder bin\\mailpit")
	}
	cfg := s.m.config()
	for _, p := range []int{cfg.MailpitSMTPPort, cfg.MailpitHTTPPort} {
		if !PortFree(p) {
			return fmt.Errorf("port %d sedang digunakan", p)
		}
	}
	dbDir := filepath.Join(s.m.P.Data, "mailpit")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return err
	}
	args := []string{
		"--smtp", fmt.Sprintf("127.0.0.1:%d", cfg.MailpitSMTPPort),
		"--listen", fmt.Sprintf("127.0.0.1:%d", cfg.MailpitHTTPPort),
		"--database", filepath.Join(dbDir, "mailpit.db"),
	}
	if err := s.proc.Start(bin, args, filepath.Dir(bin), envWithPath(nil),
		filepath.Join(s.m.P.Logs, "mailpit.log")); err != nil {
		return err
	}
	if err := waitPort(cfg.MailpitHTTPPort, s.proc.Exited(), 15*time.Second); err != nil {
		_ = s.proc.Stop(nil, 0)
		return fmt.Errorf("Mailpit gagal dimulakan (%v). Lihat logs\\mailpit.log", err)
	}
	return nil
}

func (s *Mailpit) Stop() error { return s.proc.Stop(nil, 0) }
