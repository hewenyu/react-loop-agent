package sched

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/agent"
)

// fakeAgents keeps agents as plain records the test changes by hand.
type fakeAgents struct {
	mu     sync.Mutex
	n      int
	agents map[string]*agent.Info
	// calls records what the scheduler asked, e.g. "spawn a1: <prompt>".
	calls []string
	// killDelay is how long a killed agent takes to end.
	killDelay time.Duration
}

func newFakeAgents() *fakeAgents {
	return &fakeAgents{agents: map[string]*agent.Info{}}
}

func (f *fakeAgents) Spawn(o agent.SpawnOptions) (agent.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.n++
	id := fmt.Sprintf("a%d", f.n)
	f.agents[id] = &agent.Info{ID: id, Cwd: o.Cwd, TaskID: o.TaskID, SessionID: "s-" + id, Status: agent.Starting}
	f.calls = append(f.calls, "spawn "+id+": "+o.Prompt)

	return *f.agents[id], nil
}

func (f *fakeAgents) Resume(id, prompt string) (agent.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	a := f.agents[id]
	a.Status = agent.Starting
	f.calls = append(f.calls, "resume "+id+": "+prompt)

	return *a, nil
}

func (f *fakeAgents) AgentInfo(id string) (agent.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	a, ok := f.agents[id]
	if !ok {
		return agent.Info{}, agent.ErrNotFound
	}

	return *a, nil
}

