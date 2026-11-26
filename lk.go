package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lokalkit/internal/core"
)

// The "lk" command in the LokalKit terminal talks to the running app over a
// local HTTP endpoint (127.0.0.1, random port). The port and a random token
// are written to etc\terminal\lk-api.txt, which lk.cmd reads; requests
// without the token are refused.

const (
	cY = "\x1b[38;2;250;204;21m"
	cM = "\x1b[38;2;148;156;171m"
	cG = "\x1b[38;2;52;197;106m"
	cR = "\x1b[38;2;240;99;99m"
	cB = "\x1b[1m"
	c0 = "\x1b[0m"
)

func (a *App) lkAPIFile() string { return filepath.Join(a.m.P.Etc, "terminal", "lk-api.txt") }

// startLK starts the endpoint for the lk command.
func (a *App) startLK() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		a.m.Logf("error", "lk command unavailable: %v", err)
		return
	}
	buf := make([]byte, 24)
	_, _ = rand.Read(buf)
	token := hex.EncodeToString(buf)
	port := ln.Addr().(*net.TCPAddr).Port
	_ = os.MkdirAll(filepath.Dir(a.lkAPIFile()), 0o755)
	if err := os.WriteFile(a.lkAPIFile(), []byte(fmt.Sprintf("%d %s\r\n", port, token)), 0o600); err != nil {
		ln.Close()
		return
	}
	a.lkListener = ln
	srv := &http.Server{Handler: a.lkHandler(token), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
}

func (a *App) stopLK() {
	if a.lkListener != nil {
		a.lkListener.Close()
		_ = os.Remove(a.lkAPIFile())
	}
}

func (a *App) lkHandler(token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/lk" ||
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-LK-Token")), []byte(token)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		out, err := a.runLK(r.FormValue("dir"), strings.Fields(r.FormValue("args")))
		if err != nil {
			out += cR + "✗ " + err.Error() + c0 + "\n"
		}
		_, _ = w.Write([]byte(out))
	})
}

const lkHelp = cY + "●" + c0 + " " + cB + "lk" + c0 + " controls LokalKit from the terminal\n\n" +
	"  lk status            services, and the project you are in\n" +
	"  lk open              open this project in the browser\n" +
	"  lk url               print this project's address\n" +
	"  lk log [lines]       last lines of the project's log (default 40)\n" +
	"  lk php [8.4|default] show or set this project's PHP\n" +
	"  lk db                open the database manager (DBeaver)\n" +
	"  lk mail              open Mailpit (caught emails)\n" +
	"  lk folder            open this folder in Explorer\n" +
	"  lk start|stop|restart [apache|mysql|mailpit]   all services when none given\n" +
	"  lk dev [stop]        start or stop an AdonisJS dev server\n" +
	"  lk repair            install missing packages (Composer / npm)\n" +
	"  lk reload            rescan projects and rewrite virtual hosts\n" +
	"  lk projects          list projects\n"

