# 设计与实现

面向想读懂/改代码的人。用法见 [README](../README.md)。

## 组成

| 部分 | 作用 |
|---|---|
| `cli/` → `awake.exe` | 命令行：申请/释放/查询；同时内含守护进程（`supervisor`）与巡检入口（`-watchdog`） |
| `panel/` → `panel.exe` | 托盘 + 任务栏任务胶囊（原生绘制）+ 面板（mygo + 内嵌 HTML/CSS） |
| `%LOCALAPPDATA%\AgentAwake\` | 运行时数据：租约、runtime.json、panel.json、日志 |

无硬编码路径：命令行只认「自己所在的目录」，数据只认 `%LOCALAPPDATA%`，
面板找命令行的方式是「同目录的 `awake.exe`」→「上一级的 `awake.exe`」→ `PATH`。
整个目录搬到任何位置都能直接运行。

## 租约模型

```
awake acquire ──写──> leases\<id>.json
                          │
                   awake.exe supervisor（单实例互斥体）
                          │
        ┌─────────────────┴──────────────────┐
   有活跃租约                           没有活跃租约
   睡眠/休眠 = 从不                     恢复申请前保存的原值
   SetThreadExecutionState              释放唤醒锁
   每 5 分钟自愈一次                    空闲 90 秒后自己退出
```

租约两种模式：

- **release**（agent 用）：等调用方释放、绑定的进程退出，或心跳超时（`-ttlseconds`）。
- **deadline**（手动/extend）：到点自动结束。

失效判定在 `cli/lease.go` 的 `inactiveReason`，面板侧同一套规则在
`panel/status.go` 的 `evaluateLease`（两处保持对齐，`pidStart` 防止 PID 复用误判）。

三层保障：

1. **守护进程**：真正执行者，持有系统唤醒锁。
2. **巡检计划任务**（`AgentAwake-Watchdog`，每 5 分钟，`awake.exe -watchdog`，任务属性 Hidden）：发现「有租约但没有守护进程」就重新拉起。
3. **租约兜底**：agent 租约超过上限自动失效（默认 12 小时，面板可调 1–24）。

## 电源设置：读注册表、写 powercfg

- **读**直接读注册表（`PowerSchemes` 下 AC/DC SettingIndex，微秒级），面板轮询不启动任何子进程。
- **写**只调用系统 `powercfg.exe`（隐藏窗口），语义与「设置 → 电源」一致。
- 进入保持前把睡眠/休眠原值快照进 `runtime.json`（含电源方案 GUID）；**恢复时写回当初那个方案**——用户中途换方案会被守护进程发现并迁移（`cli/supervisor.go`）。
- **熄屏超时全程不动**：屏幕该黑就黑。

## 线程与并发约定（panel）

- mygo 绑定的 Api 方法在各自 goroutine 上执行；`app` 状态只在主线程读写，
  所有入口经过 `onUI`（`panel/webapi.go`）。`onUI` 依赖 mygo `RunOnMain`
  在主线程重入时直接执行的语义——不要在主线程回调里再包一层会等待自己的调用。
- 后台 goroutine 改 UI 状态一律 `a.update(fn)`（内部 `RunOnMain` + 重绘）。
- 守护进程 `runtime.LockOSThread` 钉住 `SetThreadExecutionState`（按线程生效）。

## 任务栏胶囊：原生 ui 绘制

`panel/barnative.go`。窗口真透明、圆角真镂空；保持中泛绿并连续呼吸
（余弦 1.6s 周期），跑在 `Painter.AnimationFrameRate(20)` 的**只重绘、不重建**
绘制帧上：每帧只画一个半透明绿色圆角矩形，文字排版不重来。
2 分钟没有键鼠输入（`GetLastInputInfo`）就停止请求绘制帧，任何输入 300ms 内恢复。

窗口默认覆盖在任务栏空白区上方（topmost）；`AGENTAWAKE_EMBED=1` 可实验性
`SetParent` 嵌进 `Shell_TrayWnd`（失去逐像素透明，退回采样任务栏底色）。
explorer 重启后自动重挂，失败退回覆盖模式（`panel/embed.go`）。

## 性能

面板**不轮询子进程**：状态直接读租约 JSON、`runtime.json` 和注册表。
旧 PowerShell 实现每 5 秒 spawn 两次 `pwsh`（单核约 73%），现在空闲时几乎为零。

| 指标 | 实测（本机 125% 缩放） |
|---|---|
| 空闲 CPU（托盘 + 胶囊） | 计时粒度内为 0（30 秒 15 ms） |
| 保持中 CPU（胶囊呼吸） | ≈17–34 ms/s（20 fps 只重绘不重建） |
| 呼吸关掉时 | ≈1.5 ms/s（基本为 0） |
| 保持中但 2 分钟无键鼠输入 | ≈3 ms/s（呼吸帧自动停） |
| 状态轮询产生的子进程 | 0（只有点按钮/agent 调用时才跑 `awake.exe`） |
| `panel.exe` 内存（托盘 + 胶囊） | ≈85–88 MB 工作集 / ≈45–48 MB 私有工作集 |
| WebView2 | 胶囊常驻 **0 个进程**；只有打开面板才启动（约 150 MB 私有） |

## 日志

单文件追加 `%LOCALAPPDATA%\AgentAwake\awake.log`，一行一条
`时间  [来源]  内容`，来源有 `awake`（命令行）、`supervisor`（守护进程）、
`panel`（托盘/面板）。超过 1 MiB 轮转成 `.old`，最多占 2 MiB。

**面板「日志」页**（`panel/logview.go`）读同一份文件的尾部（只 seek 末尾 128 KiB，
大文件也是常数开销），最新在前、按来源着色，失败类与「原因=max-lifetime /
heartbeat-timeout」标琥珀；每行悬浮可复制整行。日志页可见时随 1 秒轮询刷新，
面板动作/CLI 动作发生后一秒内可见。租约结束的事件行带 `持续=`（申请到结束的时长），
「手动停止」逐条记录，便于回答「谁在什么时候停了什么」。

租约的 key 会写进日志：它本来就在 `leases\*.json` 里（同目录、同权限），
日志没有扩大暴露面。但日志仍属敏感信息，**不要贴给 agent、也不要贴进 issue**。

**结构化历史账本**：`history.jsonl`（`cli/history.go`）在每张租约结束时追加一行
（申请/结束时间、持续、结束原因），供 `awake history` 查询；与日志同目录、同权限，
上限 500 条自动修剪。

## 版本号

发布构建由 CI 用 `-ldflags "-X main.version=vX.Y.Z"` 注入（两个 exe 同一版本）；
本地 `go build` 是 `dev`，面板右上角退化为可执行文件时间戳。

## 不做的事

- **不改熄屏超时**：屏幕该黑就黑，只是系统不睡。
- **不改合盖动作**：合盖仍按系统设置睡眠。
- **不强制阻止 Windows 更新重启**：若系统重启，巡检任务会在几分钟内重新拉起守护进程。

## 卸载

```powershell
awake stop
schtasks /Delete /TN AgentAwake-Watchdog /F
Remove-Item "$env:LOCALAPPDATA\AgentAwake" -Recurse -Force
```
