// Package api is the daemon's HTTP and WebSocket interface, used by the
// desktop app, the rla command and the agents' hooks.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/hewenyu/react-loop-agent/internal/agent"
	"github.com/hewenyu/react-loop-agent/internal/sched"
)

// State is everything the app shows.
type State struct {
	Repos  []sched.Repo `json:"repos"`
	Tasks  []sched.Task `json:"tasks"`
	Agents []agent.Info `json:"agents"`
}

// Server serves the API.
type Server struct {
	Agents   *agent.Manager
	Sched    *sched.Scheduler
	Token    string
	Version  string
	Shutdown func()
	Log      *slog.Logger

	mu      sync.Mutex
	subs    map[chan struct{}]struct{}
	pending bool
}

// State returns everything the app shows.
func (s *Server) State() State {
	saved := s.Sched.Saved()

	return State{Repos: saved.Repos, Tasks: saved.Tasks, Agents: s.Agents.List()}
}

// Changed tells every watcher that the state changed. Bursts of changes
// are sent as one, a moment later.
func (s *Server) Changed() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pending {
		return
	}

	s.pending = true

	time.AfterFunc(100*time.Millisecond, func() {
		s.mu.Lock()
		defer s.mu.Unlock()

		s.pending = false

		for ch := range s.subs {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	})
}

// Handler routes the API. Every route needs the token.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/ping", s.ping)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/shutdown", s.shutdown)

	mux.HandleFunc("POST /api/tasks", s.addTask)
	mux.HandleFunc("POST /api/tasks/{id}/{action}", s.taskAction)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.removeTask)
	mux.HandleFunc("POST /api/report", s.report)

	mux.HandleFunc("POST /api/repos", s.setRepo)
	mux.HandleFunc("DELETE /api/repos", s.removeRepo)

	mux.HandleFunc("POST /api/agents", s.spawn)
	mux.HandleFunc("POST /api/agents/{id}/{action}", s.agentAction)
	mux.HandleFunc("DELETE /api/agents/{id}", s.removeAgent)
	mux.HandleFunc("GET /api/agents/{id}/term", s.term)
	mux.HandleFunc("GET /api/agents/{id}/screen", s.screen)
	mux.HandleFunc("POST /api/hook", s.hook)

	return s.auth(mux)
}

// auth checks the token, from the Authorization header or, for
// WebSockets, which browsers open without headers, the token parameter.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)

			return
		}

		tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if tok == "" {
			tok = r.URL.Query().Get("token")
		}

		if subtle.ConstantTimeCompare([]byte(tok), []byte(s.Token)) != 1 {
			fail(w, http.StatusUnauthorized, errors.New("bad token"))

			return
		}

		next.ServeHTTP(w, r)
	})
}

func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	reply(w, map[string]string{"version": s.Version})
}

func (s *Server) state(w http.ResponseWriter, _ *http.Request) {
	reply(w, s.State())
}

func (s *Server) shutdown(w http.ResponseWriter, _ *http.Request) {
	reply(w, map[string]bool{"ok": true})

	if s.Shutdown != nil {
		go s.Shutdown()
	}
}

// events sends the whole state on connect and again after every change.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()

	ch := make(chan struct{}, 1)

	s.mu.Lock()
	if s.subs == nil {
		s.subs = map[chan struct{}]struct{}{}
	}
	s.subs[ch] = struct{}{}
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	ctx := c.CloseRead(r.Context())
	ping := time.NewTicker(20 * time.Second)

	defer ping.Stop()

	send := func() error {
		b, _ := json.Marshal(map[string]any{"type": "state", "state": s.State()})

		return c.Write(ctx, websocket.MessageText, b)
	}

	if send() != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			if send() != nil {
				return
			}
		case <-ping.C:
			if c.Ping(ctx) != nil {
				return
			}
		}
	}
}

func (s *Server) addTask(w http.ResponseWriter, r *http.Request) {
	var n sched.NewTask
	if !decode(w, r, &n) {
		return
	}

	t, err := s.Sched.Add(n)
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	reply(w, t)
}

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var err error

	switch r.PathValue("action") {
	case "cancel":
		err = s.Sched.Cancel(id)
	case "retry":
		err = s.Sched.Retry(id)
	case "finish":
		err = s.Sched.Finish(id)
	default:
		fail(w, http.StatusNotFound, fmt.Errorf("no action %q", r.PathValue("action")))

		return
	}

	done(w, err)
}

