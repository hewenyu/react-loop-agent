package sched

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeBotInbox(t *testing.T) {
	repo := t.TempDir()
	pending := tasksDir(repo, "pending")
	done := tasksDir(repo, "done")

	for _, d := range []string{pending, done} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	task := `{"id":"task:abc","handled":1,"entries":[{"kind":"user","text":"old"},{"kind":"bot","text":"hi"},{"kind":"user","text":"  帮我修一下登录\n细节…"}]}`
	write(t, filepath.Join(pending, "task_abc.json"), task)
	write(t, filepath.Join(pending, "broken.json"), "{")
	write(t, filepath.Join(done, "task_old.json"), `{"id":"task:old"}`)

	// A fake claude-bot that prints the inbox the way `inbox` does.
	bin := filepath.Join(t.TempDir(), "claude-bot")
	write(t, bin, "#!/bin/sh\n[ \"$1 $3\" = \"-C inbox\" ] || exit 2\ncat \"$2/.claude-bot/inbox/tasks/pending/task_abc.json\"; echo\n")

	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}

	c := ClaudeBot{Program: bin}

	got, err := c.Pending(repo)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].ID != "task:abc" || got[0].Entries != 3 || got[0].Title != "帮我修一下登录" {
		t.Fatalf("pending = %+v", got)
	}

	bt, err := c.Take(repo, "task:abc")
	if err != nil {
		t.Fatal(err)
	}

	if bt.Entries != 3 || string(bt.Line) != task {
		t.Fatalf("taken = %+v", bt)
	}

	if _, err := c.Take(repo, "task:gone"); !errors.Is(err, ErrNotPending) {
		t.Fatalf("take of a closed task: %v", err)
	}

	if c.Done(repo, "task:abc") || !c.Done(repo, "task:old") || c.Done(repo, "task:never") {
		t.Fatal("done is wrong")
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}
