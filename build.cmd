@echo off
rem Build everything: CLI awake.exe + panel\panel.exe
setlocal
call "%~dp0cli\build.cmd" || exit /b 1
call "%~dp0panel\build.cmd" || exit /b 1
echo.
echo Build OK.
