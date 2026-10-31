// Command rla schedules coding agents from the outside: a daemon keeps each
// agent in its own terminal, hands it one task, follows its status through
// hooks, and serves the desktop app that shows it all.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/api"
	"github.com/hewenyu/react-loop-agent/internal/client"
	"github.com/hewenyu/react-loop-agent/internal/sched"
)

// Version is set at build time.
var Version = "dev"

const usage = `rla: run coding agents from an outside scheduler

Usage:
  rla daemon [-agent claude] [-agent-args "..."] [-claude-bot claude-bot]
                                     run the daemon (the app starts it for you)
  rla task add [-C dir] [-title t] <prompt>
                                     queue a task
  rla task cancel|retry|finish <task>
  rla repo add [-claude-bot] [-concurrency n] <dir>
                                     register a repo; -claude-bot takes tasks from its claude-bot inbox
  rla ls                             list tasks and agents
  rla read <agent>                   print an agent's terminal output
  rla keys <agent> <key>...          press keys: enter esc up down left right tab space ctrl-c, or text
  rla report [task] <done|failed|need-input> <summary>
                                     (for agents) report on the task you were given
  rla hook                           (for agents) report a Claude Code hook event, read from stdin
  rla stop                           stop the daemon and its agents
  rla version
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rla:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		fmt.Print(usage)

		return nil
	}

	switch cmd, rest := args[0], args[1:]; cmd {
	case "daemon":
		return cmdDaemon(rest)
	case "hook":
		cmdHook()

		return nil
	case "report":
		return cmdReport(rest)
	case "task":
		return cmdTask(rest)
	case "repo":
		return cmdRepo(rest)
	case "ls":
		return cmdList()
	case "read":
		return cmdRead(rest)
	case "keys":
		return cmdKeys(rest)
	case "stop":
		return call("POST", "/api/shutdown", nil, nil)
	case "version":
		fmt.Println(Version)

		return nil
	case "help", "-h", "--help":
		fmt.Print(usage)

		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
	}
}

// cmdHook forwards a hook event to the daemon. It never fails the hook: a
// daemon that is gone must not stop the agent.
func cmdHook() {
	body, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil || os.Getenv("RLA_AGENT_ID") == "" {
		return
	}

	c, err := client.New()
	if err != nil {
		return
	}

	c.HTTP.Timeout = 2 * time.Second
	_ = c.Do(context.Background(), "POST", "/api/hook", body, nil)
}

func cmdReport(args []string) error {
	task := os.Getenv("RLA_TASK_ID")

	if len(args) > 0 && !isReport(args[0]) {
		task, args = args[0], args[1:]
	}

	if task == "" || len(args) == 0 || !isReport(args[0]) {
		return errors.New("usage: rla report [task] <done|failed|need-input> <summary>")
	}

	summary := strings.Join(args[1:], " ")
	if summary == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}

		summary = string(b)
	}

	if err := call("POST", "/api/report", map[string]string{"task": task, "status": args[0], "summary": summary}, nil); err != nil {
		return err
	}

	fmt.Printf("reported %s %s; end your turn now\n", task, args[0])

	return nil
}

func isReport(s string) bool {
	return s == sched.ReportDone || s == sched.ReportFailed || s == sched.ReportNeedInput
}

func cmdTask(args []string) error {
	if len(args) == 2 && (args[0] == "cancel" || args[0] == "retry" || args[0] == "finish") {
		return call("POST", "/api/tasks/"+args[1]+"/"+args[0], nil, nil)
	}

	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: rla task add [-C dir] [-title t] <prompt>\n       rla task cancel|retry|finish <task>")
	}

	fs := flag.NewFlagSet("task add", flag.ContinueOnError)
	dir := fs.String("C", ".", "folder the agent works in")
	title := fs.String("title", "", "short title")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	prompt := strings.Join(fs.Args(), " ")
	if prompt == "" || prompt == "-" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}

		prompt = string(b)
	}

	abs, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}

	var t sched.Task
	if err := call("POST", "/api/tasks", sched.NewTask{Title: *title, Prompt: prompt, Cwd: abs}, &t); err != nil {
		return err
	}

	fmt.Println(t.ID)

	return nil
}

func cmdRepo(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return errors.New("usage: rla repo add [-claude-bot] [-concurrency n] <dir>")
	}

	fs := flag.NewFlagSet("repo add", flag.ContinueOnError)
	bot := fs.Bool("claude-bot", false, "take tasks from the repo's claude-bot inbox")
	n := fs.Int("concurrency", 1, "tasks that may run in the repo at once")

	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	dir := "."
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	var r sched.Repo
	if err := call("POST", "/api/repos", sched.Repo{Path: abs, ClaudeBot: *bot, Concurrency: *n}, &r); err != nil {
		return err
	}

	fmt.Printf("%s (claude-bot %v, concurrency %d)\n", r.Path, r.ClaudeBot, r.Concurrency)

	return nil
}

func cmdList() error {
	var st api.State
	if err := call("GET", "/api/state", nil, &st); err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TASK\tSTATE\tAGENT\tFOLDER\tTITLE\tDETAIL")

	for _, t := range st.Tasks {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.State, t.AgentID, filepath.Base(t.Cwd), t.Title, t.Detail)
	}

	fmt.Fprintln(w, "\nAGENT\tSTATUS\tTURNS\tFOLDER\tTASK\tDETAIL")

	for _, a := range st.Agents {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n", a.ID, a.Status, a.Turns, filepath.Base(a.Cwd), a.TaskID, a.Detail)
	}

	return w.Flush()
}

func cmdRead(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: rla read <agent>")
	}

	var b []byte
	if err := call("GET", "/api/agents/"+args[0]+"/screen", nil, &b); err != nil {
		return err
	}

	_, err := os.Stdout.Write(b)

	return err
}

var keyNames = map[string]string{
	"enter": "\r", "esc": "\x1b", "tab": "\t", "space": " ", "backspace": "\x7f",
	"up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D", "ctrl-c": "\x03",
}

func cmdKeys(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: rla keys <agent> <key>...")
	}

	var b strings.Builder

	for _, k := range args[1:] {
		if seq, ok := keyNames[strings.ToLower(k)]; ok {
			b.WriteString(seq)
		} else {
			b.WriteString(k)
		}
	}

	return call("POST", "/api/agents/"+args[0]+"/keys", map[string]string{"text": b.String()}, nil)
}

func call(method, path string, in, out any) error {
	c, err := client.New()
	if err != nil {
		return err
	}

	return c.Do(context.Background(), method, path, in, out)
}
