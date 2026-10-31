// Package sched decides which task runs when, starts an agent for it, and
// follows the agent until the task is done. The deciding is plain code:
// no model is asked which task to run next.
package sched

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hewenyu/react-loop-agent/internal/agent"
)

// State is where a task is.
type State string

// Task states. Waiting is an agent that ended its turn without saying the
// task is done: it asked something, or it needs a person to look.
const (
	Queued    State = "queued"
	Running   State = "running"
	Blocked   State = "blocked"
	Waiting   State = "waiting"
	Done      State = "done"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Active reports whether an agent works on the task, holding a slot.
func (s State) Active() bool {
	return s == Running || s == Blocked || s == Waiting
}

// Final reports whether the task ended.
func (s State) Final() bool {
	return s == Done || s == Failed || s == Cancelled
}

// Task sources.
const (
	SourceManual    = "manual"
	SourceClaudeBot = "claude-bot"
)

// Report statuses an agent sends with `rla report`.
const (
	ReportDone      = "done"
	ReportFailed    = "failed"
	ReportNeedInput = "need-input"
)

// maxAttempts bounds how often a task is resumed by itself after its agent
// was lost.
const maxAttempts = 3

// retryAfter is how long a task whose start failed waits before the next
// try.
const retryAfter = 30 * time.Second

// Repo is a folder the scheduler runs tasks in.
type Repo struct {
	Path string `json:"path"`
	// ClaudeBot takes tasks from the repo's claude-bot inbox.
	ClaudeBot bool `json:"claude_bot"`
	// Concurrency is how many tasks may run in the repo at once. Agents
	// share the checkout, so it is 1 unless they work on separate trees.
	Concurrency int    `json:"concurrency"`
	Error       string `json:"error,omitempty"`
}

// LogLine is one thing that happened to a task.
type LogLine struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Task is one piece of work for one agent.
type Task struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Prompt   string `json:"prompt,omitempty"`
	Cwd      string `json:"cwd"`
	Source   string `json:"source"`
	External string `json:"external,omitempty"`
	// Seen is how many entries of a claude-bot task its agent was given.
	Seen    int    `json:"seen,omitempty"`
	State   State  `json:"state"`
	AgentID string `json:"agent_id,omitempty"`
	// TurnBase is the agent's turn count when it was given its latest
	// message; a turn after it answers that message.
	TurnBase int    `json:"turn_base"`
	Report   string `json:"report,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Error    string `json:"error,omitempty"`
	Attempts int    `json:"attempts"`
	// FollowUp is set when a claude-bot task got new messages for its agent.
	FollowUp bool `json:"follow_up,omitempty"`
	// Stopped is set when the scheduler ended the agent, which may take a
	// moment: a next start waits for it before resuming.
	Stopped    bool       `json:"stopped,omitempty"`
	NextTry    time.Time  `json:"next_try,omitzero"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Log        []LogLine  `json:"log,omitempty"`
}

// Agents runs agents for the scheduler.
type Agents interface {
	Spawn(o agent.SpawnOptions) (agent.Info, error)
	Resume(id, prompt string) (agent.Info, error)
	AgentInfo(id string) (agent.Info, error)
	WaitAgent(id string, timeout time.Duration)
	SendText(id, text string) error
	KillAgent(id string) error
}

// Options configure a scheduler.
type Options struct {
	Agents Agents
	Bot    BotSource
	// Report is the command agents run to report, e.g. "rla report".
	Report string
	// OnChange is called after any task or repo changes.
	OnChange func()
	// Poll is how often the claude-bot inboxes are read.
	Poll time.Duration
}

// Saved is what the scheduler keeps across restarts.
type Saved struct {
	Repos []Repo `json:"repos"`
	Tasks []Task `json:"tasks"`
}

// Scheduler runs tasks in agents.
type Scheduler struct {
	o    Options
	kick chan struct{}

	mu    sync.Mutex
	repos map[string]*Repo
	tasks map[string]*Task
}

// New returns a scheduler that picks up where saved left off.
func New(o Options, saved Saved) *Scheduler {
	if o.Poll <= 0 {
		o.Poll = 3 * time.Second
	}

	if o.Report == "" {
		o.Report = "rla report"
	}

	s := &Scheduler{o: o, kick: make(chan struct{}, 1), repos: map[string]*Repo{}, tasks: map[string]*Task{}}

	for _, r := range saved.Repos {
		s.repos[r.Path] = &r
	}

	for _, t := range saved.Tasks {
		s.tasks[t.ID] = &t
	}

	return s
}

