param(
    [switch]$Measure,
    [string]$PreparedBuild = "",
    [string]$OutputParent = (Join-Path $PSScriptRoot "evidence")
)

$ErrorActionPreference = "Stop"
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot "../..")).Path
$baselineCommit = "48a2d846697d03c564fb39555ad0004efbb06597"

function Get-Identity {
    param([string]$Root, [System.IO.FileInfo[]]$Files)
    $entries = @($Files | ForEach-Object {
        [ordered]@{ path = $_.FullName.Substring($Root.Length + 1).Replace("\", "/")
            sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    } | Sort-Object { $_.path })
    $canonical = ($entries | ForEach-Object { $_.path + " " + $_.sha256 }) -join "`n"
    $hash = [System.Security.Cryptography.SHA256]::Create()
    try { $digest = $hash.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($canonical)) }
    finally { $hash.Dispose() }
    [ordered]@{ sha256 = ([BitConverter]::ToString($digest)).Replace("-", "").ToLowerInvariant(); files = $entries }
}

function Get-SourceIdentity {
    param([string]$Root)
    Get-Identity -Root $Root -Files @(
        Get-Item -LiteralPath (Join-Path $Root "go.mod"), (Join-Path $Root "go.sum")
        Get-ChildItem -LiteralPath (Join-Path $Root "internal/domain"), (Join-Path $Root "internal/scheduler") -Recurse -File -Filter *.go
    )
}

function Write-JSON {
    param($Value, [string]$Path)
    $text = $Value | ConvertTo-Json -Depth 20
    [System.IO.File]::WriteAllText($Path, $text + "`n", [System.Text.UTF8Encoding]::new($false))
}

Push-Location -LiteralPath $repoRoot
try {
    if ($PreparedBuild) {
        if (-not $Measure) { throw "PreparedBuild requires Measure" }
        $evidenceDir = (Resolve-Path -LiteralPath $PreparedBuild).Path
        $manifest = Get-Content -LiteralPath (Join-Path $evidenceDir "build.json") -Raw | ConvertFrom-Json
    } else {
        $buildID = [Guid]::NewGuid().ToString("N")
        $buildRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("aegis-core-comparison-" + $buildID)
        $evidenceDir = Join-Path $OutputParent ("build-" + (Get-Date -Format "yyyyMMdd-HHmmss") + "-" + $buildID.Substring(0,8))
        New-Item -ItemType Directory -Path $buildRoot, $evidenceDir -Force | Out-Null
        $evidenceDir = (Resolve-Path -LiteralPath $evidenceDir).Path
        $baselineRoot = Join-Path $buildRoot "baseline"
        $currentRoot = Join-Path $buildRoot "current"
        $archive = Join-Path $buildRoot "baseline.zip"
        & git -c core.autocrlf=false archive --format=zip --output=$archive $baselineCommit -- go.mod go.sum internal/domain internal/scheduler
        if ($LASTEXITCODE -ne 0) { throw "baseline archive failed" }
        Expand-Archive -LiteralPath $archive -DestinationPath $baselineRoot
        New-Item -ItemType Directory -Path (Join-Path $currentRoot "internal") -Force | Out-Null
        $currentBefore = Get-SourceIdentity -Root $repoRoot
        Copy-Item -LiteralPath (Join-Path $repoRoot "go.mod"), (Join-Path $repoRoot "go.sum") -Destination $currentRoot
        foreach ($name in @("domain", "scheduler")) {
            Copy-Item -LiteralPath (Join-Path $repoRoot ("internal/" + $name)) -Destination (Join-Path $currentRoot "internal") -Recurse
        }
        $currentIdentity = Get-SourceIdentity -Root $currentRoot
        if ($currentBefore.sha256 -ne $currentIdentity.sha256) { throw "source changed while copying; retry" }
        $baselineIdentity = Get-SourceIdentity -Root $baselineRoot
        $adapterFiles = @(Get-ChildItem -LiteralPath $PSScriptRoot -File -Filter *.go.tmpl)
        $adapterIdentity = Get-Identity -Root $PSScriptRoot -Files $adapterFiles
        Write-JSON $baselineIdentity (Join-Path $evidenceDir "baseline-source.json")
        Write-JSON $currentIdentity (Join-Path $evidenceDir "current-source.json")
        Write-JSON $adapterIdentity (Join-Path $evidenceDir "adapter-source.json")
        $binaries = @()
        foreach ($build in @(
            @{role="baseline"; root=$baselineRoot; identity=$baselineIdentity},
            @{role="current"; root=$currentRoot; identity=$currentIdentity}
        )) {
            $commandDir = Join-Path $build.root "cmd/corecomparison"
            New-Item -ItemType Directory -Path $commandDir -Force | Out-Null
            foreach ($file in $adapterFiles) {
                Copy-Item -LiteralPath $file.FullName -Destination (Join-Path $commandDir $file.Name.Replace(".tmpl", ""))
            }
            $binary = Join-Path $buildRoot ($build.role + ".exe")
            $flags = "-X main.sourceSHA256=" + $build.identity.sha256 + " -X main.adapterSHA256=" + $adapterIdentity.sha256 + " -X main.buildRole=" + $build.role
            Push-Location -LiteralPath $build.root
            try {
                & go build -trimpath -buildvcs=false -ldflags $flags -o $binary ./cmd/corecomparison
                if ($LASTEXITCODE -ne 0) { throw ($build.role + " adapter build failed") }
            } finally { Pop-Location }
            $binaries += [ordered]@{role=$build.role; path=$binary; sha256=(Get-FileHash -LiteralPath $binary -Algorithm SHA256).Hash.ToLowerInvariant()}
        }
        $adapterAfter = Get-Identity -Root $PSScriptRoot -Files @(Get-ChildItem -LiteralPath $PSScriptRoot -File -Filter *.go.tmpl)
        if ($adapterAfter.sha256 -ne $adapterIdentity.sha256) { throw "adapter changed during build; retry" }
        $archives = @()
        foreach ($snapshot in @(@{role="baseline"; root=$baselineRoot}, @{role="current"; root=$currentRoot})) {
            $zip = Join-Path $evidenceDir ($snapshot.role + "-build-input.zip")
            Compress-Archive -Path (Join-Path $snapshot.root "*") -DestinationPath $zip
            $archives += [ordered]@{role=$snapshot.role; file=[System.IO.Path]::GetFileName($zip); sha256=(Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()}
        }
        $goVersion = (& go version | Out-String).Trim()
        $manifest = [ordered]@{
            schema="aegisrt.core-comparison-build/v1"; baseline_commit=$baselineCommit
            created_at_utc=[DateTime]::UtcNow.ToString("o"); go_version=$goVersion
            source_scope="go.mod, go.sum, internal/domain/**/*.go, internal/scheduler/**/*.go; exact dependency subset, not whole daemon"
            canonical_format="sorted relative path, space, lowercase SHA256; LF separators; no trailing LF"
            adapter_sha256=$adapterIdentity.sha256; baseline_source_sha256=$baselineIdentity.sha256; current_source_sha256=$currentIdentity.sha256
            runner_sha256=(Get-FileHash -LiteralPath $PSCommandPath -Algorithm SHA256).Hash.ToLowerInvariant()
            verifier_sha256=(Get-FileHash -LiteralPath (Join-Path $PSScriptRoot "verify.py") -Algorithm SHA256).Hash.ToLowerInvariant()
            build_flags="-trimpath -buildvcs=false"; build_root=$buildRoot; binaries=$binaries
            archives=$archives
            variants=@{A="original 48a2 scheduler"; B="current settled-only ablation, no restoration"; C="current native reservation-aware scheduler"}
        }
        Write-JSON $manifest (Join-Path $evidenceDir "build.json")
        & python (Join-Path $PSScriptRoot "verify_sources.py") --build-directory $evidenceDir --compare-workspace
        if ($LASTEXITCODE -ne 0) { throw "source archive validation failed" }
    }
    Write-Output ("Prepared build: " + $evidenceDir)
    if (-not $Measure) { Write-Output "Build only; no simulations executed."; return }
    if ((Get-FileHash -LiteralPath (Join-Path $PSScriptRoot "verify.py") -Algorithm SHA256).Hash.ToLowerInvariant() -ne $manifest.verifier_sha256) { throw "prepared verifier identity mismatch" }
    foreach ($binary in $manifest.binaries) {
        if ((Get-FileHash -LiteralPath $binary.path -Algorithm SHA256).Hash.ToLowerInvariant() -ne $binary.sha256) { throw "prepared binary identity mismatch" }
    }
    $runDir = Join-Path $evidenceDir ("run-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $runDir | Out-Null
    foreach ($variant in @("A", "B", "C")) {
        $role = if ($variant -eq "A") { "baseline" } else { "current" }
        $binary = @($manifest.binaries | Where-Object { $_.role -eq $role })[0]
        & $binary.path --variant $variant --output (Join-Path $runDir ($variant + ".json"))
        if ($LASTEXITCODE -ne 0) { throw ("variant " + $variant + " failed") }
    }
    & python (Join-Path $PSScriptRoot "verify.py") --run-directory $runDir --build-directory $evidenceDir
    if ($LASTEXITCODE -ne 0) { throw "independent comparison verification failed" }
    Write-Output ("Comparison results: " + $runDir)
} finally { Pop-Location }
