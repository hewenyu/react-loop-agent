package sched

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// BotTask is a claude-bot task: one thing people asked for in a chat or on
// GitHub, with its whole conversation.
type BotTask struct {
	ID      string
	Title   string
	Entries int
	// Line is the task as `claude-bot inbox` shows it, for the agent.
	Line []byte
}

// BotSource reads a repo's claude-bot inbox. The gateway keeps filling it;
// the scheduler only takes tasks from it and agents answer them with
// `claude-bot reply`.
type BotSource interface {
	// Pending lists the tasks waiting for an answer, without taking them.
	Pending(repo string) ([]BotTask, error)
	// Take shows the pending tasks to Claude, as `claude-bot inbox` does,
	// and returns the one with this id: a reply then closes what it showed.
	Take(repo, id string) (BotTask, error)
	// Done reports whether the task was answered and closed.
	Done(repo, id string) bool
}

// ErrNotPending reports a task that was closed before it was taken.
var ErrNotPending = errors.New("claude-bot task is not pending")

// ClaudeBot reads the inbox files and runs the claude-bot command.
type ClaudeBot struct {
	// Program is the claude-bot command.
	Program string
}

type botFile struct {
	ID      string `json:"id"`
	Handled int    `json:"handled"`
	Entries []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	} `json:"entries"`
}

func tasksDir(repo, state string) string {
	return filepath.Join(repo, ".claude-bot", "inbox", "tasks", state)
}

// fileName is how claude-bot names a task's file (channel.PathSafe there):
// task:x becomes task_x.
func fileName(id string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}

		return '_'
	}, id) + ".json"
}

// Pending reads the pending tasks' files.
func (c ClaudeBot) Pending(repo string) ([]BotTask, error) {
	dir := tasksDir(repo, "pending")

	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}

	tasks := make([]BotTask, 0, len(names))

	for _, name := range names {
		b, err := os.ReadFile(name)
		if err != nil {
			continue // closed meanwhile
		}

		var f botFile
		if err := json.Unmarshal(b, &f); err != nil || f.ID == "" {
			continue
		}

		tasks = append(tasks, BotTask{ID: f.ID, Title: botTitle(f), Entries: len(f.Entries)})
	}

	return tasks, nil
}

// botTitle is the first line of the first thing people said that was not
// handled yet.
func botTitle(f botFile) string {
	for i, e := range f.Entries {
		if e.Kind == "user" && i >= f.Handled && strings.TrimSpace(e.Text) != "" {
			return clip(firstLine(strings.TrimSpace(e.Text)), 80)
		}
	}

	return f.ID
}

// Take runs `claude-bot inbox` in the repo and returns the task's line.
func (c ClaudeBot) Take(repo, id string) (BotTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.program(), "-C", repo, "inbox")

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return BotTask{}, fmt.Errorf("claude-bot inbox: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 64<<20)

	for sc.Scan() {
		var f botFile
		if json.Unmarshal(sc.Bytes(), &f) != nil || f.ID != id {
			continue
		}

		return BotTask{ID: id, Title: botTitle(f), Entries: len(f.Entries), Line: bytes.Clone(sc.Bytes())}, nil
	}

	return BotTask{}, fmt.Errorf("%w: %s", ErrNotPending, id)
}

// Done reports whether the task's file is among the done ones.
func (c ClaudeBot) Done(repo, id string) bool {
	if _, err := os.Stat(filepath.Join(tasksDir(repo, "pending"), fileName(id))); err == nil {
		return false
	}

	_, err := os.Stat(filepath.Join(tasksDir(repo, "done"), fileName(id)))

	return err == nil
}

func (c ClaudeBot) program() string {
	if c.Program != "" {
		return c.Program
	}

	return "claude-bot"
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")

	return line
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}

	return s
}
