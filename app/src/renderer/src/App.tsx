import { useEffect, useMemo, useRef, useState } from "react";
import { useDaemonState } from "./api";
import { AgentView } from "./AgentView";
import { Board } from "./Board";
import { NewTask, Repos } from "./Forms";
import { agentLabel, base, tone } from "./format";
import type { Agent, State, Task } from "./types";

export type View = { kind: "board" } | { kind: "repos" } | { kind: "agent"; id: string };

export function App() {
  const { state, online } = useDaemonState();
  const [view, setView] = useState<View>({ kind: "board" });
  const [tabs, setTabs] = useState<string[]>([]);
  const [adding, setAdding] = useState(false);

  const agents = state?.agents ?? [];
  const tasks = state?.tasks ?? [];
  const repos = state?.repos ?? [];

  useNotifications(state, (id) => openAgent(id));

  const openAgent = (id: string) => {
    setTabs((t) => (t.includes(id) ? t : [...t, id]));
    setView({ kind: "agent", id });
  };

  const closeTab = (id: string) => {
    setTabs((t) => t.filter((x) => x !== id));
    if (view.kind === "agent" && view.id === id) setView({ kind: "board" });
  };

  // Agents that disappear (removed) close their tabs.
  useEffect(() => {
    if (!state) return;
    const ids = new Set(agents.map((a) => a.id));
    setTabs((t) => t.filter((id) => ids.has(id)));
    if (view.kind === "agent" && !ids.has(view.id)) setView({ kind: "board" });
  }, [state]);

  const byId = useMemo(() => new Map(agents.map((a) => [a.id, a])), [agents]);

  return (
    <div className="app">
      <Sidebar
        agents={agents}
        tasks={tasks}
        online={online}
        view={view}
        onAgent={openAgent}
        onNew={() => setAdding(true)}
        onBoard={() => setView({ kind: "board" })}
        onRepos={() => setView({ kind: "repos" })}
      />
      <main className="main">
        <header className="tabs">
          <button className={`tab ${view.kind === "board" ? "on" : ""}`} onClick={() => setView({ kind: "board" })}>
            任务看板
          </button>
          <button className={`tab ${view.kind === "repos" ? "on" : ""}`} onClick={() => setView({ kind: "repos" })}>
            仓库
          </button>
          {tabs.map((id) => {
            const a = byId.get(id);
            if (!a) return null;
            return (
              <div key={id} className={`tab agent-tab ${view.kind === "agent" && view.id === id ? "on" : ""}`}>
                <button onClick={() => setView({ kind: "agent", id })}>
                  <span className={`dot ${tone(a.status)} ${a.status === "working" ? "pulse" : ""}`} />
                  {a.name}
                </button>
                <button className="x" title="关闭标签（agent 继续运行）" onClick={() => closeTab(id)}>
                  ×
                </button>
              </div>
            );
          })}
        </header>
        <section className="content">
          {!state ? (
            <div className="empty">{online ? "加载中…" : "正在连接 rla daemon…"}</div>
          ) : view.kind === "board" ? (
            <Board tasks={tasks} agents={byId} onAgent={openAgent} onNew={() => setAdding(true)} />
          ) : view.kind === "repos" ? (
            <Repos repos={repos} />
          ) : byId.get(view.id) ? (
            <AgentView agent={byId.get(view.id)!} task={tasks.find((t) => t.agent_id === view.id && t.id === byId.get(view.id)!.task_id)} />
          ) : null}
        </section>
      </main>
      {adding && <NewTask repos={repos} onClose={() => setAdding(false)} />}
    </div>
  );
}

