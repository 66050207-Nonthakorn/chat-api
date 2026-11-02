param(
    [ValidateSet("baseline", "api-restart", "worker-restart", "valkey-interruption", "all", "cleanup")]
    [string]$Scenario = "baseline",
    [int]$DurationSeconds = 60,
    [int]$Connections = 100,
    [int]$MessagesPerSecond = 10,
    [int]$PayloadBytes = 256,
    [int]$GroupPercent = 50,
    [int]$FaultAfterSeconds = 12,
    [int]$FaultDurationSeconds = 5,
    [switch]$Cleanup
)

$ErrorActionPreference = "Stop"
$project = "chat-ticket08-benchmark"
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..\..\..")).Path
$composeFile = Join-Path $repoRoot "infra\load\compose.benchmark.yaml"
$evidenceDir = Join-Path $repoRoot "docs\adr\evidence"

function Invoke-Compose {
    param([string[]]$ComposeArgs)
    & docker compose --project-name $project --file $composeFile @ComposeArgs
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose $($ComposeArgs -join ' ') failed with exit code $LASTEXITCODE"
    }
}

function Wait-ApiReady {
    $deadline = [DateTime]::UtcNow.AddSeconds(60)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $null = Invoke-RestMethod -Uri "http://127.0.0.1:18080/health/ready" -TimeoutSec 2
            return
        } catch {
            Start-Sleep -Seconds 1
        }
    }
    throw "Isolated API did not become ready within 60 seconds"
}

