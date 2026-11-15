# AgentAwake — 开发约定

让 Windows 在 agent 干活期间不睡眠、但屏幕照常熄灭的工具。
纯 Go 实现：`awake.exe`（命令行 + 守护进程）与 `panel.exe`（托盘 + 面板）。

## 目录

| 路径 | 内容 |
|---|---|
| `cli/` | `awake.exe`：租约、守护进程、电源设置、巡检计划任务 |
| `panel/` | `panel.exe`：托盘、任务栏胶囊、面板；`panel/web/` 是内嵌前端 |
| `build.cmd` | 一次构建两个可执行文件 |
| `docs-added/` | 本地参考文档（不提交） |
| `tmp-workspace/` | 临时文件（不提交） |
| `agents-added/` | agent 附加说明（不提交） |

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
- 不提交 `*.exe`、日志、临时文件、密钥；`docs-added/` 与 `tmp-workspace/` 已忽略。
- 注释用中文，解释原因；界面文案统一中文。
- 面板的 Go ↔ JS 接口集中在 `panel/webapi.go`，DTO 字段与 `web/app.js` 对齐。
- 后台 goroutine 不直接操作 UI，回到主线程用 `RunOnMain` / `Update`。
- 提交使用约定式提交消息（`feat:` / `fix:` / `docs:` / `chore:` …）。

更细的框架与 Windows 机制笔记见 `docs-added/`（本地，不入库）。
