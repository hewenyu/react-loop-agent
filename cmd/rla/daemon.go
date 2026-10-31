package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/agent"
	"github.com/hewenyu/react-loop-agent/internal/api"
	"github.com/hewenyu/react-loop-agent/internal/sched"
	"github.com/hewenyu/react-loop-agent/internal/store"
)

// cmdDaemon runs the daemon: it owns the agents' terminals, schedules the
// tasks and serves the app, until it is stopped.
func cmdDaemon(args []string) error {
	home, err := store.Home()
	if err != nil {
		return err
	}

	cfg, err := store.LoadConfig(home)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:0", "address to listen on (loopback only)")
	program := fs.String("agent", cfg.Agent, "agent command (config: agent)")
	claudeBot := fs.String("claude-bot", cfg.ClaudeBot, "claude-bot command, for repos that take its tasks (config: claude_bot)")
	agentArgs := fs.String("agent-args", strings.Join(cfg.AgentArgs, " "),
		"extra arguments for every agent, space separated, e.g. \"--permission-mode acceptEdits\" (config: agent_args)")
	verbose := fs.Bool("v", false, "log every hook")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}

	unlock, err := lockDaemon(home)
	if err != nil {
		return err
	}
	defer unlock()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	self, err := os.Executable()
	if err != nil {
		return err
	}

	self, _ = filepath.EvalSymlinks(self)

	saved, err := store.Load(home)
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}

	if host, _, _ := net.SplitHostPort(ln.Addr().String()); !net.ParseIP(host).IsLoopback() {
		_ = ln.Close()

		return fmt.Errorf("refusing to listen on %s: the API drives agents; use a loopback address", host)
	}

	token := newToken()

	srv := &api.Server{Token: token, Version: Version, Log: log}

	// Save soon after any change, not on every one.
	var (
		saveMu   sync.Mutex
		saveSoon *time.Timer
		stopping atomic.Bool
	)

	var s *sched.Scheduler

	var m *agent.Manager

	save := func() {
		saveMu.Lock()
		defer saveMu.Unlock()

		saved := s.Saved()
		if err := store.Save(home, store.State{Repos: saved.Repos, Tasks: saved.Tasks, Agents: m.List()}); err != nil {
			log.Error("save state", "err", err)
		}
	}

	changed := func() {
		srv.Changed()

		if stopping.Load() {
			// Agents ending with the daemon stay saved as running, to
			// come back lost and resume.
			return
		}

		saveMu.Lock()
		defer saveMu.Unlock()

		if saveSoon == nil {
			saveSoon = time.AfterFunc(500*time.Millisecond, func() {
				saveMu.Lock()
				saveSoon = nil
				saveMu.Unlock()

				if !stopping.Load() {
					save()
				}
			})
		}
	}

	m = agent.NewManager(agent.Config{
		Program:     *program,
		Args:        strings.Fields(*agentArgs),
		HookCommand: shellQuote(self) + " hook",
		// Agents report with `rla report`, found on the PATH below; it
		// only tells the daemon about their own task.
		Allow: []string{"Bash(rla report:*)"},
		Env: map[string]string{
			"RLA_ADDR":  ln.Addr().String(),
			"RLA_TOKEN": token,
			"PATH":      filepath.Dir(self),
		},
	}, func(agent.Info) {
		changed()

		if s != nil {
			s.Kick()
		}
	})
	m.Restore(saved.Agents)

	s = sched.New(sched.Options{
		Agents:   m,
		Bot:      sched.ClaudeBot{Program: *claudeBot},
		Report:   "rla report",
		OnChange: changed,
	}, sched.Saved{Repos: saved.Repos, Tasks: saved.Tasks})

	srv.Agents, srv.Sched = m, s

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv.Shutdown = stop

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}

	go func() {
		if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve", "err", err)
			stop()
		}
	}()

	if err := store.WriteDaemon(home, store.Daemon{
		Addr: ln.Addr().String(), Token: token, PID: os.Getpid(), Version: Version, StartedAt: time.Now(),
	}); err != nil {
		return err
	}
	defer store.RemoveDaemon(home)

	log.Info("rla daemon listening", "addr", ln.Addr().String(), "home", home, "agent", *program, "agent_args", *agentArgs)

	go s.Run(ctx)

	<-ctx.Done()
	log.Info("rla daemon stopping")

	save()
	stopping.Store(true)

	sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_ = httpSrv.Shutdown(sctx)

	m.KillAll()

	return nil
}

// lockDaemon makes sure one daemon runs per home.
func lockDaemon(home string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(home, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()

		return nil, errors.New("another rla daemon is running for " + home)
	}

	return func() { _ = f.Close() }, nil
}

func newToken() string {
	var b [24]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

// shellQuote quotes s for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
