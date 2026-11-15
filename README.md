# AgentAwake

让电脑在 agent 干活期间**不进入睡眠**，屏幕照常自然熄灭。
支持多个 agent 同时申请，**全部完成后**才恢复原来的电源设置。

全部由 Go 实现：一个命令行 `awake.exe`（控制台程序，给 agent 和终端用）、
一个面板 `panel\panel.exe`（GUI 子系统，托盘 + 任务栏任务胶囊 + 面板）。
**不再需要 PowerShell、不需要脚本、不会弹黑窗。**

## 两种保持方式

| 谁用 | 命令 | 结束条件 |
|---|---|---|
| **agent**（推荐） | `awake acquire -name 任务名 -followpid 进程号` | 它主动 `release`，或绑定的进程退出；**另有最长时限兜底** |
| 人 | `awake hold -for 30m` / `-until 06:00` | 到点自动结束 |

agent 模式没有固定时长——干完说一声就释放；兜底上限默认 12 小时（面板里可调 1–24），
防止忘了释放把机器一直钉住。

## 组成

| 部分 | 作用 |
|---|---|
| `awake.exe` | 命令行：申请/释放/查询；同时内含守护进程与巡检入口（`cli\`） |
| `panel\panel.exe` | 托盘 + 任务栏任务胶囊 + 面板（Go + 内嵌 HTML/CSS） |
| `%LOCALAPPDATA%\AgentAwake\` | 运行时数据：租约、runtime.json、panel.json、日志 |

无硬编码路径：命令行只认「自己所在的目录」，数据只认 `%LOCALAPPDATA%`，
面板找命令行的方式是「同目录的 `awake.exe`」→「上一级的 `awake.exe`」→ `PATH`。
整个目录搬到任何位置都能直接运行。

## 命令行

```powershell
awake hold -for 30m            # 手动保持 30 分钟
awake hold -until 06:00        # 保持到 06:00
awake status                   # 查看（-json 给程序用）
awake stop                     # 立刻全部停止并恢复
```

参数大小写不敏感，`-For 30m`、`-for=30m`、`--for 30m` 都可以。

### Agent 接入

```powershell
# 绑定自身进程：进程被杀/崩溃也会自动释放
$lease = awake acquire -name "nightly-build" -followpid $PID -json | ConvertFrom-Json
# ...干活...
awake release -id $lease.id     # 主动通知「我完成了」
```

| 命令 | 作用 |
|---|---|
| `acquire -name x [-followpid n] [-timeouthours 12] [-ttlseconds n] [-json]` | 申请（等完成模式） |
| `release -id x` / `-name x` / `-all` | 通知完成 |
| `extend -id x -for 30m` / `-until 07:00` / `-all` | 改成定时 |
| `heartbeat -id x` | 给带 TTL 的租约续期 |
| `status [-json]` | 查看状态 |
| `stop` | 释放全部并立即恢复 |

时间写法：`30m` `90s` `2h` `1.5d` `30分钟` `2小时` `06:00` `2026-11-15T06:00`

## 界面

- **托盘图标**：只显示品牌图标，左键打开面板，右键「打开面板 / 设置 / 退出」。
- **任务栏任务胶囊**：`● 保持中 租约 N`，叠在任务栏空白区，点击也打开面板；保持中泛绿并呼吸。
- **面板**（420 宽竖屏）：状态页 + 设置页。
  - 状态页：上方固定（状态、三个指标块、手动保持按钮），下方是动态租约列表。
  - 设置页：任务胶囊位置、显示开关、agent 最长保持、呼吸开关。
    窗口高度按内容自适应，不会截断最后一项。
  - 界面是内嵌 HTML/CSS（`go:embed` + `mygo.SetFrontend`），不需要 Bun/npm。
- 标题栏右侧是构建标记（`panel.exe` 的写入时间，如 `1115-2045`），用来确认「你看到的是哪一版」。

## 构建

```bat
build.cmd            :: 一次构建 awake.exe + panel\panel.exe
cli\build.cmd        :: 只构建命令行
panel\build.cmd      :: 只构建面板（-H=windowsgui，运行时不带控制台）
```

## 性能

面板**不轮询子进程**：状态直接读租约 JSON、`runtime.json` 和注册表。

| 指标 | 旧实现（每 5 秒 spawn 2 次 pwsh） | 现在 |
|---|---|---|
| CPU | ≈73% 单核（单次 PowerShell 冷启动实测 1.8 秒） | ≈0.2% 单核 |
| 子进程 | 持续产生 | 0（仅点按钮/agent 调用时） |
| 内存 | 73 MB | 常驻 74 MB；面板打开时约 91 MB（WebView2） |

## 测试

```powershell
cd cli;   go test ./...   # 时间解析、参数解析、租约判定、列宽对齐
cd panel; go test ./...   # 任务胶囊视图、状态 DTO、租约规则
```

## 工作机制

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

三层保障：

1. **守护进程**：真正执行者，持有系统唤醒锁。
2. **巡检计划任务**（`AgentAwake-Watchdog`，每 5 分钟，`awake.exe -watchdog`，任务属性 Hidden）：发现「有租约但没有守护进程」就重新拉起。
3. **租约兜底**：agent 租约超过上限自动失效（默认 12 小时）。

守约进程身份不再靠命令行字符串判断，而是「这个 PID 的镜像路径 == 当前 awake.exe」，
所以换目录、改名都没问题。

## 文件位置

| 路径 | 内容 |
|---|---|
| `%LOCALAPPDATA%\AgentAwake\leases\` | 租约文件，一张一个 |
| `%LOCALAPPDATA%\AgentAwake\runtime.json` | 守护进程状态与原始电源值快照 |
| `%LOCALAPPDATA%\AgentAwake\panel.json` | 界面设置（位置、显示、上限、面板位置） |
| `%LOCALAPPDATA%\AgentAwake\awake.log` | 日志 |

## 不做的事

- **不改熄屏超时**：屏幕该黑就黑，只是系统不睡。
- **不改合盖动作**：合盖仍按系统设置睡眠。
- **不强制阻止 Windows 更新重启**：若系统重启，巡检任务会在几分钟内重新拉起守护进程。

## 图标

`panel/assets/icon-*.png` 由生图流水线（洋红底 + 平涂风格块 + 本地抠图）生成后按尺寸导出，
托盘图标通过 `go:embed` 打进二进制，面板网页用 `panel/web/icon.png`。
换图标：替换这些 PNG 后重新构建。

## 卸载

```powershell
awake stop
schtasks /Delete /TN AgentAwake-Watchdog /F
Remove-Item "$env:LOCALAPPDATA\AgentAwake" -Recurse -Force
```
