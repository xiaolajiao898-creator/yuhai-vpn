#!/bin/bash
# 宇海电脑版（Mac）一键检查 + 打包。在你的 Mac 上运行，可重复执行。
#   1. 自动补齐 macOS / Windows 工程（第一次）并打补丁（窗口大小、联网权限、管理员清单）
#   2. 代码检查 + 全部自动化测试，日志写到 deploy/app-check.log
#   3. 打包 Mac 版，放到桌面「宇海VPN-Mac版.zip」
set -uo pipefail
die() { printf '\n[失败] %s\n' "$*" >&2; exit 1; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"   # 仓库根目录（脚本位于 deploy/）
# 工具链位置都可用环境变量覆盖：FLUTTER_HOME / JAVA_HOME / ANDROID_SDK_ROOT / PUB_CACHE
FLUTTER_HOME="${FLUTTER_HOME:-$HOME/flutter}"
export PUB_CACHE="${PUB_CACHE:-$HOME/.pub-cache}"
export PATH="$FLUTTER_HOME/bin:$PATH"
APP="$ROOT/apps/mobile"
DEPLOY="$ROOT/deploy"
FUND_KEY_FILE="${YUHAI_FUND_KEY_FILE:-$HOME/.yuhai-fund-key}"  # 内含 FUND_ADDRESS_PUBKEY_B64=（不入库）
OUT_DIR="${OUT_DIR:-$HOME/Desktop}"
LOG="$DEPLOY/app-check.log"
API="https://api.yhvpn.io"

[ -x "$FLUTTER_HOME/bin/flutter" ] || die "找不到 Flutter：$FLUTTER_HOME（可设置 FLUTTER_HOME）"
FUNDPUB=$(grep '^FUND_ADDRESS_PUBKEY_B64=' "$FUND_KEY_FILE" 2>/dev/null | cut -d= -f2-)
[ -n "$FUNDPUB" ] || die "找不到收款签名公钥（$FUND_KEY_FILE，可设置 YUHAI_FUND_KEY_FILE）"
cd "$APP" || die "找不到 App 源码"

echo "==> 1/4 准备电脑版工程"
flutter --no-version-check config --no-analytics --enable-macos-desktop --enable-windows-desktop >/dev/null 2>&1
if [ ! -d macos ] || [ ! -d windows ]; then
  had_test=0; [ -f test/widget_test.dart ] && had_test=1
  flutter --no-version-check create --platforms=macos,windows --org com.yuhai --project-name yuhai_vpn . >/dev/null \
    || die "生成电脑版工程失败"
  [ "$had_test" = 0 ] && rm -f test/widget_test.dart
fi
python3 "$DEPLOY/patch-desktop.py" "$APP" || die "打补丁失败"
flutter --no-version-check pub get >/dev/null || die "依赖下载失败"

echo "==> 2/4 代码检查与自动化测试（约 2-4 分钟）"
{
  echo "===== $(date '+%F %T') flutter analyze ====="
  flutter --no-version-check analyze --no-fatal-infos --no-fatal-warnings 2>&1
  echo "ANALYZE_EXIT=$?"
  echo "===== flutter test ====="
  flutter --no-version-check test 2>&1 | tail -n 80
  echo "TEST_EXIT=${PIPESTATUS[0]}"
} > "$LOG" 2>&1
grep -E "ANALYZE_EXIT|TEST_EXIT|issues found|No issues|All tests passed|Some tests failed" "$LOG" | sed 's/^/  /'

echo "==> 3/4 打包 Mac 版（约 3-6 分钟）"
if ! xcodebuild -version >/dev/null 2>&1; then
  echo "  ⚠️ 还没有完整的 Xcode，Mac 版先跳过打包（检查结果已保存）。装好 Xcode 并打开一次后再运行本脚本。"
  exit 0
fi
flutter --no-version-check build macos --release \
  --dart-define=YH_API_BASE="$API" \
  --dart-define=YH_FUND_KEY_ID_1=fund-prod-1 \
  --dart-define=YH_FUND_PUBKEY_1="$FUNDPUB" 2>&1 | tail -n 5
APPDIR=$(ls -d build/macos/Build/Products/Release/*.app 2>/dev/null | head -1)
[ -n "$APPDIR" ] || die "没有生成 Mac 应用"

echo "==> 4/4 压缩"
OUT="$OUT_DIR/宇海VPN-Mac版.zip"
rm -f "$OUT"
ditto -c -k --keepParent "$APPDIR" "$OUT"
echo "  大小：$(du -h "$OUT" | cut -f1)"
echo; echo "✅ Mac 版已生成：桌面上的「宇海VPN-Mac版.zip」（解压后双击打开；首次打开如被拦截，右键选「打开」）"