function Sidebar(p: {
  agents: Agent[];
  tasks: Task[];
  online: boolean;
  view: View;
  onAgent: (id: string) => void;
  onNew: () => void;
  onBoard: () => void;
  onRepos: () => void;
}) {
  const count = (f: (t: Task) => boolean) => p.tasks.filter(f).length;
  const live = p.agents.filter((a) => a.status !== "exited");
  const ended = p.agents.filter((a) => a.status === "exited").slice(-8).reverse();
  const order = { blocked: 0, working: 1, starting: 2, idle: 3, lost: 4, exited: 5 } as const;
  live.sort((a, b) => order[a.status] - order[b.status]);

  return (
    <aside className="sidebar">
      <div className="brand">
        <span className="logo">◆</span> RLA 调度台
        <span className={`conn ${p.online ? "ok" : "bad"}`} title={p.online ? "已连接 daemon" : "daemon 未连接"} />
      </div>
      <div className="stats" onClick={p.onBoard}>
        <Stat n={count((t) => t.state === "running")} label="进行中" cls="info" />
        <Stat n={count((t) => t.state === "blocked" || t.state === "waiting")} label="需要你" cls="warn" />
        <Stat n={count((t) => t.state === "queued")} label="排队" cls="off" />
      </div>
      <button className="primary wide" onClick={p.onNew}>
        + 新任务
      </button>

      <div className="side-title">Agents</div>
      <div className="agent-list">
        {live.length === 0 && <div className="side-empty">没有运行中的 agent</div>}
        {live.map((a) => (
          <AgentItem key={a.id} a={a} on={p.view.kind === "agent" && p.view.id === a.id} onClick={() => p.onAgent(a.id)} />
        ))}
        {ended.length > 0 && <div className="side-sub">最近结束</div>}
        {ended.map((a) => (
          <AgentItem key={a.id} a={a} on={p.view.kind === "agent" && p.view.id === a.id} onClick={() => p.onAgent(a.id)} />
        ))}
      </div>

      <button className={`side-link ${p.view.kind === "repos" ? "on" : ""}`} onClick={p.onRepos}>
        ⚙ 仓库与 claude-bot
      </button>
    </aside>
  );
}

function Stat({ n, label, cls }: { n: number; label: string; cls: string }) {
  return (
    <div className={`stat ${cls}`}>
      <b>{n}</b>
      <span>{label}</span>
    </div>
  );
}

function AgentItem({ a, on, onClick }: { a: Agent; on: boolean; onClick: () => void }) {
  return (
    <button className={`agent-item ${on ? "on" : ""} ${a.status}`} onClick={onClick}>
      <span className={`dot ${tone(a.status)} ${a.status === "working" ? "pulse" : ""}`} />
      <span className="agent-main">
        <span className="agent-name">{a.name}</span>
        <span className="agent-sub">
          {agentLabel[a.status]}
          {a.detail ? ` · ${a.detail}` : a.title ? ` · ${a.title}` : ""}
        </span>
      </span>
      <span className="agent-repo">{base(a.cwd)}</span>
    </button>
  );
}

// useNotifications shows a system notification when an agent needs a
// person, or a task ends. It says nothing about what was so on startup.
function useNotifications(state: State | null, open: (agentId: string) => void) {
  const seen = useRef<Map<string, string> | null>(null);

  useEffect(() => {
    if (!state) return;
    const now = new Map<string, string>();
    for (const a of state.agents ?? []) now.set(`a:${a.id}`, a.status);
    for (const t of state.tasks ?? []) now.set(`t:${t.id}`, t.state);

    const prev = seen.current;
    seen.current = now;
    if (!prev) return;

    for (const a of state.agents ?? []) {
      if (a.status === "blocked" && prev.get(`a:${a.id}`) !== "blocked") {
        notify(`${a.name} 需要你处理`, a.detail || "agent 在等你确认", () => open(a.id));
      }
    }
    for (const t of state.tasks ?? []) {
      const was = prev.get(`t:${t.id}`);
      if (was === t.state) continue;
      if (t.state === "done") notify(`完成: ${t.title}`, t.summary || "", () => t.agent_id && open(t.agent_id));
      if (t.state === "failed") notify(`失败: ${t.title}`, t.error || t.detail || "", () => t.agent_id && open(t.agent_id));
      if (t.state === "waiting") notify(`等你回复: ${t.title}`, t.summary || t.detail || "", () => t.agent_id && open(t.agent_id));
    }
  }, [state, open]);
}

function notify(title: string, body: string, onClick: () => void) {
  if (typeof Notification === "undefined") return;
  const n = new Notification(title, { body: body.slice(0, 200), silent: false });
  n.onclick = () => {
    window.focus();
    onClick();
  };
}