func (f *fakeAgents) WaitAgent(id string, _ time.Duration) {
	for range 100 {
		if a, _ := f.AgentInfo(id); !a.Status.Live() {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fakeAgents) SendText(id, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.agents[id].Status != agent.Idle {
		return agent.ErrBusy
	}

	f.calls = append(f.calls, "send "+id+": "+text)

	return nil
}

func (f *fakeAgents) KillAgent(id string) error {
	time.Sleep(f.killDelay)

	f.mu.Lock()
	defer f.mu.Unlock()

	f.agents[id].Status = agent.Exited
	f.calls = append(f.calls, "kill "+id)

	return nil
}

func (f *fakeAgents) set(id string, st agent.Status, turns int, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	a := f.agents[id]
	a.Status, a.Turns, a.Detail = st, turns, detail
	a.LastMessage = fmt.Sprintf("turn %d", turns)
}

// lastCall waits for a call starting with prefix and returns it.
func (f *fakeAgents) lastCall(t *testing.T, prefix string) string {
	t.Helper()

	for range 100 {
		f.mu.Lock()
		for i := len(f.calls) - 1; i >= 0; i-- {
			if strings.HasPrefix(f.calls[i], prefix) {
				c := f.calls[i]
				f.mu.Unlock()

				return c
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("no call %q in %q", prefix, f.calls)

	return ""
}

type fakeBot struct {
	mu      sync.Mutex
	pending map[string]int // id → entries
	done    map[string]bool
	takes   int
}

func (b *fakeBot) Pending(string) ([]BotTask, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []BotTask
	for id, n := range b.pending {
		out = append(out, BotTask{ID: id, Title: "q " + id, Entries: n})
	}

	return out, nil
}

func (b *fakeBot) Take(_, id string) (BotTask, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	n, ok := b.pending[id]
	if !ok {
		return BotTask{}, ErrNotPending
	}

	b.takes++

	return BotTask{ID: id, Entries: n, Line: fmt.Appendf(nil, `{"id":%q,"n":%d}`, id, n)}, nil
}

func (b *fakeBot) Done(_, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.done[id]
}

func (b *fakeBot) reply(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	delete(b.pending, id)
	b.done[id] = true
}

func newTest(t *testing.T) (*Scheduler, *fakeAgents, *fakeBot) {
	t.Helper()

	fa := newFakeAgents()
	fb := &fakeBot{pending: map[string]int{}, done: map[string]bool{}}

	return New(Options{Agents: fa, Bot: fb, Report: "rla report"}, Saved{}), fa, fb
}

func task(t *testing.T, s *Scheduler, id string) Task {
	t.Helper()

	for _, c := range s.Saved().Tasks {
		if c.ID == id {
			return c
		}
	}

	t.Fatalf("no task %s", id)

	return Task{}
}

func expectState(t *testing.T, s *Scheduler, id string, want State) Task {
	t.Helper()

	got := task(t, s, id)
	if got.State != want {
		t.Fatalf("task %s is %s (%s), want %s; log %v", id, got.State, got.Detail, want, got.Log)
	}

	return got
}

func TestManualTaskRunsToDone(t *testing.T) {
	s, fa, _ := newTest(t)
	tk, _ := s.Add(NewTask{Prompt: "fix the bug", Cwd: "/repo"})

	s.Step()

	call := fa.lastCall(t, "spawn a1: ")
	if !strings.Contains(call, "fix the bug") || !strings.Contains(call, "rla report "+tk.ID+" done") {
		t.Fatalf("prompt = %q", call)
	}

	fa.set("a1", agent.Idle, 0, "") // SessionStart before the prompt runs
	s.Step()
	expectState(t, s, tk.ID, Running)

	fa.set("a1", agent.Blocked, 0, "需要授权: Bash: rm x")
	s.Step()

	if got := expectState(t, s, tk.ID, Blocked); got.Detail != "需要授权: Bash: rm x" {
		t.Fatalf("detail = %q", got.Detail)
	}

	fa.set("a1", agent.Working, 0, "")
	_ = s.Report(tk.ID, ReportDone, "fixed in #12")
	s.Step()
	expectState(t, s, tk.ID, Running) // done once the turn ends

	fa.set("a1", agent.Idle, 1, "")
	s.Step()

	if got := expectState(t, s, tk.ID, Done); got.Summary != "fixed in #12" {
		t.Fatalf("summary = %q", got.Summary)
	}

	fa.lastCall(t, "kill a1")
}

func TestTurnWithoutReportWaitsThenRetryResumes(t *testing.T) {
	s, fa, _ := newTest(t)
	tk, _ := s.Add(NewTask{Prompt: "do it", Cwd: "/repo"})
	s.Step()

	fa.set("a1", agent.Idle, 1, "")
	s.Step()

	if got := expectState(t, s, tk.ID, Waiting); got.Summary != "turn 1" {
		t.Fatalf("summary = %q", got.Summary)
	}

	if err := s.Retry(tk.ID); err != nil {
		t.Fatal(err)
	}

	s.Step()

	if call := fa.lastCall(t, "send a1: "); !strings.Contains(call, "上次这个任务没有完成") {
		t.Fatalf("retry message = %q", call)
	}

	if got := expectState(t, s, tk.ID, Running); got.TurnBase != 1 || got.Attempts != 2 {
		t.Fatalf("turn base %d, attempts %d", got.TurnBase, got.Attempts)
	}

	// Ended for good, a retry resumes the session.
	fa.set("a1", agent.Exited, 2, "")
	s.Step()
	expectState(t, s, tk.ID, Failed)

	if err := s.Retry(tk.ID); err != nil {
		t.Fatal(err)
	}

	s.Step()
	fa.lastCall(t, "resume a1: ")
	expectState(t, s, tk.ID, Running)
}

func TestOneTaskPerRepoAtATime(t *testing.T) {
	s, fa, _ := newTest(t)
	first, _ := s.Add(NewTask{Prompt: "one", Cwd: "/repo"})
	second, _ := s.Add(NewTask{Prompt: "two", Cwd: "/repo"})
	other, _ := s.Add(NewTask{Prompt: "three", Cwd: "/other"})

	s.Step()
	expectState(t, s, first.ID, Running)
	expectState(t, s, second.ID, Queued)
	expectState(t, s, other.ID, Running)

	_ = s.Cancel(first.ID)
	s.Step()
	expectState(t, s, first.ID, Cancelled)
	expectState(t, s, second.ID, Running)
	fa.lastCall(t, "kill a1")
}

func TestRepoConcurrency(t *testing.T) {
	s, _, _ := newTest(t)
	_, _ = s.SetRepo(Repo{Path: "/repo", Concurrency: 2})
	a, _ := s.Add(NewTask{Prompt: "one", Cwd: "/repo"})
	b, _ := s.Add(NewTask{Prompt: "two", Cwd: "/repo"})

	s.Step()
	expectState(t, s, a.ID, Running)
	expectState(t, s, b.ID, Running)
}

func TestClaudeBotTaskWithFollowUp(t *testing.T) {
	s, fa, fb := newTest(t)
	_, _ = s.SetRepo(Repo{Path: "/repo", ClaudeBot: true})
	fb.pending["task:x"] = 2

	s.pollBots()
	s.pollBots() // seen twice, queued once

	tasks := s.Saved().Tasks
	if len(tasks) != 1 || tasks[0].External != "task:x" || tasks[0].Title != "q task:x" {
		t.Fatalf("tasks = %+v", tasks)
	}

	id := tasks[0].ID
	s.Step()

	if call := fa.lastCall(t, "spawn a1: "); !strings.Contains(call, `{"id":"task:x","n":2}`) || !strings.Contains(call, "bot.md") {
		t.Fatalf("prompt = %q", call)
	}

	if got := expectState(t, s, id, Running); got.Seen != 2 {
		t.Fatalf("seen = %d", got.Seen)
	}

	// Someone adds to the conversation while the agent works.
	fa.set("a1", agent.Working, 0, "")
	fb.pending["task:x"] = 3
	s.pollBots()
	s.Step()

	if got := expectState(t, s, id, Running); !got.FollowUp {
		t.Fatal("follow-up not noted")
	}

	// The turn ends: the news goes to the same agent.
	fa.set("a1", agent.Idle, 1, "")
	s.Step()
	s.Step()

	if call := fa.lastCall(t, "send a1: "); !strings.Contains(call, `"n":3`) || !strings.Contains(call, "新消息") {
		t.Fatalf("follow-up = %q", call)
	}

	if got := expectState(t, s, id, Running); got.Seen != 3 || got.TurnBase != 1 {
		t.Fatalf("seen %d, turn base %d", got.Seen, got.TurnBase)
	}

	// It replies, which closes the claude-bot task, without reporting.
	fb.reply("task:x")
	fa.set("a1", agent.Idle, 2, "")
	s.Step()
	expectState(t, s, id, Done)
	fa.lastCall(t, "kill a1")

	// A day later the conversation goes on: the ended agent resumes.
	fb.mu.Lock()
	fb.pending["task:x"], fb.done["task:x"] = 4, false
	fb.mu.Unlock()
	s.pollBots()
	expectState(t, s, id, Queued)
	s.Step()

	if call := fa.lastCall(t, "resume a1: "); !strings.Contains(call, `"n":4`) {
		t.Fatalf("resume = %q", call)
	}
}

func TestClaudeBotTaskClosedBeforeItStarts(t *testing.T) {
	s, _, fb := newTest(t)
	_, _ = s.SetRepo(Repo{Path: "/repo", ClaudeBot: true})
	fb.pending["task:y"] = 1
	s.pollBots()

	fb.reply("task:y")
	s.Step()

	if got := s.Saved().Tasks[0]; got.State != Done {
		t.Fatalf("state = %s", got.State)
	}
}

func TestLostAgentResumes(t *testing.T) {
	s, fa, _ := newTest(t)
	tk, _ := s.Add(NewTask{Prompt: "long job", Cwd: "/repo"})
	s.Step()

	fa.set("a1", agent.Lost, 0, "")
	s.Step()
	s.Step()

	fa.lastCall(t, "resume a1: ")
	expectState(t, s, tk.ID, Running)
}

func TestFailedStartRetriesLater(t *testing.T) {
	s, _, fb := newTest(t)
	_, _ = s.SetRepo(Repo{Path: "/repo", ClaudeBot: true})
	fb.pending["task:z"] = 1
	s.pollBots()

	s.mu.Lock()
	s.o.Bot = errBot{fb}
	s.mu.Unlock()
	s.Step()

	got := s.Saved().Tasks[0]
	if got.State != Queued || got.Error == "" || !got.NextTry.After(time.Now()) {
		t.Fatalf("after failed start: %+v", got)
	}
}

type errBot struct{ *fakeBot }

func (errBot) Take(string, string) (BotTask, error) {
	return BotTask{}, fmt.Errorf("gateway not running")
}

func TestRetryRightAfterDoneWaitsForTheAgentToEnd(t *testing.T) {
	s, fa, _ := newTest(t)
	fa.killDelay = 100 * time.Millisecond // a program taking its time to exit

	tk, _ := s.Add(NewTask{Prompt: "do it", Cwd: "/repo"})
	s.Step()

	_ = s.Report(tk.ID, ReportDone, "ok")
	fa.set("a1", agent.Idle, 1, "")
	s.Step()
	expectState(t, s, tk.ID, Done)

	if err := s.Retry(tk.ID); err != nil {
		t.Fatal(err)
	}

	s.Step()
	fa.lastCall(t, "resume a1: ")
	expectState(t, s, tk.ID, Running)
}