func (s *Server) removeTask(w http.ResponseWriter, r *http.Request) {
	done(w, s.Sched.Remove(r.PathValue("id")))
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Task    string `json:"task"`
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}

	if !decode(w, r, &b) {
		return
	}

	done(w, s.Sched.Report(b.Task, b.Status, b.Summary))
}

func (s *Server) setRepo(w http.ResponseWriter, r *http.Request) {
	var repo sched.Repo
	if !decode(w, r, &repo) {
		return
	}

	got, err := s.Sched.SetRepo(repo)
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	reply(w, got)
}

func (s *Server) removeRepo(w http.ResponseWriter, r *http.Request) {
	done(w, s.Sched.RemoveRepo(r.URL.Query().Get("path")))
}

func (s *Server) spawn(w http.ResponseWriter, r *http.Request) {
	var o agent.SpawnOptions
	if !decode(w, r, &o) {
		return
	}

	info, err := s.Agents.Spawn(o)
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	s.Changed()
	reply(w, info)
}

func (s *Server) agentAction(w http.ResponseWriter, r *http.Request) {
	a, err := s.Agents.Get(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, err)

		return
	}

	var b struct {
		Text  string `json:"text"`
		Force bool   `json:"force"`
	}

	if r.ContentLength > 0 && !decode(w, r, &b) {
		return
	}

	switch r.PathValue("action") {
	case "send":
		err = a.Send(b.Text, b.Force)
	case "keys":
		err = a.Write([]byte(b.Text))
	case "interrupt":
		err = a.Interrupt()
	case "kill":
		err = a.Kill()
	case "resume":
		_, err = s.Agents.Resume(a.Info().ID, b.Text)
	default:
		fail(w, http.StatusNotFound, fmt.Errorf("no action %q", r.PathValue("action")))

		return
	}

	s.Changed()
	done(w, err)
}

func (s *Server) removeAgent(w http.ResponseWriter, r *http.Request) {
	err := s.Agents.Remove(r.PathValue("id"))
	s.Changed()
	done(w, err)
}

// term streams an agent's terminal: first what it printed so far, then
// its output as it comes. Binary messages from the viewer are keys; text
// messages are JSON, e.g. {"type":"resize","cols":120,"rows":40}.
func (s *Server) term(w http.ResponseWriter, r *http.Request) {
	a, err := s.Agents.Get(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, err)

		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()

	c.SetReadLimit(1 << 20)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	replay, out, detach := a.Attach()
	defer detach()

	go s.termInput(ctx, cancel, c, a)

	if err := c.Write(ctx, websocket.MessageBinary, replay); err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-out:
			if !ok {
				if a.Info().Status.Live() {
					// Fell behind: the viewer reconnects and replays.
					_ = c.Close(4000, "resync")
				} else {
					_ = c.Close(websocket.StatusNormalClosure, "exited")
				}

				return
			}

			if err := c.Write(ctx, websocket.MessageBinary, p); err != nil {
				return
			}
		}
	}
}

func (s *Server) termInput(ctx context.Context, cancel func(), c *websocket.Conn, a *agent.Agent) {
	defer cancel()

	for {
		typ, p, err := c.Read(ctx)
		if err != nil {
			return
		}

		if typ == websocket.MessageBinary {
			_ = a.Write(p)

			continue
		}

		var m struct {
			Type string `json:"type"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}

		if json.Unmarshal(p, &m) == nil && m.Type == "resize" {
			_ = a.Resize(m.Rows, m.Cols)
		}
	}
}

// screen returns the terminal's recent output as plain text, for `rla read`.
func (s *Server) screen(w http.ResponseWriter, r *http.Request) {
	a, err := s.Agents.Get(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, err)

		return
	}

	replay, _, detach := a.Attach()
	detach()

	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(replay)
}

// hook takes a hook event from an agent's `rla hook`.
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	var h agent.Hook
	if err := json.Unmarshal(body, &h); err != nil {
		fail(w, http.StatusBadRequest, err)

		return
	}

	id := r.Header.Get("X-RLA-Agent")
	if err := s.Agents.Hook(id, h); err != nil {
		fail(w, http.StatusNotFound, err)

		return
	}

	if s.Log != nil {
		s.Log.Debug("hook", "agent", id, "event", h.Event, "tool", h.ToolName)
	}

	reply(w, map[string]bool{"ok": true})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, fmt.Errorf("bad request body: %w", err))

		return false
	}

	return true
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func done(w http.ResponseWriter, err error) {
	if err != nil {
		code := http.StatusConflict
		if errors.Is(err, agent.ErrNotFound) {
			code = http.StatusNotFound
		}

		fail(w, code, err)

		return
	}

	reply(w, map[string]bool{"ok": true})
}

func fail(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