// Saved returns everything the scheduler knows, to save or to show.
func (s *Scheduler) Saved() Saved {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out Saved

	for _, r := range s.repos {
		out.Repos = append(out.Repos, *r)
	}

	for _, t := range s.tasks {
		c := *t
		c.Log = slices.Clone(t.Log)
		out.Tasks = append(out.Tasks, c)
	}

	slices.SortFunc(out.Repos, func(a, b Repo) int { return cmpString(a.Path, b.Path) })
	slices.SortFunc(out.Tasks, func(a, b Task) int { return a.CreatedAt.Compare(b.CreatedAt) })

	return out
}

// Kick asks the scheduler to look at everything now, e.g. after an agent
// changed.
func (s *Scheduler) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Run schedules until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	tick := time.NewTicker(s.o.Poll)
	defer tick.Stop()

	lastPoll := time.Time{}

	for {
		if time.Since(lastPoll) >= s.o.Poll {
			s.pollBots()

			lastPoll = time.Now()
		}

		s.Step()

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.kick:
		}
	}
}

// Step brings every task up to date with its agent and starts what can
// start.
func (s *Scheduler) Step() {
	s.mu.Lock()

	changed := false

	for _, t := range s.sorted() {
		if s.follow(t) {
			changed = true
		}
	}

	starts := s.startable()
	s.mu.Unlock()

	for _, t := range starts {
		s.start(t)

		changed = true
	}

	if changed {
		s.changed()
	}
}

// follow updates an active task from its agent. Call it with the lock held.
func (s *Scheduler) follow(t *Task) bool {
	if !t.State.Active() || t.AgentID == "" {
		return false
	}

	info, err := s.o.Agents.AgentInfo(t.AgentID)
	if err != nil {
		return s.finish(t, Failed, "agent 不见了: "+err.Error())
	}

	switch info.Status {
	case agent.Starting, agent.Working:
		return s.set(t, Running, "")
	case agent.Blocked:
		return s.set(t, Blocked, info.Detail)
	case agent.Idle:
		if info.Turns <= t.TurnBase {
			return s.set(t, Running, "")
		}

		return s.turnEnded(t, info)
	case agent.Exited, agent.Lost:
		if t.Report == ReportDone {
			return s.finish(t, Done, "")
		}

		if info.Status == agent.Lost && t.Attempts < maxAttempts {
			// The daemon restarted under it: resume where it was.
			t.State, t.Detail, t.NextTry, t.UpdatedAt = Queued, "daemon 重启，恢复会话", time.Time{}, time.Now()
			s.logf(t, "agent 随 daemon 中断，重新排队恢复")

			return true
		}

		return s.finish(t, Failed, "agent 退出了，没有报告完成")
	}

	return false
}

// turnEnded decides what an agent's finished turn means for its task.
func (s *Scheduler) turnEnded(t *Task, info agent.Info) bool {
	if t.Summary == "" || t.Report == "" {
		t.Summary = clip(info.LastMessage, 500)
	}

	switch {
	case t.FollowUp:
		// News came while it worked: give it to the same agent, idle now.
		t.FollowUp = false
		t.State, t.Detail, t.Report, t.NextTry = Queued, "有新消息", "", time.Time{}
		s.logf(t, "回合结束，接着处理新消息")

		return true
	case t.Report == ReportDone:
		s.stopAgent(t)

		return s.finish(t, Done, "")
	case t.Report == ReportFailed:
		s.stopAgent(t)

		return s.finish(t, Failed, "agent 报告失败")
	case t.Source == SourceClaudeBot && s.o.Bot.Done(t.Cwd, t.External):
		// It replied, which closed the task, but did not report.
		s.stopAgent(t)

		return s.finish(t, Done, "")
	case t.Report == ReportNeedInput:
		return s.set(t, Waiting, "等你回答")
	default:
		return s.set(t, Waiting, "回合结束，没有报告结果")
	}
}

func (s *Scheduler) stopAgent(t *Task) {
	id := t.AgentID
	t.Stopped = true

	go func() { _ = s.o.Agents.KillAgent(id) }()
}

// set moves a task to an active state. Call it with the lock held.
func (s *Scheduler) set(t *Task, st State, detail string) bool {
	if t.State == st && t.Detail == detail {
		return false
	}

	if t.State != st {
		s.logf(t, "%s → %s %s", t.State, st, detail)
	}

	t.State, t.Detail, t.UpdatedAt = st, detail, time.Now()

	return true
}