function Invoke-LoadRun {
    param(
        [string]$Name,
        [string]$FaultService
    )
    $reportPath = "docs/adr/evidence/ticket-08-$Name.json"
    $stdoutPath = Join-Path $evidenceDir "ticket-08-$Name.stdout.json"
    $stderrPath = Join-Path $evidenceDir "ticket-08-$Name.stderr.log"
    $faultPath = Join-Path $evidenceDir "ticket-08-$Name-fault.json"
    $readyPath = Join-Path $evidenceDir "ticket-08-$Name-ready.txt"
    foreach ($path in @($reportPath, $stdoutPath, $stderrPath, $faultPath, $readyPath)) {
        Remove-Item -LiteralPath (Join-Path $repoRoot $path) -Force -ErrorAction SilentlyContinue
    }
    $arguments = @(
        "run", "./cmd/loadbench",
        "--base-url", "http://127.0.0.1:18080",
        "--connections", "$Connections",
        "--messages-per-second", "$MessagesPerSecond",
        "--duration", "${DurationSeconds}s",
        "--payload-bytes", "$PayloadBytes",
        "--group-percent", "$GroupPercent",
        "--group-room-size", "2",
        "--private-room-size", "2",
        "--compose-file", "infra/load/compose.benchmark.yaml",
        "--compose-project", $project,
        "--output", $reportPath
    )
    if ($FaultService) {
        Remove-Item -LiteralPath $readyPath -Force -ErrorAction SilentlyContinue
        $arguments += @("--fault-window", "docs/adr/evidence/ticket-08-$Name-fault.json", "--ready-file", "docs/adr/evidence/ticket-08-$Name-ready.txt")
    }
    $quoted = $arguments | ForEach-Object { '"' + $_.Replace('"', '\"') + '"' }
    $process = Start-Process -FilePath "go" -ArgumentList ($quoted -join " ") -WorkingDirectory $repoRoot -PassThru -NoNewWindow -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath

    if ($FaultService) {
        $readyDeadline = [DateTime]::UtcNow.AddSeconds(60)
        while (-not (Test-Path -LiteralPath $readyPath) -and [DateTime]::UtcNow -lt $readyDeadline) {
            $process.Refresh()
            if ($process.HasExited) {
                Get-Content $stderrPath -ErrorAction SilentlyContinue
                throw "Load run '$Name' exited before all WebSocket subscriptions were ready"
            }
            Start-Sleep -Milliseconds 250
        }
        if (-not (Test-Path -LiteralPath $readyPath)) {
            throw "Load run '$Name' did not reach the requested connection count within 60 seconds"
        }
        Start-Sleep -Seconds $FaultAfterSeconds
        Invoke-Compose -ComposeArgs @("stop", $FaultService)
        $faultStart = [DateTime]::UtcNow
        Start-Sleep -Seconds $FaultDurationSeconds
        Invoke-Compose -ComposeArgs @("start", $FaultService)
        if ($FaultService -eq "api") {
            Wait-ApiReady
        } elseif ($FaultService -eq "valkey") {
            Wait-ApiReady
        } else {
            Start-Sleep -Seconds 2
        }
        $faultEnd = [DateTime]::UtcNow
        $fault = [ordered]@{
            service = $FaultService
            started_at = $faultStart.ToString("o")
            ended_at = $faultEnd.ToString("o")
        } | ConvertTo-Json
        [IO.File]::WriteAllText($faultPath, $fault + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    }

    $process.WaitForExit()
    $process.Refresh()
    if ($null -ne $process.ExitCode -and $process.ExitCode -ne 0) {
        Get-Content $stderrPath -ErrorAction SilentlyContinue
        throw "Load run '$Name' failed with exit code $($process.ExitCode); see $stderrPath"
    }
    $report = Get-Content -LiteralPath (Join-Path $repoRoot $reportPath) -Raw | ConvertFrom-Json
    if ($report.errors.sync_errors -gt 0 -or $report.traffic.not_found_by_sync -gt 0) {
        throw "Load run '$Name' reported sync errors or accepted messages missing from HTTP sync"
    }
    Remove-Item -LiteralPath $stdoutPath -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $stderrPath -Force -ErrorAction SilentlyContinue
    Write-Host "Wrote $reportPath"
}

if ($Scenario -eq "cleanup") {
    Invoke-Compose -ComposeArgs @("down", "--volumes", "--remove-orphans")
    Write-Host "Removed only isolated Compose project '$project' and its named benchmark volume."
    exit 0
}

if ($DurationSeconds -lt 10 -or $Connections -lt 1 -or $MessagesPerSecond -lt 1 -or $PayloadBytes -lt 1 -or $PayloadBytes -gt 4096 -or $GroupPercent -lt 0 -or $GroupPercent -gt 100) {
    throw "Invalid workload settings. Duration must be >=10s, connections/rate positive, payload 1..4096, group percent 0..100."
}
if ($Scenario -in @("api-restart", "worker-restart", "valkey-interruption", "all") -and ($FaultAfterSeconds + $FaultDurationSeconds + 5 -ge $DurationSeconds)) {
    throw "The fault window and a post-recovery interval must fit inside the load duration."
}

New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
Set-Location $repoRoot
Invoke-Compose -ComposeArgs @("build", "api", "worker", "migrate")
Invoke-Compose -ComposeArgs @("up", "-d", "--wait", "postgres", "valkey")
Invoke-Compose -ComposeArgs @("run", "--rm", "--no-deps", "migrate")

$previous = @{}
foreach ($key in @("APP_ENV", "ENABLE_LOCAL_TEST_IDENTITY", "DATABASE_URL", "VALKEY_URL", "CURSOR_HMAC_KEY")) {
    $previous[$key] = [Environment]::GetEnvironmentVariable($key, "Process")
}
try {
    $env:APP_ENV = "local"
    $env:ENABLE_LOCAL_TEST_IDENTITY = "true"
    $env:DATABASE_URL = "postgres://chat:chat@127.0.0.1:15432/chat?sslmode=disable"
    $env:VALKEY_URL = "redis://127.0.0.1:16379/0"
    $env:CURSOR_HMAC_KEY = "ticket-08-local-benchmark-key-32-bytes-minimum"
    & go run ./cmd/seed
    if ($LASTEXITCODE -ne 0) { throw "Demo seed failed with exit code $LASTEXITCODE" }
} finally {
    foreach ($key in $previous.Keys) {
        [Environment]::SetEnvironmentVariable($key, $previous[$key], "Process")
    }
}

Invoke-Compose -ComposeArgs @("up", "-d", "api", "worker")
Wait-ApiReady

switch ($Scenario) {
    "baseline" { Invoke-LoadRun -Name "baseline" -FaultService "" }
    "api-restart" { Invoke-LoadRun -Name "api-restart" -FaultService "api" }
    "worker-restart" { Invoke-LoadRun -Name "worker-restart" -FaultService "worker" }
    "valkey-interruption" { Invoke-LoadRun -Name "valkey-interruption" -FaultService "valkey" }
    "all" {
        Invoke-LoadRun -Name "baseline" -FaultService ""
        Invoke-LoadRun -Name "api-restart" -FaultService "api"
        Invoke-LoadRun -Name "worker-restart" -FaultService "worker"
        Invoke-LoadRun -Name "valkey-interruption" -FaultService "valkey"
    }
}

if ($Cleanup) {
    Invoke-Compose -ComposeArgs @("down", "--volumes", "--remove-orphans")
    Write-Host "Removed only isolated Compose project '$project' and its named benchmark volume."
}
