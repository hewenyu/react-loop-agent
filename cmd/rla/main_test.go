package main

import (
	"os/exec"
	"testing"
)

func TestShellQuote(t *testing.T) {
	for _, s := range []string{"/usr/local/bin/rla", "/Users/me/my apps/rla", "/tmp/it's/rla", `/tmp/$HOME/"x"`} {
		out, err := exec.CommandContext(t.Context(), "sh", "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil {
			t.Fatal(err)
		}

		if string(out) != s {
			t.Errorf("sh read %q back as %q", s, out)
		}
	}
}

func TestRunRejectsBadUsage(t *testing.T) {
	for _, args := range [][]string{
		{"nope"},
		{"task"},
		{"repo", "add", "-concurrency", "0", "."},
		{"repo", "add", "a", "b"},
		{"read"},
		{"keys", "a1"},
		{"report", "maybe"},
	} {
		if err := run(args); err == nil {
			t.Errorf("rla %v: no error", args)
		}
	}
}
