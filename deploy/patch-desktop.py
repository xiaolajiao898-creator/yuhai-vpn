#!/usr/bin/env python3
"""宇海电脑版：在 `flutter create --platforms=macos,windows` 生成的工程上打补丁（可重复执行）。

- 窗口：默认 440×860，最小 400×720（和手机版同一套界面，像常见 VPN 客户端那样的小窗口）
- macOS：允许联网（沙盒 network.client）、应用显示名「宇海」
- Windows：窗口标题「宇海」、程序清单要求管理员权限（WireGuard 隧道服务需要）
"""
import pathlib
import re
import sys

app = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
changed = []


def patch(path, fn):
    p = app / path
    if not p.exists():
        print(f"  跳过（不存在）：{path}")
        return
    old = p.read_text(encoding="utf-8")
    new = fn(old)
    if new != old:
        p.write_text(new, encoding="utf-8")
        changed.append(path)


# ---------- macOS ----------
def mac_window(s):
    if "YUHAI_WINDOW" in s:
        return s
    return s.replace(
        "    let windowFrame = self.frame\n",
        "    // YUHAI_WINDOW：默认窗口大小\n"
        "    var windowFrame = self.frame\n"
        "    windowFrame.size = NSSize(width: 440, height: 860)\n"
        "    self.minSize = NSSize(width: 400, height: 720)\n"
        "    self.title = \"宇海\"\n",
        1,
    )


patch("macos/Runner/MainFlutterWindow.swift", mac_window)


def entitlements(s):
    if "com.apple.security.network.client" in s:
        return s
    return s.replace(
        "</dict>",
        "\t<key>com.apple.security.network.client</key>\n\t<true/>\n</dict>",
        1,
    )


patch("macos/Runner/DebugProfile.entitlements", entitlements)
patch("macos/Runner/Release.entitlements", entitlements)


def mac_name(s):
    s = re.sub(r"(?m)^PRODUCT_NAME = .*$", "PRODUCT_NAME = YuhaiVPN", s)
    s = re.sub(r"(?m)^PRODUCT_BUNDLE_IDENTIFIER = .*$", "PRODUCT_BUNDLE_IDENTIFIER = com.yuhai.vpn.desktop", s)
    return s


patch("macos/Runner/Configs/AppInfo.xcconfig", mac_name)


def mac_plist(s):
    if "CFBundleDisplayName" in s:
        return s
    return s.replace(
        "<dict>",
        "<dict>\n\t<key>CFBundleDisplayName</key>\n\t<string>宇海</string>",
        1,
    )


patch("macos/Runner/Info.plist", mac_plist)

# ---------- Windows ----------
def win_main(s):
    s = re.sub(r"Win32Window::Size size\(\s*\d+\s*,\s*\d+\s*\);", "Win32Window::Size size(440, 860);", s)
    s = re.sub(r'window\.Create\(L"[^"]*"', lambda m: 'window.Create(L"\\u5b87\\u6d77"', s)
    return s


patch("windows/runner/main.cpp", win_main)


def win_manifest(s):
    if "requireAdministrator" in s:
        return s
    block = (
        '  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">\n'
        "    <security>\n"
        "      <requestedPrivileges>\n"
        '        <requestedExecutionLevel level="requireAdministrator" uiAccess="false"/>\n'
        "      </requestedPrivileges>\n"
        "    </security>\n"
        "  </trustInfo>\n"
    )
    return s.replace("</assembly>", block + "</assembly>", 1)


patch("windows/runner/runner.exe.manifest", win_manifest)


def win_rc(s):
    s = re.sub(r'VALUE "ProductName", "[^"]*"', 'VALUE "ProductName", "YuhaiVPN"', s)
    s = re.sub(r'VALUE "FileDescription", "[^"]*"', 'VALUE "FileDescription", "YuhaiVPN"', s)
    return s


patch("windows/runner/Runner.rc", win_rc)

print("已修改：" + ("、".join(changed) if changed else "无（之前已改好）"))
