$ErrorActionPreference = "Stop"

$packageDirectories = @(go list -f '{{.Dir}}' ./...)
if ($LASTEXITCODE -ne 0) {
    throw "go list failed"
}
# Archived experiments contain independent modules and deliberate ablations.
# Check the active module without rewriting immutable historical evidence.
$goFiles = @($packageDirectories | ForEach-Object {
    Get-ChildItem -LiteralPath $_ -Filter *.go -File | ForEach-Object { $_.FullName }
} | Sort-Object -Unique)
$shellFiles = @(Get-ChildItem -Path . -Recurse -Filter *.sh | ForEach-Object { $_.FullName })
$bashPath = $null
if ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) {
    foreach ($candidate in @(
        "C:\Program Files\Git\bin\bash.exe",
        "C:\Program Files\Git\usr\bin\bash.exe"
    )) {
        if (Test-Path -LiteralPath $candidate) {
            $bashPath = $candidate
            break
        }
    }
}
if (-not $bashPath) {
    $bash = Get-Command bash -ErrorAction SilentlyContinue
    if ($bash) {
        $bashPath = $bash.Source
    }
}
if ($shellFiles -and -not $bashPath) {
    throw "bash is required to validate repository shell scripts"
}
foreach ($shellFile in $shellFiles) {
    $relativeShellFile = (Resolve-Path -LiteralPath $shellFile -Relative).Replace("\", "/")
    & $bashPath -n -- $relativeShellFile
    if ($LASTEXITCODE -ne 0) {
        throw "shell syntax validation failed: $relativeShellFile"
    }
}

$forbiddenFiles = @(Get-ChildItem -Path . -Recurse -Force -File | Where-Object {
    $_.FullName -notlike "*\.git\*" -and ($_.Name -eq ".env" -or $_.Extension -eq ".log")
})
if ($forbiddenFiles) {
    throw "Forbidden local or generated files detected:`n$($forbiddenFiles.FullName)"
}

$scanFiles = @(Get-ChildItem -Path . -Recurse -File | Where-Object {
    $_.FullName -notlike "*\.git\*" -and $_.Extension -in ".go", ".md", ".json", ".yml", ".yaml", ".toml", ".ps1", ".html", ".css", ".js", ".sh", ".service", ".csv", ".tmpl"
})
$credentialPattern = '(?i)(sk-[a-z0-9]{20,}|ghp_[a-z0-9]{20,}|AKIA[A-Z0-9]{16}|(api[_-]?key|access[_-]?token|client[_-]?secret|password)\s*[:=]\s*["''][^"'']{16,}["''])'
$credentialMatches = @($scanFiles | Select-String -Pattern $credentialPattern)
if ($credentialMatches) {
    throw "Credential-like values detected; inspect the source locally. Matched values are not printed."
}

$unformatted = @($goFiles | ForEach-Object { gofmt -l $_ })
if ($unformatted) {
    throw "Unformatted Go files:`n$unformatted"
}

go vet ./...
if ($LASTEXITCODE -ne 0) {
    throw "go vet failed"
}

go test -p 1 ./... -count=1
if ($LASTEXITCODE -ne 0) {
    throw "go test failed"
}

go test -race -p 1 ./... -count=1
if ($LASTEXITCODE -ne 0) {
    throw "go test -race failed"
}

go build ./...
if ($LASTEXITCODE -ne 0) {
    throw "go build failed"
}

Write-Output "AegisRT verification passed."
