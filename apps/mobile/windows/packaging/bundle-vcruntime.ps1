# 把 VC++ 2015–2022 运行库（x64）复制进 Flutter 的 Release 目录，安装包会把它们装到程序目录。
#
# 为什么需要：Flutter 的 Windows 程序和插件动态链接 VC++ 运行库。没装过这个运行库的电脑，
# 打开宇海会依次报「找不到 VCRUNTIME140_1.dll / VCRUNTIME140.dll / MSVCP140.dll」。
# GitHub 的 Windows 构建机自带运行库，所以 CI 里的启动冒烟测试发现不了这个问题。
#
# 文件来源：构建机上 Visual Studio 的 VC\Redist\MSVC\<版本>\x64\Microsoft.VC14x.CRT，
# 和编译程序用的是同一套工具链；每个文件都要有有效的微软签名，否则直接失败。
#
# 用法（在仓库根目录，先运行 flutter build windows --release）：
#   pwsh apps/mobile/windows/packaging/bundle-vcruntime.ps1
#   pwsh apps/mobile/windows/packaging/bundle-vcruntime.ps1 -Release <Release 目录>
param(
  [string]$Release = (Join-Path $PSScriptRoot '..\..\build\windows\x64\runner\Release')
)

$ErrorActionPreference = 'Stop'
$names = @('msvcp140.dll', 'vcruntime140.dll', 'vcruntime140_1.dll')

if (-not (Test-Path $Release)) {
  throw "找不到 Release 目录：$Release（先运行 flutter build windows --release）"
}
$Release = (Resolve-Path $Release).Path

$vswhere = Join-Path ${env:ProgramFiles(x86)} 'Microsoft Visual Studio\Installer\vswhere.exe'
if (-not (Test-Path $vswhere)) { throw "找不到 vswhere：$vswhere" }
$vs = & $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath
if (-not $vs) { throw '找不到装了 C++ 桌面工具的 Visual Studio' }

$root = Join-Path $vs 'VC\Redist\MSVC'
if (-not (Test-Path $root)) { throw "找不到运行库目录：$root" }
$crt = $null
$versions = Get-ChildItem $root -Directory |
  Where-Object { $_.Name -match '^\d+(\.\d+){1,3}$' } |
  Sort-Object { [version]$_.Name } -Descending
foreach ($dir in $versions) {
  $candidate = Get-ChildItem (Join-Path $dir.FullName 'x64') -Directory -Filter 'Microsoft.VC*.CRT' -ErrorAction SilentlyContinue |
    Select-Object -First 1
  if ($candidate) { $crt = $candidate; break }
}
if (-not $crt) { throw "在 $root 下找不到 x64 的 Microsoft.VC*.CRT 目录" }

# 三个文件全部检查通过才复制，避免只复制了一部分。
$sources = foreach ($name in $names) {
  $src = Join-Path $crt.FullName $name
  if (-not (Test-Path $src)) { throw "运行库目录里没有 $name：$($crt.FullName)" }
  $sig = Get-AuthenticodeSignature -FilePath $src
  if ($sig.Status -ne 'Valid' -or $sig.SignerCertificate.Subject -notmatch 'O=Microsoft Corporation') {
    throw "$name 没有有效的微软签名：$($sig.Status)"
  }
  $bytes = [System.IO.File]::ReadAllBytes($src)
  $pe = [BitConverter]::ToInt32($bytes, 0x3C)
  $machine = [BitConverter]::ToUInt16($bytes, $pe + 4)
  if ($machine -ne 0x8664) { throw "$name 不是 x64 版本（machine=$machine）" }
  $src
}
foreach ($src in $sources) {
  Copy-Item -Path $src -Destination $Release -Force
}

"VC++ 运行库来自 $($crt.FullName)，已复制：$($names -join '、')"
