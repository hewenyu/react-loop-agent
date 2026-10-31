import { useEffect, useId, useState } from "react";
import { call, message } from "./api";
import { REPO_CONCURRENCY_MAX, REPO_CONCURRENCY_MIN } from "./constants";
import { base } from "./format";
import type { Repo } from "./types";

// NewTask queues a task: a folder and what to do there.
export function NewTask({ repos, onClose }: { repos: Repo[]; onClose: () => void }) {
  const [cwd, setCwd] = useState(repos[0]?.path ?? "");
  const [title, setTitle] = useState("");
  const [prompt, setPrompt] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);
  const id = useId();
  const canSubmit = !busy && !!cwd && !!prompt.trim();

  // Esc closes the dialog, as clicking outside it does.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const pick = async () => {
    try {
      const p = await window.rla.pickFolder();
      if (p) setCwd(p);
    } catch (e) {
      setErr(message(e));
    }
  };

  const submit = async () => {
    if (!canSubmit) return;
    setBusy(true);
    setErr("");
    try {
      await call("POST", "/api/tasks", { cwd, title, prompt });
      onClose();
    } catch (e) {
      setErr(message(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-back" onMouseDown={onClose}>
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={`${id}-head`}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <h2 id={`${id}-head`}>新任务</h2>
        <label htmlFor={`${id}-cwd`}>目录</label>
        <div className="row">
          <select id={`${id}-cwd`} value={repos.some((r) => r.path === cwd) ? cwd : ""} onChange={(e) => setCwd(e.target.value)}>
            {!repos.some((r) => r.path === cwd) && <option value="">{cwd || "选择目录…"}</option>}
            {repos.map((r) => (
              <option key={r.path} value={r.path}>
                {base(r.path)} — {r.path}
              </option>
            ))}
          </select>
          <button className="ghost" onClick={() => void pick()}>
            选择…
          </button>
        </div>
        <label htmlFor={`${id}-title`}>标题（可选）</label>
        <input id={`${id}-title`} value={title} onChange={(e) => setTitle(e.target.value)} placeholder="不填就用描述的第一行" />
        <label htmlFor={`${id}-prompt`}>要做什么</label>
        <textarea
          id={`${id}-prompt`}
          className="tall"
          autoFocus
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          placeholder="像交代给同事一样写清楚：目标、范围、怎么算完成。⌘+回车提交"
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              void submit();
            }
          }}
        />
        {err && <div className="card-note bad">{err}</div>}
        <div className="row end">
          <button className="ghost" onClick={onClose}>
            取消
          </button>
          <button className="primary" disabled={!canSubmit} onClick={() => void submit()}>
            排队
          </button>
        </div>
      </div>
    </div>
  );
}

// Repos lists the folders tasks run in, how many may run at once in each,
// and which take their tasks from claude-bot.
export function Repos({ repos }: { repos: Repo[] }) {
  const [err, setErr] = useState("");

  const save = async (r: Repo) => {
    setErr("");
    try {
      await call("POST", "/api/repos", r);
    } catch (e) {
      setErr(message(e));
    }
  };

  const add = async () => {
    setErr("");
    try {
      const p = await window.rla.pickFolder();
      // Adding a repo again would reset its settings.
      if (p && !repos.some((r) => r.path === p)) {
        await save({ path: p, claude_bot: false, concurrency: REPO_CONCURRENCY_MIN });
      }
    } catch (e) {
      setErr(message(e));
    }
  };

  const remove = async (path: string) => {
    setErr("");
    try {
      await call("DELETE", `/api/repos?path=${encodeURIComponent(path)}`);
    } catch (e) {
      setErr(message(e));
    }
  };

  const reveal = async (path: string) => {
    setErr("");
    try {
      const msg = await window.rla.reveal(path);
      if (msg) setErr(msg);
    } catch (e) {
      setErr(message(e));
    }
  };

  // The field is the daemon's value: a cleared or out-of-range entry is
  // saved as the nearest allowed number.
  const setConcurrency = (r: Repo, v: string) => {
    const n = Math.round(Number(v));
    if (!v || !Number.isFinite(n)) return;
    const c = Math.min(REPO_CONCURRENCY_MAX, Math.max(REPO_CONCURRENCY_MIN, n));
    if (c !== r.concurrency) void save({ ...r, concurrency: c });
  };

  return (
    <div className="repos">
      <div className="repos-head">
        <h2>仓库</h2>
        <button className="primary" onClick={() => void add()}>
          + 添加仓库
        </button>
      </div>
      <p className="muted">
        同一个仓库里的 agent 共用一份代码，默认一次只跑一个任务。打开「claude-bot」的仓库，会从它的
        <code> .claude-bot/inbox </code>
        取任务，每件事交给一个 agent，回复走 <code>claude-bot reply</code>。这时不要再在那个仓库里跑 <code>/loop /bot</code>。
      </p>
      {err && <div className="banner bad">{err}</div>}
      {repos.length === 0 && <div className="empty small">还没有仓库。</div>}
      {repos.map((r) => (
        <div className="repo" key={r.path}>
          <div className="repo-main">
            <b>{base(r.path)}</b>
            <span className="muted mono small">{r.path}</span>
            {r.error && <span className="card-note bad">{r.error}</span>}
          </div>
          <label className="toggle">
            <input type="checkbox" checked={r.claude_bot} onChange={(e) => void save({ ...r, claude_bot: e.target.checked })} />
            claude-bot
          </label>
          <label className="num">
            同时
            <input
              type="number"
              min={REPO_CONCURRENCY_MIN}
              max={REPO_CONCURRENCY_MAX}
              value={r.concurrency}
              onChange={(e) => setConcurrency(r, e.target.value)}
            />
            个
          </label>
          <button className="ghost" onClick={() => void reveal(r.path)}>
            打开
          </button>
          <button className="ghost danger" onClick={() => void remove(r.path)}>
            移除
          </button>
        </div>
      ))}
    </div>
  );
}
