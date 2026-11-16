# AgentAwake — 开发约定

让 Windows 在 agent 干活期间不睡眠、但屏幕照常熄灭的工具。
纯 Go 实现：`awake.exe`（命令行 + 守护进程）与 `panel.exe`（托盘 + 面板）。

## 目录

| 路径 | 内容 |
|---|---|
| `cli/` | `awake.exe`：租约、守护进程、电源设置、巡检计划任务 |
| `panel/` | `panel.exe`：托盘、任务栏胶囊、面板；`panel/web/` 是内嵌前端 |
| `docs/` | 面向用户的截图与设计文档（`design.md`） |
| `build.cmd` | 一次构建两个可执行文件；`build.cmd vX.Y.Z` 注入版本号 |

## 命令

```powershell
cd cli;   go test ./...; go build ./...
cd panel; go test ./...; go build ./...
.\build.cmd          # 生成 awake.exe + panel.exe
```

前端没有热更新：改 `panel/web/` 后需要重新构建。

## 约定

- 纯 Go、无 cgo；系统 API 走 `golang.org/x/sys/windows` 或 `syscall`。
- 不写死盘符或用户路径：可执行文件用 `os.Executable()`，数据用 `%LOCALAPPDATA%`。
- 不提交 `*.exe`、日志、临时文件、密钥。
- 注释用中文，解释原因；界面文案统一中文。
- 面板的 Go ↔ JS 接口集中在 `panel/webapi.go`，DTO 字段与 `web/app.js` 对齐。
- 后台 goroutine 不直接操作 UI，回到主线程用 `RunOnMain` / `Update`；
  `onUI` 依赖 mygo `RunOnMain` 主线程重入直接执行的语义（见 `docs/design.md`）。
- 提交使用约定式提交消息（`feat:` / `fix:` / `docs:` / `chore:` …）。

## 发版规范

**版本号**（SemVer；1.0 之前接口不承诺稳定）：

| 改动 | 版本 | 例 |
|---|---|---|
| 新增用户可感知的功能 | `0.x.0` | v0.1.0 → v0.2.0 |
| 只有修复 | `0.x.y` | v0.2.0 → v0.2.1 |
| 破坏性变更 / 行为契约冻结 | `1.0.0` 起 | — |

**节奏**：按「一个用户可感知的变更批次」发布，不逐 commit 发，也不积压：

- 新功能单独发；2–3 个修复攒一批；typo/纯文档跟随下一批
- 严重问题（崩溃、数据丢失、安全）立即发，不攒
- 判断标准：这次改动值不值得用户重新下载一次

**流程**：

1. main 分支测试全绿（CI 在 push/PR 时自动验证）
2. `git tag -a vX.Y.Z -m "AgentAwake vX.Y.Z"`
3. `git push origin vX.Y.Z` — `.github/workflows/release.yml` 自动构建
   zip（含两个 exe + README/LICENSE/DESIGN）、生成 SHA256 并发布 Release

**产物**：`AgentAwake-vX.Y.Z-windows-x64.zip` + `checksums-sha256.txt`；
目前只发 windows-x64，未做代码签名（SmartScreen 提示在 Release 说明里告知）。

