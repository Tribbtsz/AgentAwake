@echo off
rem Build awake.exe (console CLI used by agents and the panel)
rem Usage: build.cmd [version]   e.g. build.cmd v0.1.0
cd /d "%~dp0"
set LDFLAGS=-s -w
if not "%~1"=="" set LDFLAGS=-s -w -X main.version=%~1
go build -trimpath -ldflags "%LDFLAGS%" -o ..\awake.exe .
if errorlevel 1 (
  echo Build failed. If awake.exe is running, stop it first: awake stop
  exit /b 1
)
echo Built awake.exe
