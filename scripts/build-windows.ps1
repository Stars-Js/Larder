# 构建 Windows 桌面版（无控制台窗口）。
#
# 产物：dist/larder-windows-<arch>.exe
# 双击运行后会在 exe 同级生成 larder.toml 与 data/ 目录。
param(
    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64",
    [string]$Output = ""
)

$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $PSScriptRoot)

if (-not $Output) {
    $Output = "dist/larder-windows-$Arch.exe"
}

$outPath = Join-Path (Get-Location) $Output
$outDir = Split-Path -Parent $outPath
if ($outDir -and -not (Test-Path $outDir)) {
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null
}

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = $Arch

Write-Host "正在构建 $outPath (windows/$Arch) ..."
go build -trimpath -ldflags "-s -w -H windowsgui" -o $outPath .

Write-Host "构建完成：$outPath"
Write-Host "双击运行；首次启动会在同目录生成 larder.toml 与 data\。"
