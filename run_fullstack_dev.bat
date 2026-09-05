@echo off
setlocal
set "ROOT=%~dp0"
pushd "%ROOT%" >nul

echo Starting GoTaskAI full stack in development mode...

start "GoTaskAI API Dev" powershell -NoExit -Command "Set-Location '%CD%'; go run ./cmd/api"
start "GoTaskAI Worker Dev" powershell -NoExit -Command "Set-Location '%CD%'; go run ./cmd/worker"
start "GoTaskAI Web Dev" powershell -NoExit -Command "Set-Location '%CD%\web'; if (-not (Test-Path '.\node_modules')) { npm install }; npm run dev"

echo API: http://localhost:8080
echo Web: http://localhost:5173
echo All development services are starting in separate terminal windows.
popd >nul
pause
