package agent

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNotFound reports an unknown agent.
var ErrNotFound = errors.New("no such agent")

// hookEvents are the Claude Code hooks an agent reports to the daemon.
var hookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure",
	"PermissionRequest", "Notification", "Stop", "SubagentStop", "SessionEnd",
}

// sessionVars are what a Claude Code session sets for the programs it runs.
// An agent started by a daemon that was itself started from Claude Code
// must not inherit them: it would take itself for a child session and not
// save its conversation, so it could never be resumed.
var sessionVars = map[string]bool{
	"CLAUDECODE":                    true,
	"CLAUDE_CODE_ENTRYPOINT":        true,
	"CLAUDE_CODE_SESSION_ID":        true,
	"CLAUDE_CODE_CHILD_SESSION":     true,
	"CLAUDE_CODE_SESSION_ATTENDED":  true,
	"CLAUDE_CODE_EXECPATH":          true,
	"CLAUDE_CODE_SSE_PORT":          true,
	"CLAUDE_CODE_PARENT_SESSION_ID": true,
}

// Config says how to start agents.
type Config struct {
	// Program is the agent command, normally "claude".
	Program string
	// Args go before everything the manager adds, e.g. a permission mode.
	Args []string
	// HookCommand is the shell command Claude runs for every hook; it reads
	// the event on stdin and reports it to the daemon.
	HookCommand string
	// Allow are permission rules every agent gets, e.g. reporting to the
	// daemon without asking.
	Allow []string
	// Env is added to every agent's environment, e.g. how to reach the
	// daemon. Its PATH entry, if any, goes before the inherited PATH.
	Env map[string]string
	// RingSize is how much output each terminal keeps for replay.
	RingSize int
}

// SpawnOptions describe a new agent.
type SpawnOptions struct {
	Name   string
	Cwd    string
	TaskID string
	// Prompt is the first message, submitted as the program starts.
	Prompt string
}

// Manager owns the agents.
type Manager struct {
	cfg      Config
	onChange func(Info)

	mu     sync.Mutex
	agents map[string]*Agent
}

// NewManager returns a manager that reports every change of an agent to
// onChange.
func NewManager(cfg Config, onChange func(Info)) *Manager {
	if cfg.Program == "" {
		cfg.Program = "claude"
	}

	if cfg.RingSize <= 0 {
		cfg.RingSize = 4 << 20
	}

	return &Manager{cfg: cfg, onChange: onChange, agents: map[string]*Agent{}}
}

// Restore brings back agents saved by an earlier daemon. Their programs
// ended with it, so live ones come back lost, ready to resume.
func (m *Manager) Restore(infos []Info) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, info := range infos {
		if info.Status.Live() {
			info.Status, info.Detail, info.PID = Lost, "", 0
		}

		a := newAgent(info, m.cfg.RingSize, m.changed)
		a.note("daemon restarted; earlier output is gone")
		m.agents[info.ID] = a
	}
}

// Spawn starts a new agent with a fresh session.
func (m *Manager) Spawn(o SpawnOptions) (Info, error) {
	cwd, err := filepath.Abs(o.Cwd)
	if err != nil {
		return Info{}, err
	}

	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return Info{}, fmt.Errorf("no such folder: %s", cwd)
	}

	now := time.Now()
	info := Info{
		ID:        newID(now),
		Name:      o.Name,
		Cwd:       cwd,
		TaskID:    o.TaskID,
		SessionID: newUUID(),
		Status:    Starting,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if info.Name == "" {
		info.Name = filepath.Base(cwd)
	}

	a := newAgent(info, m.cfg.RingSize, m.changed)

	cmd, err := m.command(info, false, o.Prompt)
	if err != nil {
		return Info{}, err
	}

	if err := a.start(cmd); err != nil {
		return Info{}, err
	}

	m.mu.Lock()
	m.agents[info.ID] = a
	m.mu.Unlock()

	return a.Info(), nil
}

// Resume starts an ended agent again in its session, with all its earlier
// conversation, and submits prompt if there is one.
func (m *Manager) Resume(id, prompt string) (Info, error) {
	a, err := m.Get(id)
	if err != nil {
		return Info{}, err
	}

	info := a.Info()
	if info.Status.Live() {
		return Info{}, ErrLive
	}

	cmd, err := m.command(info, true, prompt)
	if err != nil {
		return Info{}, err
	}

	a.note("resumed session " + info.SessionID)

	if err := a.start(cmd); err != nil {
		return Info{}, err
	}

	return a.Info(), nil
}

// command is how to run an agent: the program, its hooks reporting to the
// daemon, and the session it keeps.
func (m *Manager) command(info Info, resume bool, prompt string) (*exec.Cmd, error) {
	path, err := exec.LookPath(m.cfg.Program)
	if err != nil {
		return nil, fmt.Errorf("agent program %q: %w", m.cfg.Program, err)
	}

	settings, err := m.settings()
	if err != nil {
		return nil, err
	}

	args := slices.Clone(m.cfg.Args)
	args = append(args, "--settings", settings)

	if resume {
		args = append(args, "--resume", info.SessionID)
	} else {
		args = append(args, "--session-id", info.SessionID)
	}

	if prompt != "" {
		if strings.HasPrefix(prompt, "-") {
			prompt = " " + prompt // not an option
		}

		args = append(args, prompt)
	}

	cmd := exec.Command(path, args...)
	cmd.Dir = info.Cwd
	cmd.Env = m.env(info)

	return cmd, nil
}

