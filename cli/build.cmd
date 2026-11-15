@echo off
rem Build awake.exe (console CLI used by agents and the panel)
cd /d "%~dp0"
go build -ldflags "-s -w" -o ..\awake.exe .
if errorlevel 1 exit /b 1
echo Built awake.exe
