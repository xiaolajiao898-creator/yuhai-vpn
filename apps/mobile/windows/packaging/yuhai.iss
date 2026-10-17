; 宇海 VPN Windows x64 安装包。未签名。许可证页包含 Xray MPL-2.0 与 Wintun 许可。

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
ArchitecturesAllowed=x64
ArchitecturesInstallIn64BitMode=x64
LicenseFile=licenses\NOTICES.txt
VersionInfoVersion=1.1.0.0

[Languages]
; 发行版 ISCC 6.7 的 Languages 目录没有简体中文。这份是 kira-96 的 6.5.0+ 翻译
; （1ff90acc4ed4aee82b1cda43253243deee3daed4，MIT）。缺少的新消息回退为英文。
Name: "chinesesimp"; MessagesFile: "ChineseSimplified.isl"

[Tasks]
Name: "desktopicon"; Description: "创建桌面图标"; GroupDescription: "附加图标:"; Flags: checkedonce

[Files]
Source: "{#ReleaseDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{autoprograms}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\{#MyAppExeName}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#MyAppExeName}"; Description: "启动{#MyAppName}"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
Type: filesandordirs; Name: "{commonappdata}\YuhaiVPN"
