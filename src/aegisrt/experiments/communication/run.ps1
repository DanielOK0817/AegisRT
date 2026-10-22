param([string]$OutputParent = (Join-Path $PSScriptRoot "evidence"))

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "../..")).Path

function Get-SourceIdentity {
    param([string]$Root)
    $files = @(
        Get-ChildItem -LiteralPath (Join-Path $Root "cmd"), (Join-Path $Root "internal") -Recurse -File -Filter *.go
        Get-Item -LiteralPath (Join-Path $Root "go.mod"), (Join-Path $Root "go.sum"), $PSCommandPath
    )
    $entries = @($files | ForEach-Object {
        [ordered]@{
            path = $_.FullName.Substring($Root.Length + 1).Replace("\", "/")
            sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    } | Sort-Object { $_.path })
    $canonical = ($entries | ForEach-Object { $_.path + " " + $_.sha256 }) -join "`n"
    $algorithm = [System.Security.Cryptography.SHA256]::Create()
    try {
        $digest = $algorithm.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($canonical))
    } finally {
        $algorithm.Dispose()
    }
    [ordered]@{
        schema_version = 1
        source_sha256 = ([System.BitConverter]::ToString($digest)).Replace("-", "").ToLowerInvariant()
        canonical_format = "sorted relative path, one ASCII space, lowercase file SHA256; LF separators; no trailing LF"
        files = $entries
    }
}

Push-Location -LiteralPath $repoRoot
try {
    if (-not (Test-Path -LiteralPath $OutputParent)) {
        New-Item -ItemType Directory -Path $OutputParent -Force | Out-Null
    }
    $outputRoot = (Resolve-Path -LiteralPath $OutputParent).Path
    $identity = Get-SourceIdentity -Root $repoRoot
    $buildID = [Guid]::NewGuid().ToString("N")
    $buildDir = Join-Path ([System.IO.Path]::GetTempPath()) ("aegis-communication-build-" + $buildID)
    New-Item -ItemType Directory -Path $buildDir | Out-Null
    $binary = Join-Path $buildDir "aegisbench.exe"
    & go build -ldflags ("-X main.communicationSourceSHA256=" + $identity.source_sha256) -o $binary ./cmd/aegisbench
    if ($LASTEXITCODE -ne 0) { throw "communication benchmark build failed" }
    $afterBuild = Get-SourceIdentity -Root $repoRoot
    if ($afterBuild.source_sha256 -ne $identity.source_sha256) {
        throw "source files changed during build; retry after concurrent edits finish"
    }
    $manifestPath = Join-Path $outputRoot ("source-" + $identity.source_sha256 + ".json")
    $identity | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $manifestPath -Encoding UTF8
    & $binary communication --profile smoke --output $outputRoot
    if ($LASTEXITCODE -ne 0) { throw "communication benchmark failed; inspect the generated report.json" }
    $afterRun = Get-SourceIdentity -Root $repoRoot
    if ($afterRun.source_sha256 -ne $identity.source_sha256) {
        Write-Warning "Workspace changed during measurement; report identifies the already-built binary and its saved source manifest."
    }
    Write-Output ("Source manifest: " + $manifestPath)
} finally {
    Pop-Location
}
