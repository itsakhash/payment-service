param(
    [string]$Tag = "run",
    [int]$Runs = 3
)

for ($i = 1; $i -le $Runs; $i++) {
    Write-Host "=== $Tag run $i of $Runs ==="
    Get-Content loadtest\reset.sql | docker compose exec -T postgres psql -U payments -d payments | Out-Null
    k6 run --summary-export="loadtest/results/$Tag-$i.json" loadtest/hot.js |
        Select-String -Pattern '^\s+(http_reqs|http_req_duration\.|http_req_failed)'
}