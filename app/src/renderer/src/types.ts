// Mirrors the daemon's JSON (internal/agent, internal/sched, internal/api).

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

export type Task = {
  id: string;
  title: string;
  prompt?: string;
  cwd: string;
  source: "manual" | "claude-bot";
  external?: string;
  state: TaskState;
  agent_id?: string;
  report?: string;
  summary?: string;
  detail?: string;
  error?: string;
  attempts: number;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
  log?: { at: string; text: string }[];
};

export type Repo = {
  path: string;
  claude_bot: boolean;
  concurrency: number;
  error?: string;
};

export type State = {
  repos: Repo[] | null;
  tasks: Task[] | null;
  agents: Agent[] | null;
};

declare global {
  interface Window {
    rla: {
      daemon: () => Promise<{ addr: string; token: string }>;
      pickFolder: () => Promise<string | null>;
      reveal: (p: string) => Promise<string>;
    };
  }
}
