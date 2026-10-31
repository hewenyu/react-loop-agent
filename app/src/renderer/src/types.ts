// Mirrors the daemon's JSON (internal/agent, internal/sched, internal/api).
import type { RlaApi } from "../../preload";

export type AgentStatus = "starting" | "working" | "blocked" | "idle" | "exited" | "lost";

export type Agent = {
  id: string;
  name: string;
  cwd: string;
  task_id?: string;
  session_id: string;
  transcript?: string;
  status: AgentStatus;
  detail?: string;
  title?: string;
  turns: number;
  last_message?: string;
  pid?: number;
  exit_code?: number;
  cols: number;
  rows: number;
  created_at: string;
  updated_at: string;
};

export type TaskState = "queued" | "running" | "blocked" | "waiting" | "done" | "failed" | "cancelled";

export type TaskSource = "manual" | "claude-bot";

export type LogLine = { at: string; text: string };

export type Task = {
  id: string;
  title: string;
  prompt?: string;
  cwd: string;
  source: TaskSource;
  external?: string;
  seen?: number;
  state: TaskState;
  agent_id?: string;
  turn_base: number;
  report?: string;
  summary?: string;
  detail?: string;
  error?: string;
  attempts: number;
  follow_up?: boolean;
  stopped?: boolean;
  next_try?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
  log?: LogLine[];
};

export type Repo = {
  path: string;
  claude_bot: boolean;
  concurrency: number;
  error?: string;
};

// State is what GET /api/state and the "state" event carry. Go encodes
// empty lists as null.
export type State = {
  repos: Repo[] | null;
  tasks: Task[] | null;
  agents: Agent[] | null;
};

// Messages on /api/events.
export type Event = { type: "state"; state: State };

declare global {
  interface Window {
    rla: RlaApi;
  }
}
