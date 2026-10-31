package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/agent"
	"github.com/hewenyu/react-loop-agent/internal/sched"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name, file string
		want       Config
		wantErr    bool
	}{
		{"none", "", Config{Agent: agent.DefaultProgram, ClaudeBot: sched.DefaultBotProgram}, false},
		{"empty fields keep the defaults", `{"agent":"","claude_bot":""}`, Config{Agent: agent.DefaultProgram, ClaudeBot: sched.DefaultBotProgram}, false},
		{"set", `{"agent":"my-claude","agent_args":["-x"],"claude_bot":"cb"}`, Config{Agent: "my-claude", AgentArgs: []string{"-x"}, ClaudeBot: "cb"}, false},
		{"broken", `{"agent":`, Config{}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			if tt.file != "" {
				if err := os.WriteFile(filepath.Join(home, ConfigFile), []byte(tt.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got, err := LoadConfig(home)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, want error %v", err, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			if got.Agent != tt.want.Agent || got.ClaudeBot != tt.want.ClaudeBot || len(got.AgentArgs) != len(tt.want.AgentArgs) {
				t.Fatalf("config = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStateRoundTrip(t *testing.T) {
	home := filepath.Join(t.TempDir(), "rla") // made on the first save

	empty, err := Load(home)
	if err != nil || len(empty.Tasks) != 0 {
		t.Fatalf("Load of nothing = %+v, %v", empty, err)
	}

	st := State{
		Repos:  []sched.Repo{{Path: "/repo", Concurrency: 2}},
		Tasks:  []sched.Task{{ID: "t1", State: sched.Queued, CreatedAt: time.Now().Round(0)}},
		Agents: []agent.Info{{ID: "a1", Status: agent.Idle}},
	}

	if err := Save(home, st); err != nil {
		t.Fatal(err)
	}

	got, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Repos) != 1 || got.Repos[0].Concurrency != 2 || got.Tasks[0].ID != "t1" || got.Agents[0].Status != agent.Idle {
		t.Fatalf("loaded %+v", got)
	}

	fi, err := os.Stat(filepath.Join(home, StateFile))
	if err != nil {
		t.Fatal(err)
	}

	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state is %o, want 600", perm)
	}

	if tmps, _ := filepath.Glob(filepath.Join(home, ".tmp-*")); len(tmps) != 0 {
		t.Fatalf("temporary files left: %v", tmps)
	}
}

func TestDaemonRecord(t *testing.T) {
	home := t.TempDir()

	if _, err := ReadDaemon(home); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadDaemon of nothing: %v", err)
	}

	if err := WriteDaemon(home, Daemon{Addr: "127.0.0.1:1", Token: "secret", PID: 1}); err != nil {
		t.Fatal(err)
	}

	d, err := ReadDaemon(home)
	if err != nil || d.Token != "secret" {
		t.Fatalf("ReadDaemon = %+v, %v", d, err)
	}

	RemoveDaemon(home)

	if _, err := ReadDaemon(home); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("record left after RemoveDaemon: %v", err)
	}
}

func TestHome(t *testing.T) {
	t.Setenv(HomeEnv, "relative/rla")

	got, err := Home()
	if err != nil {
		t.Fatal(err)
	}

	if !filepath.IsAbs(got) {
		t.Fatalf("Home = %q, want an absolute path", got)
	}
}
