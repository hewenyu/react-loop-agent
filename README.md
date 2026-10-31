# react-loop-agent (rla)

**把 ReAct 循环放到 Agent 外面：调度用普通代码做，Agent 只管把一件事做好。**

`claude-bot` 用 `/loop /bot` 让一个 Claude 会话自己收消息、开子 Agent、等下一轮。这个「调度员」本身是 LLM：每次醒来都要重读说明、拉取 inbox、派任务，上下文越积越长，任务也只能串行。

rla 把这一层换成一个 Go 写的 daemon：它负责排队、派活、看状态、处理追问和重启恢复；每个任务交给一个**交互式** Claude Code，跑在 daemon 自己管理的终端里。桌面 APP（Electron + TS）能看到每个 Agent 的真实终端，随时接管。

```
 新任务（APP / rla task add）     claude-bot 的 inbox（聊天、GitHub，经 Jev 筛过）
              │                                │
              ▼                                ▼
       ┌────────────────────── rla daemon (Go) ──────────────────────┐
       │ 调度器：按仓库并发上限派活、追问并回原 Agent、重启后恢复会话    │
       │ 终端：每个 Agent 一个 PTY，输出留 4MB 供回放                    │
       │ 状态：Claude Code hooks → working / blocked / idle            │
       └──────┬───────────────────────────────┬──────────────────────┘
              │ PTY                           │ HTTP + WebSocket（仅本机，带 token）
              ▼                               ▼
   claude --settings <hooks> --session-id …   Electron APP：看板、实时终端、授权、通知
     └─ hook → `rla hook` → daemon
     └─ 做完 → `rla report <task> done "…"`
```

## 状态从哪来

不靠猜屏幕。daemon 启动 Agent 时用 `--settings` 注入 hooks（不改你的 `~/.claude/settings.json`），每个 hook 调 `rla hook` 把事件报给 daemon：

| 事件 | 状态 |
|---|---|
| `SessionStart` | idle（就绪） |
| `UserPromptSubmit` / `PreToolUse` / `PostToolUse` | working，附带正在跑的工具，如 `Bash: go test ./...` |
| `PermissionRequest` | blocked，附带要授权的命令 |
| `Stop` | idle，回合数 +1，记下最后一句话 |

hooks 管不到的三种情况，用终端兜底，都有测试：

- **启动前的对话框**（比如「信任这个目录」）：还没有任何 hook，输出里出现 `Esc to cancel` → blocked。
- **按 Esc 中断**：没有 `Stop`；标题从转圈 `◐` 变成 `✳` 且 2 秒内没有 hook → idle。
- **拒绝授权**：没有 hook；有人在授权框里按了键、之后 2 秒没再画出授权框 → idle。

任务的完成由 Agent 自己报告（`rla report <task> done|failed|need-input "…"`，已在注入的设置里免授权），在它这一回合结束时生效。没报告就结束回合的，任务进「需要你」，不会被当成完成。

## 快速开始

需要 Go 1.27+、Node 22+、[Claude Code](https://claude.com/claude-code)。

```bash
make build        # bin/rla
make app-dev      # 启动桌面 APP；没有 daemon 时它会用登录 shell 启动 bin/rla daemon
```

APP 关掉，daemon 和 Agent 继续跑；再打开就接上。不用 APP 也可以：

```bash
rla daemon &                                  # 或者让 APP 启动它
rla repo add -concurrency 1 ~/code/myproj
rla task add -C ~/code/myproj "修好 make test 里失败的用例，补上测试"
rla ls                                        # 任务和 Agent
rla read <agent>                              # 终端输出
rla keys <agent> down enter                   # 按键，比如回答对话框
rla task retry|cancel|finish <task>
rla stop                                      # 停 daemon（Agent 下次启动时自动恢复会话）
```

## 接 claude-bot

在 APP 的「仓库」里打开某个仓库的 claude-bot 开关（或 `rla repo add -claude-bot <dir>`）。之后：

- gateway 照常收消息、用 Jev 归组，写进仓库的 `.claude-bot/inbox/`；rla 每 3 秒看一次，有新的事就排队。
- 派活时运行 `claude-bot inbox` 取这件事（保持它的 `Shown`/`Close` 语义），交给一个新 Agent，按仓库里 `bot.md` 的子 Agent 说明处理，用 `claude-bot reply` 回复。
- **同一件事的追问交给同一个 Agent**：它还在就直接发过去，已经结束就 `--resume` 原会话，上下文一直在。
- 回复关掉这件事后，任务完成，Agent 退出。

这时不要再在那个仓库里跑 `/loop /bot`，否则两边会抢同一件事。

## 配置

`~/.rla/config.json`（APP 启动 daemon 时不带参数，靠它；`rla daemon` 的参数可以覆盖）：

```json
{
  "agent": "claude",
  "agent_args": ["--permission-mode", "acceptEdits"],
  "claude_bot": "claude-bot"
}
```

`~/.rla/` 下还有 `state.json`（任务、仓库、Agent，重启后接着用）、`daemon.json`（地址和 token，0600）、`daemon.log`。`RLA_HOME` 可以换目录。

## 重启和恢复

- APP 关掉：什么都不影响。
- daemon 重启：正在跑的 Agent 随之结束，标为 lost；对应任务自动重新排队，用 `claude --resume <session>` 接着做（最多 3 次）。
- 刚结束又要接着做（重试、追问）：等原进程完全退出再 resume，不会把消息打进一个正在关闭的终端。

## 代码

```
cmd/rla/           命令行和 daemon 入口
internal/agent/    PTY、hooks 注入、状态机（status.go 是纯函数，测试最全）
internal/sched/    调度器、claude-bot inbox
internal/api/      HTTP + WebSocket（状态推送、终端流）
internal/term/     输出回放缓冲、标题解析、终端查询过滤
internal/store/    ~/.rla 下的文件
app/               Electron（main / preload / renderer：React + xterm.js）
```

```bash
make test          # go test -race ./...
cd app && npm run typecheck
```

## 还没做的

- 每个任务一个 git worktree：现在同一仓库的 Agent 共用工作区，所以默认并发 1。
- 打包成 `.app`（electron-builder，把 `bin/rla` 放进 resources）。
- daemon 重启后 Agent 进程不保留（herdr-core 用独立的 holder 进程做到了），现在靠 resume 恢复。
- 屏幕回放用的是原始输出：窗口尺寸差很多时，旧内容可能错位，Claude 收到 resize 后会重画当前画面。
- 花费统计：hook 里有 `transcript_path`，可以从会话记录里算出每个任务用了多少 token。