// finish ends a task. Call it with the lock held.
func (s *Scheduler) finish(t *Task, st State, why string) bool {
	if t.State == st {
		return false
	}

	now := time.Now()
	s.logf(t, "%s → %s %s", t.State, st, why)
	t.State, t.Detail, t.UpdatedAt, t.FinishedAt = st, why, now, &now

	if st == Failed && t.Error == "" {
		t.Error = why
	}

	return true
}

// startable picks the queued tasks that fit in their repos' free slots,
// the oldest first, and marks them running. Call it with the lock held.
func (s *Scheduler) startable() []*Task {
	busy := map[string]int{}

	for _, t := range s.tasks {
		if t.State.Active() {
			busy[t.Cwd]++
		}
	}

	var starts []*Task

	now := time.Now()

	for _, t := range s.sorted() {
		if t.State != Queued || now.Before(t.NextTry) {
			continue
		}

		limit := 1
		if r, ok := s.repos[t.Cwd]; ok && r.Concurrency > 0 {
			limit = r.Concurrency
		}

		if busy[t.Cwd] >= limit {
			continue
		}

		busy[t.Cwd]++
		t.State, t.Detail, t.UpdatedAt = Running, "启动中", now
		starts = append(starts, t)
	}

	return starts
}

// start gives a task to an agent: a new one, or the one it had before,
// resumed with its conversation.
func (s *Scheduler) start(t *Task) {
	s.mu.Lock()
	c := *t
	s.mu.Unlock()

	prompt, seen, err := s.prompt(c)
	if err == nil {
		var info agent.Info

		info, err = s.run(c, prompt)
		if err == nil {
			s.mu.Lock()
			now := time.Now()
			t.AgentID, t.TurnBase, t.Report, t.Error, t.FollowUp, t.Stopped = info.ID, info.Turns, "", "", false, false
			t.Attempts++
			t.StartedAt, t.FinishedAt = &now, nil

			if seen > 0 {
				t.Seen = seen
			}

			s.logf(t, "交给 agent %s", info.ID)
			s.mu.Unlock()

			return
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if errors.Is(err, ErrNotPending) {
		s.finish(t, Done, "claude-bot 里已经处理完")

		return
	}

	t.State, t.Detail, t.Error = Queued, "启动失败，稍后重试", err.Error()
	t.NextTry = time.Now().Add(retryAfter)
	s.logf(t, "启动失败: %v", err)
}

// run starts the agent: live and idle, it gets the prompt as a message;
// ended, it resumes; none, a new one starts.
func (s *Scheduler) run(t Task, prompt string) (agent.Info, error) {
	if t.AgentID != "" {
		if t.Stopped {
			s.o.Agents.WaitAgent(t.AgentID, 10*time.Second)
		}

		info, err := s.o.Agents.AgentInfo(t.AgentID)
		if err == nil {
			if info.Status == agent.Idle {
				if err := s.o.Agents.SendText(t.AgentID, prompt); err != nil {
					return agent.Info{}, err
				}

				return info, nil
			}

			if !info.Status.Live() && info.SessionID != "" {
				return s.o.Agents.Resume(t.AgentID, prompt)
			}

			if info.Status.Live() {
				return agent.Info{}, fmt.Errorf("agent %s is %s", t.AgentID, info.Status)
			}
		}
	}

	return s.o.Agents.Spawn(agent.SpawnOptions{Name: t.Title, Cwd: t.Cwd, TaskID: t.ID, Prompt: prompt})
}

// prompt is the message that hands the task to its agent, and for a
// claude-bot task how many of its entries that message shows.
func (s *Scheduler) prompt(t Task) (string, int, error) {
	var body string

	seen := 0

	switch {
	case t.Source == SourceClaudeBot:
		bt, err := s.o.Bot.Take(t.Cwd, t.External)
		if err != nil {
			return "", 0, err
		}

		seen = bt.Entries

		if t.AgentID == "" {
			body = botFirst(bt.Line)
		} else {
			body = botMore(bt.Line)
		}
	case t.AgentID != "":
		body = "上次这个任务没有完成，请接着做完：\n\n" + t.Prompt
	default:
		body = t.Prompt
	}

	return body + s.footer(t.ID), seen, nil
}

func botFirst(line []byte) string {
	return "你是 claude-bot 值守的子 Agent，只处理下面这一件事（task）。先读这个仓库的 `.claude/commands/bot.md`，" +
		"按其中「子 Agent 的任务说明」「改代码：一律走 PR」「处理 issue」「处理 PR」「发版」「原则」各节来做；" +
		"主会话调度、`/loop`、`inbox --wait` 那些不用管，调度由外面负责。用这件事的 id 运行 `claude-bot reply` 回复。\n\n" +
		"这件事的 JSON：\n```json\n" + string(line) + "\n```"
}

func botMore(line []byte) string {
	return "这件事又有了新消息（entries 里 `new: true` 的是还没处理的）。按同样的要求处理，用 `claude-bot reply` 回复：\n\n" +
		"```json\n" + string(line) + "\n```"
}

func (s *Scheduler) footer(id string) string {
	r := s.o.Report + " " + id

	return "\n\n---\n[rla] 这个任务的 ID 是 " + id + "。做完后运行 `" + r + " done \"<一句话结果>\"`；" +
		"需要人确认或补充信息时运行 `" + r + " need-input \"<要问的问题>\"`，然后停下等回复；" +
		"做不了运行 `" + r + " failed \"<原因>\"`。"
}

// pollBots turns new claude-bot tasks into queued tasks, and new messages
// on known ones into follow-ups.
func (s *Scheduler) pollBots() {
	s.mu.Lock()

	var repos []string

	for _, r := range s.repos {
		if r.ClaudeBot {
			repos = append(repos, r.Path)
		}
	}
	s.mu.Unlock()

	for _, repo := range repos {
		pending, err := s.o.Bot.Pending(repo)

		s.mu.Lock()

		if r, ok := s.repos[repo]; ok {
			r.Error = ""
			if err != nil {
				r.Error = err.Error()
			}
		}

		for _, bt := range pending {
			s.botTask(repo, bt)
		}
		s.mu.Unlock()
	}
}

// botTask applies one pending claude-bot task. Call it with the lock held.
func (s *Scheduler) botTask(repo string, bt BotTask) {
	var t *Task

	for _, c := range s.tasks {
		if c.Source == SourceClaudeBot && c.External == bt.ID && c.Cwd == repo {
			if t == nil || c.CreatedAt.After(t.CreatedAt) {
				t = c
			}
		}
	}

	if t == nil {
		s.add(Task{Title: bt.Title, Cwd: repo, Source: SourceClaudeBot, External: bt.ID})
		s.changedLater()

		return
	}

	if bt.Entries <= t.Seen || t.State == Queued || t.State == Cancelled {
		return
	}

	switch t.State {
	case Running, Blocked:
		// Its agent is busy; the news waits for the turn to end.
		if !t.FollowUp {
			t.FollowUp = true
			s.logf(t, "有新消息，等 agent 这一回合结束")
		}
	default:
		// Waiting, done or failed: give the news to the same agent,
		// resumed if it ended, so it keeps the conversation.
		t.State, t.Detail, t.Report, t.NextTry, t.UpdatedAt = Queued, "有新消息", "", time.Time{}, time.Now()
		s.logf(t, "有新消息，重新排队")
	}

	s.changedLater()
}

// NewTask is a task a person adds.
type NewTask struct {
	Title  string `json:"title"`
	Prompt string `json:"prompt"`
	Cwd    string `json:"cwd"`
}

// Add queues a task.
func (s *Scheduler) Add(n NewTask) (Task, error) {
	if n.Prompt == "" {
		return Task{}, errors.New("prompt is empty")
	}

	cwd, err := filepath.Abs(n.Cwd)
	if err != nil || n.Cwd == "" {
		return Task{}, fmt.Errorf("bad folder %q", n.Cwd)
	}

	if n.Title == "" {
		n.Title = clip(firstLine(n.Prompt), 60)
	}

	s.mu.Lock()
	t := s.add(Task{Title: n.Title, Prompt: n.Prompt, Cwd: cwd, Source: SourceManual})
	c := *t
	s.mu.Unlock()

	s.changed()
	s.Kick()

	return c, nil
}

func (s *Scheduler) add(t Task) *Task {
	now := time.Now()
	t.ID = newTaskID(now)
	t.State, t.CreatedAt, t.UpdatedAt = Queued, now, now
	s.logf(&t, "收到任务")
	s.tasks[t.ID] = &t
	s.Kick()

	return &t
}

// Report records what an agent says about its task. The task moves on once
// the agent's turn ends.
func (s *Scheduler) Report(id, status, summary string) error {
	switch status {
	case ReportDone, ReportFailed, ReportNeedInput:
	default:
		return fmt.Errorf("unknown report %q: use done, failed or need-input", status)
	}

	s.mu.Lock()

	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()

		return fmt.Errorf("no task %s", id)
	}

	t.Report, t.Summary, t.UpdatedAt = status, clip(summary, 2000), time.Now()
	s.logf(t, "agent 报告 %s: %s", status, clip(summary, 200))
	s.mu.Unlock()

	s.changed()
	s.Kick()

	return nil
}

// Cancel stops a task and its agent.
func (s *Scheduler) Cancel(id string) error {
	return s.update(id, func(t *Task) error {
		if t.State.Final() {
			return fmt.Errorf("task is %s", t.State)
		}

		if t.State.Active() && t.AgentID != "" {
			s.stopAgent(t)
		}

		s.finish(t, Cancelled, "手动取消")

		return nil
	})
}

// Retry queues an ended or waiting task again. Its agent goes on with its
// conversation.
func (s *Scheduler) Retry(id string) error {
	return s.update(id, func(t *Task) error {
		if t.State == Queued || t.State == Running || t.State == Blocked {
			return fmt.Errorf("task is %s", t.State)
		}

		// A waiting task's agent is alive and idle: it gets the message
		// itself. An ended one resumes.
		t.State, t.Detail, t.Report, t.Error, t.NextTry, t.FinishedAt = Queued, "重试", "", "", time.Time{}, nil
		s.logf(t, "重试")

		return nil
	})
}

// Finish marks a task done by hand, e.g. a waiting one a person checked.
func (s *Scheduler) Finish(id string) error {
	return s.update(id, func(t *Task) error {
		if t.State.Final() {
			return fmt.Errorf("task is %s", t.State)
		}

		if t.State.Active() && t.AgentID != "" {
			s.stopAgent(t)
		}

		s.finish(t, Done, "手动标记完成")

		return nil
	})
}

// Remove forgets an ended task.
func (s *Scheduler) Remove(id string) error {
	s.mu.Lock()

	t, ok := s.tasks[id]

	switch {
	case !ok:
		s.mu.Unlock()

		return fmt.Errorf("no task %s", id)
	case !t.State.Final():
		s.mu.Unlock()

		return fmt.Errorf("task is %s", t.State)
	}

	delete(s.tasks, id)
	s.mu.Unlock()

	s.changed()

	return nil
}

func (s *Scheduler) update(id string, f func(t *Task) error) error {
	s.mu.Lock()

	t, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()

		return fmt.Errorf("no task %s", id)
	}

	err := f(t)
	t.UpdatedAt = time.Now()
	s.mu.Unlock()

	if err == nil {
		s.changed()
		s.Kick()
	}

	return err
}

