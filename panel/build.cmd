@echo off
rem Build panel.exe as a GUI-subsystem binary (no console window at runtime)
rem Usage: build.cmd [version]   e.g. build.cmd v0.1.0
cd /d "%~dp0"
set LDFLAGS=-H=windowsgui -s -w
if not "%~1"=="" set LDFLAGS=-H=windowsgui -s -w -X main.version=%~1
go build -trimpath -ldflags "%LDFLAGS%" -o panel.exe .
if errorlevel 1 exit /b 1
copy /y panel.exe ..\panel.exe >nul
if errorlevel 1 (
  echo Built panel\panel.exe, but could not replace ..\panel.exe - close the running panel first.
  exit /b 1
)
echo Built panel.exe
