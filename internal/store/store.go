// Package store keeps the daemon's state on disk, under ~/.rla.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/agent"
	"github.com/hewenyu/react-loop-agent/internal/sched"
)

// State is everything the daemon keeps across restarts.
type State struct {
	Repos  []sched.Repo `json:"repos"`
	Tasks  []sched.Task `json:"tasks"`
	Agents []agent.Info `json:"agents"`
}

// Daemon says how to reach the running daemon.
type Daemon struct {
	Addr      string    `json:"addr"`
	Token     string    `json:"token"`
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	StartedAt time.Time `json:"started_at"`
}

// Config is how the daemon runs agents, from config.json in its folder.
// The app starts the daemon without flags, so this is where such choices
// live; flags override them.
type Config struct {
	// Agent is the agent command.
	Agent string `json:"agent"`
	// AgentArgs go to every agent, e.g. ["--permission-mode", "acceptEdits"].
	AgentArgs []string `json:"agent_args"`
	// ClaudeBot is the claude-bot command.
	ClaudeBot string `json:"claude_bot"`
}

// LoadConfig reads config.json; none is the defaults.
func LoadConfig(home string) (Config, error) {
	cfg := Config{Agent: "claude", ClaudeBot: "claude-bot"}

	err := readJSON(filepath.Join(home, "config.json"), &cfg)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}

	return cfg, nil
}

// Home is the daemon's folder: $RLA_HOME, else ~/.rla.
func Home() (string, error) {
	if h := os.Getenv("RLA_HOME"); h != "" {
		return h, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(home, ".rla"), nil
}

// Load reads the saved state; none saved is an empty state.
func Load(home string) (State, error) {
	var st State

	err := readJSON(filepath.Join(home, "state.json"), &st)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}

	return st, err
}

// Save writes the state.
func Save(home string, st State) error {
	return writeJSON(filepath.Join(home, "state.json"), st)
}

// ReadDaemon reads how to reach the daemon.
func ReadDaemon(home string) (Daemon, error) {
	var d Daemon

	err := readJSON(filepath.Join(home, "daemon.json"), &d)

	return d, err
}

// WriteDaemon records how to reach the daemon. Only the user can read it:
// the token lets anyone drive the agents.
func WriteDaemon(home string, d Daemon) error {
	return writeJSON(filepath.Join(home, "daemon.json"), d)
}

// RemoveDaemon removes the record when the daemon stops.
func RemoveDaemon(home string) {
	_ = os.Remove(filepath.Join(home, "daemon.json"))
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	return nil
}

// writeJSON replaces the file in one step, so a crash never leaves half.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}

	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
