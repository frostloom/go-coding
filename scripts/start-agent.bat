@echo off
setlocal
rem go-coding one-click launcher.
rem   double-click                    -> start in the project root
rem   start-agent.bat C:\path\to\proj -> start in that folder
rem Model settings come from ~/.go-coding.env (copy go-coding.env.example and
rem fill in your key); the CLI loads that file itself, no env needed here.
cd /d "%~dp0.."
if not "%~1"=="" cd /d "%~1"
set PATH=E:\go1.25.12.windows-amd64\go\bin;%PATH%
if not exist go-coding.exe go build -o go-coding.exe ./cmd/agent
if errorlevel 1 (
  echo build failed - is Go on PATH? add it and rerun.
  pause
  exit /b 1
)
go-coding.exe -interactive
endlocal