// settings is the --settings JSON that adds the daemon's hooks and
// permission rules. Claude merges it with the user's own settings; nothing
// on disk changes.
func (m *Manager) settings() (string, error) {
	if m.cfg.HookCommand == "" && len(m.cfg.Allow) == 0 {
		return "{}", nil
	}

	type hook struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}

	type matcher struct {
		Hooks []hook `json:"hooks"`
	}

	hooks := map[string][]matcher{}
	for _, e := range hookEvents {
		hooks[e] = []matcher{{Hooks: []hook{{Type: "command", Command: m.cfg.HookCommand, Timeout: 5}}}}
	}

	settings := map[string]any{"hooks": hooks}
	if len(m.cfg.Allow) > 0 {
		settings["permissions"] = map[string]any{"allow": m.cfg.Allow}
	}

	b, err := json.Marshal(settings)

	return string(b), err
}

func (m *Manager) env(info Info) []string {
	extra := map[string]string{
		"TERM":         "xterm-256color",
		"COLORTERM":    "truecolor",
		"RLA_AGENT_ID": info.ID,
		"RLA_TASK_ID":  info.TaskID,
	}

	for k, v := range m.cfg.Env {
		extra[k] = v
	}

	var env []string

	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")

		switch {
		case sessionVars[k] || strings.HasPrefix(k, "CLAUDE_CODE_MESSAGING_") || strings.HasPrefix(k, "RLA_"):
			// Not a nested session; the daemon's own settings come below.
			continue
		case k == "PATH" && extra["PATH"] != "":
			extra["PATH"] += string(os.PathListSeparator) + v

			continue
		}

		if _, ok := extra[k]; !ok {
			env = append(env, kv)
		}
	}

	for k, v := range extra {
		env = append(env, k+"="+v)
	}

	return env
}

func (m *Manager) changed(id string) {
	a, err := m.Get(id)
	if err == nil && m.onChange != nil {
		m.onChange(a.Info())
	}
}

// Get returns an agent by id.
func (m *Manager) Get(id string) (*Agent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	a, ok := m.agents[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	return a, nil
}

// List returns every agent, the oldest first.
func (m *Manager) List() []Info {
	m.mu.Lock()
	agents := make([]*Agent, 0, len(m.agents))

	for _, a := range m.agents {
		agents = append(agents, a)
	}
	m.mu.Unlock()

	infos := make([]Info, 0, len(agents))
	for _, a := range agents {
		infos = append(infos, a.Info())
	}

	slices.SortFunc(infos, func(x, y Info) int { return x.CreatedAt.Compare(y.CreatedAt) })

	return infos
}

// Hook applies a hook event reported for an agent.
func (m *Manager) Hook(id string, h Hook) error {
	a, err := m.Get(id)
	if err != nil {
		return err
	}

	a.Hook(h)

	return nil
}

// Remove forgets an agent that is no longer running.
func (m *Manager) Remove(id string) error {
	a, err := m.Get(id)
	if err != nil {
		return err
	}

	if a.Info().Status.Live() {
		return ErrLive
	}

	m.mu.Lock()
	delete(m.agents, id)
	m.mu.Unlock()

	return nil
}

// KillAll ends every running agent and waits for them.
func (m *Manager) KillAll() {
	var wg sync.WaitGroup

	for _, info := range m.List() {
		if !info.Status.Live() {
			continue
		}

		a, err := m.Get(info.ID)
		if err != nil {
			continue
		}

		wg.Go(func() { _ = a.Kill() })
	}

	wg.Wait()
}

var idSeq atomic.Int64

func newID(now time.Time) string {
	return "a" + strconv.FormatInt(now.UnixMilli(), 36) + strconv.FormatInt(idSeq.Add(1)%36, 36)
}

func newUUID() string {
	var b [16]byte

	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// AgentInfo returns what an agent is doing.
func (m *Manager) AgentInfo(id string) (Info, error) {
	a, err := m.Get(id)
	if err != nil {
		return Info{}, err
	}

	return a.Info(), nil
}

// SendText submits text to an idle agent.
func (m *Manager) SendText(id, text string) error {
	a, err := m.Get(id)
	if err != nil {
		return err
	}

	return a.Send(text, false)
}

// WaitAgent waits up to timeout for an agent's program to end.
func (m *Manager) WaitAgent(id string, timeout time.Duration) {
	a, err := m.Get(id)
	if err != nil {
		return
	}

	done := make(chan struct{})

	go func() {
		a.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// KillAgent ends an agent's program.
func (m *Manager) KillAgent(id string) error {
	a, err := m.Get(id)
	if err != nil {
		return err
	}

	return a.Kill()
}
