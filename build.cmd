@echo off
rem Build everything: CLI awake.exe + panel\panel.exe
rem Usage: build.cmd [version]   e.g. build.cmd v0.1.0
setlocal
call "%~dp0cli\build.cmd" %1 || exit /b 1
call "%~dp0panel\build.cmd" %1 || exit /b 1
echo.
echo Build OK.
