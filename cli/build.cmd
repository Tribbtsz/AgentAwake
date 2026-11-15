@echo off
rem Build awake.exe (console CLI used by agents and the panel)
cd /d "%~dp0"
go build -trimpath -ldflags "-s -w" -o ..\awake.exe .
if errorlevel 1 (
  echo Build failed. If awake.exe is running, stop it first: awake stop
  exit /b 1
)
echo Built awake.exe
