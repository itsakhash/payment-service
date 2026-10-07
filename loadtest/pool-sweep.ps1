param(
    [string]$Scenario = "hot",
    [string]$Tag = "",
    [int[]]$Sizes = @(2, 4, 8, 16),
    [int]$Runs = 2
)

$exe = "$env:TEMP\payment-server.exe"
go build -o $exe ./cmd/server
if ($LASTEXITCODE -ne 0) { throw "build failed" }

foreach ($n in $Sizes) {
    $env:DATABASE_URL = "postgres://payments:payments@localhost:5432/payments?pool_max_conns=$n"
    $proc = Start-Process -FilePath $exe -PassThru -WindowStyle Hidden

    for ($t = 0; $t -lt 20; $t++) {
        try { Invoke-WebRequest http://localhost:8080/healthz -UseBasicParsing | Out-Null; break }
        catch { Start-Sleep -Milliseconds 500 }
    }

    for ($i = 1; $i -le $Runs; $i++) {
        Write-Host "=== $Scenario pool_max_conns=$n run $i of $Runs ==="
        Get-Content loadtest\reset.sql | docker compose exec -T postgres psql -U payments -d payments | Out-Null

        $k6args = @('run', "--summary-export=loadtest/results/$Scenario-pool$n$Tag-run$i.json")
        if ($Scenario -eq 'spread') { $k6args += @('-e', 'PAYER_MIN=3', '-e', 'MERCH_MIN=103') }
        $k6args += "loadtest/$Scenario.js"

        k6 @k6args | Select-String -Pattern '^\s+(http_reqs|http_req_duration\.|http_req_failed)'
    }

    Stop-Process -Id $proc.Id
    Start-Sleep -Seconds 1
}
Remove-Item Env:DATABASE_URL -ErrorAction SilentlyContinue