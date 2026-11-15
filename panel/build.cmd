@echo off
rem Build panel.exe as a GUI-subsystem binary (no console window at runtime)
cd /d "%~dp0"
go build -ldflags "-H=windowsgui -s -w" -o panel.exe .
if errorlevel 1 exit /b 1
copy /y panel.exe ..\panel.exe >nul
echo Built panel.exe
