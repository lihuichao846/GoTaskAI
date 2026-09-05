@echo off
setlocal
set "ROOT=%~dp0"
pushd "%ROOT%" >nul

echo Starting GoTaskAI in Microservice Mode...

echo [1/2] Starting API Server (Gateway)...
if exist ".\bin\api.exe" (
    start "GoTaskAI API Server" powershell -NoExit -Command "Set-Location '%CD%'; .\bin\api.exe"
) else (
    start "GoTaskAI API Server" powershell -NoExit -Command "Set-Location '%CD%'; go run ./cmd/api"
)

echo [2/2] Starting Worker Node Supervisor (Compute)...
if exist ".\bin\worker.exe" (
    start "GoTaskAI Worker Node" powershell -NoExit -Command "Set-Location '%CD%'; .\bin\worker.exe"
) else (
    start "GoTaskAI Worker Node" powershell -NoExit -Command "Set-Location '%CD%'; go run ./cmd/worker"
)

echo Both services started in separate terminal windows.
echo To stop, close the respective terminal windows.
popd >nul
pause
