@echo off
rem Build panel.exe as a GUI-subsystem binary (no console window at runtime)
cd /d "%~dp0"
go build -trimpath -ldflags "-H=windowsgui -s -w" -o panel.exe .
if errorlevel 1 exit /b 1
copy /y panel.exe ..\panel.exe >nul
if errorlevel 1 (
  echo Built panel\panel.exe, but could not replace ..\panel.exe - close the running panel first.
  exit /b 1
)
echo Built panel.exe
