import { useState } from "react";
import type { KeyboardEvent } from "react";
import { call, message } from "./api";
import { BOARD_ENDED_TASKS } from "./constants";
import { agentLabel, ago, base, final as isFinal, taskLabel, time, tone } from "./format";
import type { Agent, Task, TaskState } from "./types";

// Actions on a task (internal/api taskAction), and removing it.
type TaskAction = "cancel" | "retry" | "finish" | "remove";

// ended columns show the latest finished first, and only so many.
const columns: { title: string; hint: string; states: TaskState[]; ended?: boolean }[] = [
  { title: "排队", hint: "等空出的位置", states: ["queued"] },
  { title: "进行中", hint: "agent 正在做", states: ["running"] },
  { title: "需要你", hint: "授权、回答、或检查结果", states: ["blocked", "waiting"] },
  { title: "已结束", hint: `最近 ${BOARD_ENDED_TASKS} 个`, states: ["done", "failed", "cancelled"], ended: true },
];

export function Board(p: { tasks: Task[]; agents: Map<string, Agent>; onAgent: (id: string) => void; onNew: () => void }) {
  if (p.tasks.length === 0) {
    return (
      <div className="empty">
        <p>还没有任务。</p>
        <p className="muted">新建一个任务，或在「仓库」里接上 claude-bot，聊天和 GitHub 里的事会自动进来。</p>
        <button className="primary" onClick={p.onNew}>
          + 新任务
        </button>
      </div>
    );
  }

  return (
    <div className="board">
      {columns.map((c) => {
        let list = p.tasks.filter((t) => c.states.includes(t.state));
        list = c.ended
          ? list
              .sort((a, b) => time(b.finished_at ?? b.updated_at) - time(a.finished_at ?? a.updated_at))
              .slice(0, BOARD_ENDED_TASKS)
          : list.sort((a, b) => time(a.created_at) - time(b.created_at));
        return (
          <div key={c.title} className="column">
            <div className="column-head">
              <b>{c.title}</b>
              <span className="count">{list.length}</span>
              <span className="muted small">{c.hint}</span>
            </div>
            <div className="cards">
              {list.map((t) => (
                <TaskCard key={t.id} t={t} agent={t.agent_id ? p.agents.get(t.agent_id) : undefined} onAgent={p.onAgent} />
              ))}
            </div>
          </div>
        );
      })}
    </div>
  );
}

function TaskCard({ t, agent, onAgent }: { t: Task; agent?: Agent; onAgent: (id: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState("");

  const act = async (action: TaskAction) => {
    setBusy(true);
    setErr("");
    try {
      if (action === "remove") await call("DELETE", `/api/tasks/${encodeURIComponent(t.id)}`);
      else await call("POST", `/api/tasks/${encodeURIComponent(t.id)}/${action}`);
    } catch (e) {
      setErr(message(e));
    } finally {
      setBusy(false);
    }
  };

  const final = isFinal(t.state);
  const note = t.state === "failed" ? t.error || t.detail : t.detail;
  const agentId = t.agent_id;

  // The card opens its agent's terminal, by mouse or keyboard.
  const open = agentId
    ? {
        role: "button",
        tabIndex: 0,
        onClick: () => onAgent(agentId),
        onKeyDown: (e: KeyboardEvent) => {
          if (e.target === e.currentTarget && (e.key === "Enter" || e.key === " ")) {
            e.preventDefault();
            onAgent(agentId);
          }
        },
      }
    : {};

  return (
    <div className={`card ${tone(t.state)}`} {...open}>
      <div className="card-top">
        <span className={`pill ${tone(t.state)}`}>{taskLabel[t.state]}</span>
        {t.source === "claude-bot" && <span className="chip bot">claude-bot</span>}
        <span className="chip">{base(t.cwd)}</span>
        <span className="muted small right">{ago(t.updated_at)}</span>
      </div>
      <div className="card-title">{t.title}</div>
      {note && <div className={`card-note ${tone(t.state)}`}>{note}</div>}
      {t.summary && (t.state === "done" || t.state === "waiting" || t.state === "failed") && (
        <div className="card-summary">{t.summary}</div>
      )}
      {agent && !final && (
        <div className="card-agent">
          <span className={`dot ${tone(agent.status)} ${agent.status === "working" ? "pulse" : ""}`} />
          {agentLabel[agent.status]}
          {agent.status === "working" && agent.detail ? ` · ${agent.detail}` : ""}
          {agent.turns > 0 && <span className="muted"> · 第 {agent.turns} 轮</span>}
        </div>
      )}
      {err && <div className="card-note bad">{err}</div>}
      <div className="card-actions" onClick={(e) => e.stopPropagation()}>
        {agentId && (
          <button className="ghost" onClick={() => onAgent(agentId)}>
            终端
          </button>
        )}
        {t.state === "waiting" && (
          <button className="ghost" disabled={busy} onClick={() => void act("finish")}>
            标记完成
          </button>
        )}
        {(t.state === "waiting" || final) && t.state !== "done" && (
          <button className="ghost" disabled={busy} onClick={() => void act("retry")}>
            {t.state === "waiting" ? "让它继续" : "重试"}
          </button>
        )}
        {!final && (
          <button className="ghost danger" disabled={busy} onClick={() => void act("cancel")}>
            取消
          </button>
        )}
        {final && (
          <button className="ghost" disabled={busy} onClick={() => void act("remove")}>
            移除
          </button>
        )}
      </div>
    </div>
  );
}
