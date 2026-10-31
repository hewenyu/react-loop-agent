import { useState } from "react";
import { call, message } from "./api";
import { KEY_ENTER, KEY_ESC, PERMISSION_DETAIL_PREFIX } from "./constants";
import { agentLabel, base, clock, live as isLive, taskLabel, tone } from "./format";
import { Terminal } from "./Terminal";
import type { Agent, Task } from "./types";

// Actions on an agent (internal/api agentAction), and removing it.
type AgentAction = "send" | "keys" | "interrupt" | "kill" | "resume" | "remove";

// AgentView is one agent: its live terminal, and beside it the task it
// works on and what happened to it. Render it keyed by the agent's id, so
// errors and drafts do not carry over to another agent.
export function AgentView({ agent: a, task }: { agent: Agent; task?: Task }) {
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const live = isLive(a.status);

  const act = async (action: AgentAction, body?: { text: string }) => {
    setBusy(true);
    setErr("");
    try {
      if (action === "remove") await call("DELETE", `/api/agents/${encodeURIComponent(a.id)}`);
      else await call("POST", `/api/agents/${encodeURIComponent(a.id)}/${action}`, body);
    } catch (e) {
      setErr(message(e));
    } finally {
      setBusy(false);
    }
  };

  // Claude's permission prompt has "Yes" selected: Enter allows, Esc
  // declines. Other dialogs (e.g. trusting a folder) default to "No", so
  // they get no shortcut; answer them in the terminal.
  const permission = a.status === "blocked" && a.detail?.startsWith(PERMISSION_DETAIL_PREFIX);

  return (
    <div className="agent-view">
      <div className="agent-head">
        <span className={`pill ${tone(a.status)}`}>{agentLabel[a.status]}</span>
        <b className="agent-head-name">{a.name}</b>
        <span className="muted mono small" title={a.cwd}>
          {base(a.cwd)} · {a.id}
        </span>
        {a.turns > 0 && <span className="muted small">第 {a.turns} 轮</span>}
        <span className="grow" />
        {live && (
          <button className="ghost" disabled={busy} onClick={() => void act("interrupt")} title="按 Esc：停止这一轮，不结束 agent">
            中断
          </button>
        )}
        {live && (
          <button className="ghost danger" disabled={busy} onClick={() => void act("kill")}>
            结束
          </button>
        )}
        {!live && a.session_id && (
          <button className="ghost" disabled={busy} onClick={() => void act("resume")} title={`claude --resume ${a.session_id}`}>
            恢复会话
          </button>
        )}
        {!live && (
          <button className="ghost" disabled={busy} onClick={() => void act("remove")}>
            移除
          </button>
        )}
      </div>

      {a.status === "blocked" && (
        <div className="banner warn">
          <span>⚠ {a.detail || "agent 在等你确认"}</span>
          <span className="grow" />
          {permission ? (
            <>
              <button className="primary" disabled={busy} onClick={() => void act("keys", { text: KEY_ENTER })}>
                允许一次
              </button>
              <button className="ghost" disabled={busy} onClick={() => void act("keys", { text: KEY_ESC })}>
                拒绝
              </button>
            </>
          ) : (
            <span className="muted small">在下面的终端里选择</span>
          )}
        </div>
      )}
      {err && <div className="banner bad">{err}</div>}

      <div className="agent-body">
        <Terminal agentId={a.id} live={live} />
        <TaskPanel agent={a} task={task} />
      </div>
    </div>
  );
}

function TaskPanel({ agent: a, task: t }: { agent: Agent; task?: Task }) {
  const [text, setText] = useState("");
  const [err, setErr] = useState("");
  const [sending, setSending] = useState(false);
  // The daemon only takes a message while the agent is idle.
  const canSend = a.status === "idle" && !!text.trim() && !sending;

  const send = async () => {
    if (!canSend) return;
    setSending(true);
    setErr("");
    try {
      await call("POST", `/api/agents/${encodeURIComponent(a.id)}/send`, { text });
      setText("");
    } catch (e) {
      setErr(message(e));
    } finally {
      setSending(false);
    }
  };

  return (
    <aside className="task-panel">
      {t ? (
        <>
          <div className="panel-title">任务</div>
          <div className="task-name">{t.title}</div>
          <div className="row">
            <span className={`pill ${tone(t.state)}`}>{taskLabel[t.state]}</span>
            {t.source === "claude-bot" && t.external && <span className="chip bot">{t.external}</span>}
            {t.attempts > 1 && <span className="muted small">第 {t.attempts} 次</span>}
          </div>
          {t.summary && (
            <>
              <div className="panel-title">结果</div>
              <div className="summary">{t.summary}</div>
            </>
          )}
          {t.prompt && (
            <details>
              <summary className="panel-title">任务描述</summary>
              <div className="summary">{t.prompt}</div>
            </details>
          )}
          <div className="panel-title">经过</div>
          <ol className="timeline">
            {(t.log ?? []).map((l, i) => (
              <li key={i}>
                <span className="mono muted small">{clock(l.at)}</span> {l.text}
              </li>
            ))}
          </ol>
        </>
      ) : (
        <div className="muted small">这个 agent 不属于任何任务。</div>
      )}

      {a.last_message && (
        <>
          <div className="panel-title">最后一句</div>
          <div className="summary">{a.last_message}</div>
        </>
      )}

      <div className="panel-title">发消息给 agent</div>
      <textarea
        aria-label="发消息给 agent"
        value={text}
        placeholder={a.status === "idle" ? "回车发送，Shift+回车换行" : "agent 空闲时才能发送；也可以直接在终端里打字"}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            void send();
          }
        }}
      />
      <button className="primary" disabled={!canSend} onClick={() => void send()}>
        发送
      </button>
      {err && <div className="card-note bad">{err}</div>}
      {a.session_id && (
        <div className="muted small mono session" title="claude --resume 这个会话">
          session {a.session_id}
        </div>
      )}
    </aside>
  );
}
