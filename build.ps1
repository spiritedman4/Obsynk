# Builds the obsynkd daemon and the Obsidian plugin, bundling the daemon
# binary (and current proto definition) into the plugin folder.
#
# Personal-use scope: builds for this machine's platform only (win32-x64).
# See the plan's packaging section for what a multi-platform / published
# build would add (cross-compiled binaries for every OS/arch, CI matrix).

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot

Write-Host "==> Regenerating gRPC code from proto/obsynk/v1/obsynk.proto..."
& "$env:USERPROFILE\go\bin\buf.exe" generate proto

Write-Host "==> Copying proto definition into plugin/ (loaded at runtime by @grpc/proto-loader)..."
Copy-Item "$root/proto/obsynk/v1/obsynk.proto" "$root/plugin/obsynk.proto" -Force

Write-Host "==> Building obsynkd (CGO_ENABLED=0, win32-x64)..."
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -trimpath -ldflags="-s -w" -o "$root/plugin/bin/win32-x64/obsynkd.exe" "$root/cmd/obsynkd"
Remove-Item Env:\GOOS -ErrorAction SilentlyContinue
Remove-Item Env:\GOARCH -ErrorAction SilentlyContinue

Write-Host "==> Running Go tests..."
go test "$root/..."

Write-Host "==> Building plugin (tsc typecheck + esbuild bundle)..."
Push-Location "$root/plugin"
try {
    npm run build
    npm test
} finally {
    Pop-Location
}

Write-Host ""
Write-Host "==> Done. Plugin build output (copy this whole folder into <vault>/.obsidian/plugins/obsynk/):"
Write-Host "    $root/plugin/main.js"
Write-Host "    $root/plugin/manifest.json"
Write-Host "    $root/plugin/styles.css"
Write-Host "    $root/plugin/obsynk.proto"
Write-Host "    $root/plugin/bin/win32-x64/obsynkd.exe"
