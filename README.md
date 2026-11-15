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
| `panel\panel.exe` | 托盘 + 任务栏任务胶囊（原生绘制）+ 面板（Go + 内嵌 HTML/CSS） |
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
awake release -key $lease.key     # 只释放自己的租约（推荐）
# 或：awake release -id $lease.id
```

每次申请都会返回一个 6 位**短密钥**（如 `5XFEHZ`）。多个 agent 同时工作时，
用密钥释放只影响自己的租约；`release -all` / `release -name` 可能误伤别人。

| 命令 | 作用 |
|---|---|
| `acquire -name x [-followpid n] [-timeouthours 12] [-ttlseconds n] [-json]` | 申请（等完成模式），返回 `id` 与 `key` |
| `release -key x` / `-id x` / `-name x` / `-all` | 通知完成（优先用 `-key`） |
| `extend -id x -for 30m` / `-until 07:00` / `-all` | 改成定时 |
| `heartbeat -id x` | 给带 TTL 的租约续期 |
| `status [-json]` | 查看状态（含密钥） |
| `stop` | 释放全部并立即恢复 |

时间写法：`30m` `90s` `2h` `1.5d` `30分钟` `2小时` `06:00` `2026-11-15T06:00`

## 界面

- **托盘图标**：只显示品牌图标，左键打开面板，右键「打开面板 / 设置 / 退出」。
- **任务栏任务胶囊**（`panel/barnative.go`，原生绘制）：`● 保持中 租约 N`，叠在任务栏空白区，
  点击（按下即触发）打开面板；保持中泛绿并连续呼吸（余弦，1.6s 一个周期）；被自动隐藏/全屏打断后
  重新出现时约 200ms 淡入。窗口真透明，圆角外直接透出任务栏，不再采样任务栏底色。
- **面板**（420 宽竖屏）：状态页 + 设置页。
  - 状态页：上方固定（状态、三个指标块、手动保持按钮），下方是动态租约列表（显示租约密钥）。
  - 设置页：任务胶囊位置、显示开关、agent 最长保持、呼吸开关、Agent 提示词。
  - 标题栏「复制提示词」一键把英文使用说明（如何申请、用密钥清理自己的租约）复制到剪贴板；
    内容可在设置页编辑，默认只读，点「编辑」后才能改。
  - 首页和设置页共用最大高度 672 DIP（约原来 544 与 800 的中间值）。首页随租约
    增多长高、最多显示 5 行；设置页超过 672 后整页滚动（不显示滚动条），
    提示词区域平铺展开，不再单独出滚动条。
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
旧 PowerShell 实现每 5 秒 spawn 两次 `pwsh`（单核约 73%），现在空闲时几乎为零。

| 指标 | 实测（本机 125% 缩放） |
|---|---|
| 空闲 CPU（托盘 + 胶囊） | 计时粒度内为 0（30 秒 15 ms） |
| 保持中 CPU（胶囊呼吸） | ≈34 ms/s（≈3.4% 单核，20 fps 只重绘不重建） |
| 呼吸关掉时 | ≈1.5 ms/s（基本为 0） |
| 状态轮询产生的子进程 | 0（只有点按钮/agent 调用时才跑 `awake.exe`） |
| `panel.exe` 内存（托盘 + 胶囊） | ≈85–88 MB 工作集 / ≈45–48 MB 私有工作集 |
| WebView2 | 胶囊常驻 **0 个进程**；只有打开面板才启动（browser+GPU+network+storage+crashpad+1 renderer，约 150 MB 私有） |

任务栏胶囊是 mygo 原生 `ui` 绘制的（`panel/barnative.go`）：窗口真透明、圆角真镂空，
不再需要「采样任务栏底色」。改原生之前胶囊是内嵌网页，会 24 小时养着 WebView2
的 6 个进程（≈150 MB 私有 / ≈430 MB 工作集），面板关着也不释放。

呼吸是连续的余弦曲线（1.6s 一个周期），跑在 **只重绘、不重建** 的绘制帧上
（`Painter.AnimationFrameRate(20)`）：每帧只画一个半透明绿色圆角矩形，文字排版
不重来。代价是保持中时约 34 ms/s；关掉呼吸就回到 ≈1.5 ms/s。对比旧网页版的
60fps CSS 呼吸（≈1.4% 整机），同样丝滑但便宜一个数量级。

## 测试

```powershell
cd cli;   go test ./...   # 时间解析、参数解析、租约判定、列宽对齐
cd panel; go test ./...   # 胶囊的绘制与点击、状态 DTO、租约规则
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
| `%LOCALAPPDATA%\AgentAwake\panel.json` | 界面设置（位置、显示、上限、面板位置、Agent 提示词） |
| `%LOCALAPPDATA%\AgentAwake\awake.log` | 日志（1 MiB 轮转，托盘右键「打开日志」可打开） |

## 日志

单文件追加，一行一条 `时间  [来源]  内容`，来源有 `awake`（命令行）、
`supervisor`（守护进程）、`panel`（托盘/面板）。超过 1 MiB 轮转成
`awake.log.old`，一共最多占 2 MiB——正常一天几十条、十万分之一条的量级，
够存很久。托盘右键「打开日志」用记事本打开。

租约的 key 会写进日志：它本来就在 `leases\*.json` 里（同目录、同权限），
日志没有扩大暴露面，而人要靠它把日志和界面上显示的那串对上号。
不过日志仍属敏感信息，**不要贴给 agent、也不要贴进 issue**。

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
