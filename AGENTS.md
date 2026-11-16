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
- 发版：更新版本号 → tag `vX.Y.Z` → GitHub Actions 自动构建发布（见 `.github/workflows/release.yml`）。
