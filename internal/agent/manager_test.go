package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeAgent acts like an interactive agent: it sets an idle title, echoes
// each line it reads while showing a busy title, and records its arguments.
const fakeAgent = `#!/bin/sh
printf '%s\n' "$@" > "$(dirname "$0")/args"
env > "$(dirname "$0")/env"
printf '\033[c\033]0;✳ Fake\007ready\r\n'
while IFS= read -r line; do
  printf '\033]0;◐ Fake\007got: %s\r\n' "$line"
  sleep 0.2
  printf '\033]0;✳ Fake\007'
done
`

func TestAgentInATerminal(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "fake-agent")

	if err := os.WriteFile(prog, []byte(fakeAgent), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "parent")
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")

	changes := make(chan Info, 100)
	m := NewManager(Config{Program: prog, HookCommand: "rla hook", Allow: []string{"Bash(rla report:*)"}, Env: map[string]string{"PATH": "/opt/rla", "RLA_ADDR": "x"}},
		func(i Info) { changes <- i })

	info, err := m.Spawn(SpawnOptions{Cwd: dir, Prompt: "-first task"})
	if err != nil {
		t.Fatal(err)
	}

	a, _ := m.Get(info.ID)
	waitStatus(t, a, Idle)

	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	for _, want := range []string{"--settings", `"PermissionRequest"`, `"command":"rla hook"`, `"allow":["Bash(rla report:*)"]`, "--session-id", info.SessionID, " -first task"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args lack %q:\n%s", want, args)
		}
	}

	env, _ := os.ReadFile(filepath.Join(dir, "env"))
	for _, want := range []string{"RLA_AGENT_ID=" + info.ID, "RLA_ADDR=x", "PATH=/opt/rla:", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "TERM=xterm-256color"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("env lacks %q", want)
		}
	}

	if strings.Contains(string(env), "CLAUDE_CODE_SESSION_ID") {
		t.Error("env has the parent session")
	}

	replay, out, detach := a.Attach()
	defer detach()

	if !bytes.Contains(replay, []byte("ready")) || bytes.Contains(replay, []byte("\x1b[c")) {
		t.Errorf("replay = %q", replay)
	}

	if err := a.Send("hello", false); err != nil {
		t.Fatal(err)
	}

	waitOutput(t, out, "got: \x1b[200~hello\x1b[201~")

	if err := a.Kill(); err != nil {
		t.Fatal(err)
	}

	if got := a.Info(); got.Status != Exited || got.ExitCode == nil {
		t.Fatalf("after kill: %+v", got)
	}

	if err := a.Send("again", true); err == nil {
		t.Fatal("send to an ended agent worked")
	}

	if err := m.Remove(info.ID); err != nil {
		t.Fatal(err)
	}

	if len(changes) == 0 {
		t.Fatal("no change reported")
	}
}

func TestRestoredAgentsAreLost(t *testing.T) {
	m := NewManager(Config{}, nil)
	m.Restore([]Info{{ID: "a1", Status: Working, PID: 7}, {ID: "a2", Status: Exited}})

	got := m.List()
	if got[0].Status != Lost || got[0].PID != 0 || got[1].Status != Exited {
		t.Fatalf("restored %+v", got)
	}
}

func waitStatus(t *testing.T, a *Agent, want Status) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.Info().Status == want {
			return
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatalf("status %s, want %s", a.Info().Status, want)
}

func waitOutput(t *testing.T, out <-chan []byte, want string) {
	t.Helper()

	var seen []byte

	timeout := time.After(5 * time.Second)

	for {
		select {
		case p, ok := <-out:
			if !ok {
				t.Fatalf("output ended before %q; saw %q", want, seen)
			}

			seen = append(seen, p...)
			if bytes.Contains(seen, []byte(want)) {
				return
			}
		case <-timeout:
			t.Fatalf("no %q in %q", want, seen)
		}
	}
}

func TestDialogBeforeTheSessionShows(t *testing.T) {
	dir := t.TempDir()
	prog := filepath.Join(dir, "dialog-agent")
	script := "#!/bin/sh\nprintf 'Yes,\\033[9GI\\033[11Gtrust\\033[17Gthis\\033[22Gfolder\\r\\nEnter\\033[8Gto\\033[11Gconfirm \\033[21GEsc\\033[25Gto\\033[28Gcancel\\r\\n'\nread x\n"

	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewManager(Config{Program: prog}, nil)

	info, err := m.Spawn(SpawnOptions{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}

	a, _ := m.Get(info.ID)
	waitStatus(t, a, Blocked)

	if got := a.Info().Detail; got != "启动时需要确认: 信任这个目录" {
		t.Fatalf("detail = %q", got)
	}

	_ = a.Kill()
}