// runLK runs one lk command for the terminal folder dir.
func (a *App) runLK(dir string, args []string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	cmd := ""
	if len(args) > 0 {
		cmd = strings.ToLower(args[0])
		args = args[1:]
	}
	p, inProject := a.m.ProjectForDir(dir)
	needProject := func() error {
		if !inProject {
			return errors.New("not inside a LokalKit project. Go to one first (p <name>)")
		}
		return nil
	}
	var b strings.Builder
	say := func(format string, v ...any) { fmt.Fprintf(&b, format+"\n", v...) }

	switch cmd {
	case "", "help", "-h", "--help", "/?":
		return lkHelp, nil

	case "status":
		for _, s := range a.m.Services() {
			if !s.Installed {
				continue
			}
			state := cM + "stopped" + c0
			dot := cM + "○" + c0
			if s.Running {
				state, dot = cG+"running"+c0, cG+"●"+c0
			}
			say("  %s %-8s %s  %s", dot, s.Name, state, cM+s.Ports+c0)
		}
		if inProject {
			say("")
			say("  %s%s%s  %s", cY, p.Name, c0, projectURL(p))
			say("  %s%s%s", cM, a.projectDetails(p), c0)
		}
		return b.String(), nil

	case "projects":
		for _, pr := range a.m.Projects() {
			say("  %s%-20s%s %s  %s", cY, pr.Name, c0, projectURL(pr), cM+a.projectDetails(pr)+c0)
		}
		return b.String(), nil

	case "url":
		if err := needProject(); err != nil {
			return "", err
		}
		return projectURL(p) + "\n", nil

	case "open":
		url := a.m.LocalURL("/")
		if inProject {
			url = projectURL(p)
		}
		a.openURL(url)
		say("Opening %s", url)
		if !a.serviceRunning("apache") {
			say("%sApache is not running: lk start apache%s", cY, c0)
		}
		if inProject && p.Kind == "adonis" && !p.DevRunning {
			say("%sThe dev server is not running: lk dev%s", cY, c0)
		}
		return b.String(), nil

	case "log", "logs":
		n := 40
		if len(args) > 0 {
			if v, err := strconv.Atoi(args[0]); err == nil && v > 0 {
				n = min(v, 2000)
			}
		}
		file := filepath.Join(a.m.P.Logs, "lokalkit.log")
		if inProject {
			if file = a.m.ProjectLogFile(p); file == "" {
				return "No log yet for " + p.Name + ".\n", nil
			}
		}
		tail, err := core.TailLines(file, n)
		if err != nil {
			return "", err
		}
		return cM + "── " + file + c0 + "\n" + tail, nil

	case "php":
		if err := needProject(); err != nil {
			return "", err
		}
		if p.Kind == "adonis" {
			return "", errors.New(p.Name + " is an AdonisJS (Node.js) project; it does not use PHP")
		}
		if len(args) == 0 {
			say("%s uses PHP %s%s", p.Name, p.PHPVersion, map[bool]string{true: " (pinned to " + p.PHP + ")", false: " (default)"}[p.PHP != ""])
			var have []string
			seen := map[string]bool{}
			for _, in := range a.m.PHPInstalls() {
				if br := core.PHPBranch(in.Version); !seen[br] {
					seen[br] = true
					have = append(have, br)
				}
			}
			say("%sInstalled: %s. Change with: lk php <version> or lk php default%s", cM, strings.Join(have, ", "), c0)
			return b.String(), nil
		}
		want := args[0]
		if strings.EqualFold(want, "default") {
			want = ""
		}
		if err := a.m.SetProjectPHP(p.Name, want); err != nil {
			return "", err
		}
		np, _ := a.m.ProjectForDir(dir)
		say("%s✓%s %s now uses PHP %s", cG, c0, p.Name, np.PHPVersion)
		return b.String(), nil

	case "db", "database":
		url, err := a.m.OpenDatabase()
		if err != nil {
			return "", err
		}
		if url != "" {
			a.openURL(url)
		}
		return "Opening the database manager\n", nil

	case "mail", "mailpit":
		a.openURL(a.m.MailpitURL())
		return "Opening Mailpit\n", nil

	case "folder", "explorer":
		target := dir
		if inProject {
			target = p.Path
		}
		return "Opening " + target + "\n", core.OpenPath(target)

	case "start", "stop", "restart":
		ids, err := serviceIDs(args)
		if err != nil {
			return "", err
		}
		for _, id := range ids {
			var err error
			switch cmd {
			case "start":
				err = a.m.Start(id)
			case "stop":
				err = a.m.Stop(id)
			default:
				err = a.m.Restart(id)
			}
			if err != nil {
				say("%s✗ %s: %v%s", cR, id, err, c0)
			}
		}
		out, _ := a.runLK(dir, []string{"status"})
		return b.String() + out, nil

	case "dev":
		if err := needProject(); err != nil {
			return "", err
		}
		if p.Kind != "adonis" {
			return "", errors.New("lk dev is for AdonisJS projects (Laravel runs through Apache)")
		}
		if len(args) > 0 && strings.EqualFold(args[0], "stop") {
			if err := a.m.StopDev(p.Name); err != nil {
				return "", err
			}
			return "Dev server stopped\n", nil
		}
		if err := a.m.StartDev(p.Name); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s✓%s Dev server running: %s\n", cG, c0, projectURL(p)), nil

	case "repair":
		if err := needProject(); err != nil {
			return "", err
		}
		if err := a.m.RepairProject(p.Name); err != nil {
			return "", err
		}
		return "Repair started for " + p.Name + ". Progress shows on the project in LokalKit (and in lk log).\n", nil

	case "reload":
		if err := a.m.Reload(); err != nil {
			return "", err
		}
		return cG + "✓" + c0 + " Projects rescanned, virtual hosts rewritten\n", nil
	}
	return "", fmt.Errorf("unknown command %q. Type lk help", cmd)
}

func projectURL(p core.Project) string {
	if p.HTTPS != "" {
		return p.HTTPS
	}
	return p.URL
}

func (a *App) projectDetails(p core.Project) string {
	parts := []string{p.Kind}
	if p.Kind == "adonis" {
		state := "dev server stopped"
		if p.DevRunning {
			state = "dev server running"
		}
		parts = append(parts, state)
	} else if p.PHPVersion != "" {
		v := "PHP " + p.PHPVersion
		if p.PHP != "" {
			v += " (pinned)"
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, " · ")
}

func (a *App) serviceRunning(id string) bool {
	for _, s := range a.m.Services() {
		if s.ID == id {
			return s.Running
		}
	}
	return false
}

// serviceIDs maps "apache mysql" (or nothing / "all") to service IDs.
func serviceIDs(args []string) ([]string, error) {
	all := []string{"mysql", "mailpit", "apache"}
	if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "all")) {
		return all, nil
	}
	var ids []string
	for _, a := range args {
		switch strings.ToLower(a) {
		case "apache", "web", "httpd":
			ids = append(ids, "apache")
		case "mysql", "db", "mariadb":
			ids = append(ids, "mysql")
		case "mailpit", "mail":
			ids = append(ids, "mailpit")
		default:
			return nil, fmt.Errorf("unknown service %q (apache, mysql or mailpit)", a)
		}
	}
	return ids, nil
}
