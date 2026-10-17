# Windows 客户端

Windows x64 用随包的 Xray-core 26.9.9 做出口（VLESS、Reality、Vision 等由服务端下发的 Xray JSON 决定）。配置在内存里补上 TUN 字段后从标准输入交给 `xray.exe`，不写盘。`wintun.dll` 放在程序旁边。需要管理员权限（安装包清单里的 `requireAdministrator`）。

WireGuard 引擎代码还在，但节点列表里没有可用的 WireGuard 节点时，连接不会走到它，也不会提示去安装 WireGuard。

本机装不了 Flutter Windows 目标时，用 GitHub Actions 的 `windows-client` 工作流（拉取请求、推送到 `main`，或手动 `workflow_dispatch`）。产物是未签名的 `YuhaiVPN-Windows-candidate-x64.exe`。

## 在 Windows 上自己构建

需要 Windows 10 或 11 x64、Git、Visual Studio 2022（「使用 C++ 的桌面开发」）、Flutter stable、Go 1.27.2、Inno Setup 6。简体中文语言文件在 `windows/packaging/ChineseSimplified.isl`，安装好的 Inno Setup 不必再自带这份翻译。

在仓库根目录：

```powershell
# 1. 固定提交编译 xray.exe（不要把 exe 提交进仓库）
git clone --depth 1 --branch v26.9.9 https://github.com/XTLS/Xray-core.git $env:TEMP\xray-src
cd $env:TEMP\xray-src
# 确认 HEAD 是 52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120
$env:CGO_ENABLED = '0'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
go build -trimpath -buildvcs=false -ldflags "-X github.com/xtls/xray-core/core.build=52a412d -s -w -buildid=" -o xray.exe ./main

# 2. 取官方 wintun 0.14.1，核对 SHA-256 后再拷贝 amd64 的 dll
#    zip  07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51
#    dll  e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce
#    https://www.wintun.net/builds/wintun-0.14.1.zip

# 3. 放到 Flutter 工程里（该目录已被 gitignore）
Copy-Item xray.exe <仓库>\apps\mobile\windows\third_party\xray.exe
Copy-Item wintun.dll <仓库>\apps\mobile\windows\third_party\wintun.dll

# 4. 用刚编出的 xray 检查 Windows TUN 配置能被这个版本接受
Get-Content <仓库>\apps\mobile\windows\packaging\tun-selftest.json -Raw |
  .\xray.exe run -c stdin: -test

cd <仓库>\apps\mobile
flutter pub get
flutter analyze
flutter test
flutter build windows --release
& "C:\Program Files (x86)\Inno Setup 6\ISCC.exe" windows\packaging\yuhai.iss
```

安装包在 `apps/mobile/build/windows/installer/YuhaiVPN-Windows-candidate-x64.exe`。Xray 的 MPL-2.0 与 Wintun 许可证在安装向导和程序目录的 `licenses` 里。源码对应 `https://github.com/XTLS/Xray-core/tree/52a412d9e2f5c2a5142b1b4e2ab3771dacb8b120`。

`tun-selftest.json` 只用文档地址（`203.0.113.10`、`www.example.com`）和假的公钥，不能拿来连接。

## 真机测试清单

CI 编得出安装包，也跑单元测试，但不能代替一台真实的 Win10/11。下面每一项都要在装好驱动的桌面系统上做，并记下结果。不要把节点地址、账号或配置写进仓库。

1. 安装未签名包：SmartScreen 会提示未知发布者，选「更多信息 → 仍要运行」。确认启动时出现 UAC。
2. 登录后只选择一条出口线路（不要依赖自动选择）。连接成功前界面应停在「连接中」，浏览器能打开 `https://www.gstatic.com/generate_204` 一类地址之后才变成已连接。
3. 出口 IP：用浏览器打开一家只回显访问者地址的网站，确认地址属于所选线路，而不是本机宽带。
4. DNS：用一家 DNS 泄露检测页，确认解析不是本机运营商的 DNS。若仍走物理网卡，记下来（Windows 多宿主解析的已知问题，这一版没有加 WFP 拦截）。
5. 流量数字会随浏览变化。断开后普通网页恢复直连。
6. 睡眠再唤醒、从 Wi-Fi 换到网线或反过来、拔掉当前网络再插上：要么仍可用，要么明确变成失败/重连，不能一直显示已连接但实际上不走隧道。
7. 用任务管理器结束「宇海」进程后，`xray.exe` 应一起消失。`route print` 里不应留下指向已消失的 `xray0` 的默认路由；不重装也能正常上网。
8. 卸载应删掉 `%ProgramData%\YuhaiVPN`。
9. 看 Microsoft Defender 是否把 `yuhai_vpn.exe`、`xray.exe` 或 `wintun.dll` 报成木马。未签名的 VPN 程序经常被误报，把提示原文记下来即可。
