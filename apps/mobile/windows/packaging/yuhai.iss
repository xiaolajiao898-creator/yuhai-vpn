; 宇海 VPN Windows 安装包。程序是 x64 的，也能装在 Windows 11 ARM 版上（系统用 x64 仿真运行）。
; 未签名。许可证页包含 Xray MPL-2.0 与 Wintun 许可。

#define MyAppName "宇海VPN"
#define MyAppVersion "1.1.0"
#define MyAppPublisher "宇海"
#define MyAppExeName "yuhai_vpn.exe"
#define ReleaseDir "..\..\build\windows\x64\runner\Release"

[Setup]
AppId={{7C2E9A14-6B0F-4E2A-9D55-1F0A8C3E6B21}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppVerName={#MyAppName} {#MyAppVersion}
AppPublisher={#MyAppPublisher}
DefaultDirName={autopf}\YuhaiVPN
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
OutputDir=..\..\build\windows\installer
OutputBaseFilename=YuhaiVPN-Windows-candidate-x64
SetupIconFile=..\runner\resources\app_icon.ico
UninstallDisplayIcon={app}\{#MyAppExeName}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
PrivilegesRequired=admin
; x64compatible = x64 版 Windows，加上能仿真运行 x64 程序的 Windows 11 ARM 版（需要 Inno Setup 6.3 及以上）。
; 原来写的 x64 只认 x64 版 Windows，ARM 版会直接报「此程序不支持当前计算机运行的 Windows 版本」。
; 自带的 wintun.dll（amd64）里同时有 arm64 驱动，在 ARM 版上以 x64 仿真运行时会装 arm64 驱动。
; Windows 10 ARM 版不能运行 x64 程序，仍然装不了，会看到下面 [Messages] 里的说明。
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; Flutter 桌面程序只支持 Windows 10 及以上。
MinVersion=10.0
LicenseFile=licenses\NOTICES.txt
VersionInfoVersion=1.1.0.0

[Languages]
; 发行版 ISCC 6.7 的 Languages 目录没有简体中文。这份是 kira-96 的 6.5.0+ 翻译
; （1ff90acc4ed4aee82b1cda43253243deee3daed4，MIT）。缺少的新消息回退为英文。
Name: "chinesesimp"; MessagesFile: "ChineseSimplified.isl"

[Messages]
; 处理器架构不符时 Setup 用的是这条消息。默认文字只说「不支持当前 Windows 版本」，用户不知道原因。
WindowsVersionNotSupported=宇海VPN 需要 64 位的 Windows 10 / 11（Intel 或 AMD 处理器），或者 Windows 11 ARM 版。%n%n你的电脑是 32 位 Windows 或 Windows 10 ARM 版，暂时装不了。

[Tasks]
Name: "desktopicon"; Description: "创建桌面图标"; GroupDescription: "附加图标:"; Flags: checkedonce

[Files]
Source: "{#ReleaseDir}\*"; Excludes: "msvcp140.dll,vcruntime140.dll,vcruntime140_1.dll"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
; Flutter 程序依赖 VC++ 2015–2022 运行库（x64）。没装过运行库的电脑打开会报「找不到 VCRUNTIME140_1.dll」，
; 所以放在程序目录里一起装。CI 用 bundle-vcruntime.ps1 从构建机的 Visual Studio 运行库目录复制进 Release；
; 这里逐个列出，缺任何一个 ISCC 都会报错，不会打出一个打不开的安装包。
Source: "{#ReleaseDir}\msvcp140.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ReleaseDir}\vcruntime140.dll"; DestDir: "{app}"; Flags: ignoreversion
Source: "{#ReleaseDir}\vcruntime140_1.dll"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "启动{#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
Type: filesandordirs; Name: "{commonappdata}\YuhaiVPN"
