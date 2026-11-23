package core

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"time"
)

// Proc supervises one long-running server process (httpd, mysqld, mailpit).
type Proc struct {
	name     string
	mu       sync.Mutex
	cmd      *exec.Cmd
	done     chan struct{}
	stopping bool
	onExit   func(name string, expected bool, err error)
}

func NewProc(name string, onExit func(string, bool, error)) *Proc {
	return &Proc{name: name, onExit: onExit}
}

func (p *Proc) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

// Start launches the process with stdout/stderr appended to logPath.
func (p *Proc) Start(bin string, args []string, dir string, env []string, logPath string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	fmt.Fprintf(f, "\n=== %s dimulakan %s ===\n", p.name, time.Now().Format("2006-01-02 15:04:05"))

	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	hideWindow(cmd)
	if err := cmd.Start(); err != nil {
		f.Close()
		return err
	}
	done := make(chan struct{})
	p.cmd, p.done, p.stopping = cmd, done, false

	go func() {
		werr := cmd.Wait()
		f.Close()
		p.mu.Lock()
		expected := p.stopping
		if p.cmd == cmd {
			p.cmd = nil
		}
		p.mu.Unlock()
		close(done)
		if p.onExit != nil {
			p.onExit(p.name, expected, werr)
		}
	}()
	return nil
}

// Exited is closed when the current process exits (nil if not running).
func (p *Proc) Exited() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done
}

// Stop tries graceful() first, then kills the whole process tree.
func (p *Proc) Stop(graceful func() error, timeout time.Duration) error {
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	if cmd == nil {
		p.mu.Unlock()
		return nil
	}
	p.stopping = true
	p.mu.Unlock()

	if graceful != nil && graceful() == nil {
		select {
		case <-done:
			return nil
		case <-time.After(timeout):
		}
	}
	killTree(cmd.Process.Pid)
	select {
	case <-done:
		return nil
	case <-time.After(8 * time.Second):
		return fmt.Errorf("%s tidak dapat dihentikan", p.name)
	}
}

// PortFree reports whether nothing is listening on the TCP port.
func PortFree(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err == nil {
		c.Close()
		return false
	}
	l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// waitPortFree waits for a port to be released (e.g. right after a restart).
func waitPortFree(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if PortFree(port) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var errExited = errors.New("proses berhenti semasa dimulakan")

// waitPort waits until the port accepts connections, the process exits, or timeout.
func waitPort(port int, exited <-chan struct{}, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		select {
		case <-exited:
			return errExited
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("port %d tidak dibuka selepas %s", port, timeout)
}
