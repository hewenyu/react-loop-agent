import type { AgentStatus, TaskState } from "./types";

export function ago(iso?: string): string {
  if (!iso) return "";
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 10) return "刚刚";
  if (s < 60) return `${Math.floor(s)} 秒前`;
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
  return `${Math.floor(s / 86400)} 天前`;
}

export function clock(iso: string): string {
  return new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function base(path: string): string {
  return path.split("/").filter(Boolean).pop() || path;
}

export const agentLabel: Record<AgentStatus, string> = {
  starting: "启动中",
  working: "工作中",
  blocked: "等你处理",
  idle: "空闲",
  exited: "已退出",
  lost: "已中断",
};

export const taskLabel: Record<TaskState, string> = {
  queued: "排队",
  running: "进行中",
  blocked: "等你授权",
  waiting: "等你回复",
  done: "完成",
  failed: "失败",
  cancelled: "已取消",
};

// tone is the color family of a status, shared by tasks and agents.
export function tone(s: AgentStatus | TaskState): "info" | "warn" | "ok" | "bad" | "off" {
  switch (s) {
    case "working":
    case "running":
    case "starting":
      return "info";
    case "blocked":
    case "waiting":
      return "warn";
    case "done":
      return "ok";
    case "failed":
    case "lost":
      return "bad";
    default:
      return "off";
  }
}