// SetRepo adds or changes a repo.
func (s *Scheduler) SetRepo(r Repo) (Repo, error) {
	path, err := filepath.Abs(r.Path)
	if err != nil || r.Path == "" {
		return Repo{}, fmt.Errorf("bad folder %q", r.Path)
	}

	r.Path, r.Error = path, ""
	if r.Concurrency <= 0 {
		r.Concurrency = 1
	}

	s.mu.Lock()
	s.repos[path] = &r
	s.mu.Unlock()

	s.changed()
	s.Kick()

	return r, nil
}

// RemoveRepo forgets a repo; its tasks stay.
func (s *Scheduler) RemoveRepo(path string) error {
	s.mu.Lock()

	if _, ok := s.repos[path]; !ok {
		s.mu.Unlock()

		return fmt.Errorf("no repo %s", path)
	}

	delete(s.repos, path)
	s.mu.Unlock()

	s.changed()

	return nil
}

func (s *Scheduler) sorted() []*Task {
	tasks := make([]*Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}

	slices.SortFunc(tasks, func(a, b *Task) int { return a.CreatedAt.Compare(b.CreatedAt) })

	return tasks
}

func (s *Scheduler) logf(t *Task, format string, args ...any) {
	t.Log = append(t.Log, LogLine{At: time.Now(), Text: fmt.Sprintf(format, args...)})
	if over := len(t.Log) - 100; over > 0 {
		t.Log = t.Log[over:]
	}
}

func (s *Scheduler) changed() {
	if s.o.OnChange != nil {
		s.o.OnChange()
	}
}

func (s *Scheduler) changedLater() {
	if s.o.OnChange != nil {
		go s.o.OnChange()
	}
}

var taskSeq atomic.Int64

func newTaskID(now time.Time) string {
	return "t" + strconv.FormatInt(now.UnixMilli(), 36) + strconv.FormatInt(taskSeq.Add(1)%36, 36)
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}
