package main

// task.go — 巡检用的计划任务（每 5 分钟跑一次 awake.exe -watchdog）
//
// 用系统自带 schtasks.exe + XML 定义：不依赖任何脚本，也不依赖注册表里的任务缓存。
// Hidden=true → 巡检时不会闪黑窗。

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

const taskName = "AgentAwake-Watchdog"

// 旧版本遗留的计划任务，启动时顺手清掉
var legacyTasks = []string{"OpenCodeAwake-Watchdog", "OpenCode-Awake-Restore", "OpenCode-Awake-Watchdog"}

func currentUser() string {
	domain := os.Getenv("USERDOMAIN")
	user := os.Getenv("USERNAME")
	if domain == "" {
		return user
	}
	return domain + `\` + user
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func taskXML() string {
	start := time.Now().Add(time.Minute).Format("2006-01-02T15:04:05")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>AgentAwake：每 5 分钟巡检，有租约就保证系统不休眠</Description>
  </RegistrationInfo>
  <Triggers>
    <TimeTrigger>
      <StartBoundary>%s</StartBoundary>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT5M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT3M</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%s</Command>
      <Arguments>-watchdog</Arguments>
    </Exec>
  </Actions>
</Task>`, start, xmlEscape(currentUser()), xmlEscape(exePath()))
}

// writeUTF16File schtasks 的 /XML 只接受 Unicode 文件
func writeUTF16File(path, content string) error {
	codes := utf16.Encode([]rune(content))
	buf := make([]byte, 0, len(codes)*2+2)
	buf = append(buf, 0xFF, 0xFE) // BOM (LE)
	for _, c := range codes {
		buf = binary.LittleEndian.AppendUint16(buf, c)
	}
	return os.WriteFile(path, buf, 0o644)
}

func schtasks(args ...string) (string, error) {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return runHidden(filepath.Join(root, "System32", "schtasks.exe"), args...)
}

func taskExists() bool {
	_, err := schtasks("/Query", "/TN", taskName)
	return err == nil
}

// taskPointsToUs 任务的动作是否已经指向当前 exe（迁移目录后要重新注册）。
// 用「取 Command 原文 + 反转义」再比：schtasks 输出把 & 转义成 &amp;，
// 拿原始路径做子串匹配时，路径含 & 就会永远匹配不上、每次 acquire 都重注册任务。
//
// 注意不能用 encoding/xml：schtasks 的输出声明 encoding="UTF-16"，
// 实际字节是本地 ANSI（中文乱码），Go 的 XML 解码器两种方式都会报错。
// 路径本身是 ASCII，正则取 Command 内容 + 手工反转义足够可靠。
func taskPointsToUs() bool {
	out, err := schtasks("/Query", "/TN", taskName, "/XML", "ONE")
	if err != nil {
		return false
	}
	if cmd, ok := taskCommand(out); ok {
		return strings.EqualFold(strings.TrimSpace(cmd), exePath())
	}
	return false
}

var commandRe = regexp.MustCompile(`(?s)<Command>(.*?)</Command>`)

// taskCommand 从任务 XML 里取出 <Exec><Command> 的原文（已反转义）
func taskCommand(xmlOut string) (string, bool) {
	m := commandRe.FindStringSubmatch(xmlOut)
	if m == nil {
		return "", false
	}
	return xmlUnescape(m[1]), true
}

// xmlUnescape 反转义 XML 实体（schtasks 输出里实际只会出现这几个）
func xmlUnescape(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return r.Replace(s)
}

// registerWatchdogTask 注册（或更新）巡检任务
func registerWatchdogTask() {
	if taskExists() && taskPointsToUs() {
		return
	}
	f, err := os.CreateTemp("", "agentawake-watchdog-*.xml")
	if err != nil {
		logf("awake", "写任务定义失败: %v", err)
		return
	}
	xmlPath := f.Name()
	f.Close()
	defer os.Remove(xmlPath)
	if err := writeUTF16File(xmlPath, taskXML()); err != nil {
		logf("awake", "写任务定义失败: %v", err)
		return
	}
	if out, err := schtasks("/Create", "/TN", taskName, "/XML", xmlPath, "/F"); err != nil {
		logf("awake", "注册巡检任务失败: %v %s", err, strings.TrimSpace(out))
	}
}

func removeWatchdogTask() {
	if !taskExists() {
		return
	}
	_, _ = schtasks("/Delete", "/TN", taskName, "/F")
}

func removeLegacyTasks() {
	for _, n := range legacyTasks {
		if _, err := schtasks("/Query", "/TN", n); err != nil {
			continue
		}
		if _, err := schtasks("/Delete", "/TN", n, "/F"); err == nil {
			logf("awake", "已清理旧版计划任务 %s", n)
		}
	}
}

// runWatchdog 计划任务入口：有租约没守护进程就拉起；没租约了就收摊
func runWatchdog() int {
	ensureDirs()
	active := activeLeases(time.Now())
	if len(active) > 0 {
		if supervisorPID() == 0 {
			p := startSupervisor()
			logf("watchdog", "巡检：发现 %d 个租约但没有守护进程，已拉起 PID %d", len(active), p)
		}
		return 0
	}
	if supervisorPID() == 0 {
		removeWatchdogTask()
		removeLegacyTasks()
	}
	return 0
}
