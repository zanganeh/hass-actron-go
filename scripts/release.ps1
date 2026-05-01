#!/usr/bin/env pwsh
# release.ps1 — bump version, tag, push, trigger CI build
# Usage: .\scripts\release.ps1 -Version 1.2.3

param(
    [Parameter(Mandatory)]
    [ValidatePattern('^\d+\.\d+\.\d+$')]
    [string]$Version
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ── Preflight ────────────────────────────────────────────────────────────────

$configFile = "$PSScriptRoot\..\config.json"
$config = Get-Content $configFile | ConvertFrom-Json

$current = $config.version
Write-Host "Current version : $current"
Write-Host "New version     : $Version"
Write-Host ""

if ($Version -eq $current) {
    Write-Error "Version $Version is already current. Bump the version number."
}

# Enforce semver order: new > current
$cur = [version]$current
$new = [version]$Version
if ($new -le $cur) {
    Write-Error "New version ($Version) must be greater than current ($current)."
}

# Must be on main/master with clean working tree
$branch = git rev-parse --abbrev-ref HEAD
if ($branch -notin @('main', 'master')) {
    Write-Error "Releases must be made from main/master. Current branch: $branch"
}

$dirty = git status --porcelain
if ($dirty) {
    Write-Error "Working tree is dirty. Commit or stash changes first:`n$dirty"
}

# Must be up-to-date with remote
git fetch origin $branch --quiet
$behind = git rev-list HEAD..origin/$branch --count
if ([int]$behind -gt 0) {
    Write-Error "Local branch is $behind commits behind origin/$branch. Pull first."
}

# Tests must pass
Write-Host "Running tests..."
go test ./... 2>&1
if ($LASTEXITCODE -ne 0) {
    Write-Error "Tests failed. Fix before releasing."
}
Write-Host "Tests passed." -ForegroundColor Green
Write-Host ""

# ── Bump version in config.json ───────────────────────────────────────────────

$config.version = $Version
$config | ConvertTo-Json -Depth 10 | Set-Content $configFile -Encoding UTF8
Write-Host "Updated config.json version to $Version"

# ── Commit, tag, push ─────────────────────────────────────────────────────────

git add $configFile
git commit -m "chore: release v$Version"
git tag "v$Version"
git push origin $branch
git push origin "v$Version"

Write-Host ""
Write-Host "Released v$Version" -ForegroundColor Green
Write-Host "GitHub Actions is building Docker images. Monitor at:"
Write-Host "  https://github.com/zanganeh/hass-actron-go/actions" -ForegroundColor Cyan
Write-Host ""
Write-Host "After images are built and pushed to GHCR, make packages public at:"
Write-Host "  https://github.com/zanganeh?tab=packages" -ForegroundColor Cyan
Write-Host ""
Write-Host "HA will show 'Update available' for existing installations automatically."
