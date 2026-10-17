#!/bin/bash
# 宇海VPN 安卓「多协议候选版」一键构建与自检（在你的 Mac 上运行，可重复执行）
# - 独立包名 com.yuhai.yuhai_vpn.candidate、名称「宇海多协议测试版」，和正式版并存，不覆盖正式版
# - 用与正式版相同的签名钥匙；连接正式后台 https://api.yhvpn.io（后台未升级前只会显示 WireGuard 线路）
# - 构建前先跑：Flutter 静态检查 + Flutter 测试 + 安卓原生单元测试；任何一项失败就停止
set -euo pipefail
die() { printf '\n[失败] %s\n' "$*" >&2; exit 1; }

ROOT="$(cd "$(dirname "$0")/.." && pwd)"   # 仓库根目录（脚本位于 deploy/）
# 工具链位置都可用环境变量覆盖：FLUTTER_HOME / JAVA_HOME / ANDROID_SDK_ROOT / PUB_CACHE
FLUTTER_HOME="${FLUTTER_HOME:-$HOME/flutter}"
if [ -z "${JAVA_HOME:-}" ] && [ -x /usr/libexec/java_home ]; then JAVA_HOME=$(/usr/libexec/java_home -v 17 2>/dev/null || true); fi
export JAVA_HOME="${JAVA_HOME:-}"
export ANDROID_HOME="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-$HOME/Library/Android/sdk}}"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export PUB_CACHE="${PUB_CACHE:-$HOME/.pub-cache}"
export PATH="$JAVA_HOME/bin:$FLUTTER_HOME/bin:$PATH"
APP="$ROOT/apps/mobile"
SIGN="${YUHAI_ANDROID_KEY_DIR:-$HOME/.yuhai-android}"          # 内含 release.jks + signing.env（不入库）
FUND_KEY_FILE="${YUHAI_FUND_KEY_FILE:-$HOME/.yuhai-fund-key}"  # 内含 FUND_ADDRESS_PUBKEY_B64=（不入库）
OUT_DIR="${OUT_DIR:-$HOME/Desktop}"
API="https://api.yhvpn.io"
OUT="$OUT_DIR/宇海VPN-多协议候选版.apk"
LOG="$ROOT/deploy/candidate-build.log"
AAR_URL="https://github.com/2dust/AndroidLibXrayLite/releases/download/v26.9.9/libv2ray.aar"
AAR_SHA="9ecf4c921568d8f4cb8550d3bafe08ff6f1d1984f45a6ad183dcdf52ee9302de"

[ -x "$JAVA_HOME/bin/java" ] || die "找不到 JDK 17：${JAVA_HOME:-未设置}（可设置 JAVA_HOME）"
[ -x "$FLUTTER_HOME/bin/flutter" ] || die "找不到 Flutter：$FLUTTER_HOME（可设置 FLUTTER_HOME）"
[ -d "$ANDROID_HOME/platforms" ] || die "找不到 Android SDK：$ANDROID_HOME（可设置 ANDROID_SDK_ROOT）"
[ -f "$SIGN/release.jks" ] && [ -f "$SIGN/signing.env" ] || die "找不到正式签名钥匙（$SIGN，可设置 YUHAI_ANDROID_KEY_DIR）"
FUNDPUB=$(grep '^FUND_ADDRESS_PUBKEY_B64=' "$FUND_KEY_FILE" 2>/dev/null | cut -d= -f2-)
[ -n "$FUNDPUB" ] || die "找不到收款签名公钥（$FUND_KEY_FILE，可设置 YUHAI_FUND_KEY_FILE）"
: > "$LOG"

echo "==> 1/6 准备 Xray 内核（第一次约 1 分钟，62MB）"
LIB="$APP/android/app/libs/libv2ray.aar"
mkdir -p "$(dirname "$LIB")"
if [ ! -f "$LIB" ] || [ "$(shasum -a 256 "$LIB" | cut -d' ' -f1)" != "$AAR_SHA" ]; then
  curl -fL --retry 3 -o "$LIB.part" "$AAR_URL" >>"$LOG" 2>&1 || die "内核下载失败（详见 $LOG）"
  [ "$(shasum -a 256 "$LIB.part" | cut -d' ' -f1)" = "$AAR_SHA" ] || { rm -f "$LIB.part"; die "内核校验不符，已丢弃"; }
  mv -f "$LIB.part" "$LIB"
fi
echo "  内核已核对：AndroidLibXrayLite v26.9.9（Xray-core 26.9.9）"

# shellcheck disable=SC1091
. "$SIGN/signing.env"
export YH_ANDROID_KEYSTORE="$SIGN/release.jks" YH_ANDROID_KEY_ALIAS=yuhai
export YH_ANDROID_STORE_PASSWORD="$PW" YH_ANDROID_KEY_PASSWORD="$PW"
export YH_CANDIDATE=multiproto

echo "==> 2/6 下载依赖（约 1 分钟）"
cd "$APP"
flutter --no-version-check config --no-analytics >/dev/null 2>&1 || true
flutter --no-version-check pub get >>"$LOG" 2>&1 || die "依赖下载失败（详见 $LOG）"

echo "==> 3/6 静态检查（约 1 分钟）"
flutter --no-version-check analyze >>"$LOG" 2>&1 || { tail -n 30 "$LOG"; die "静态检查未通过"; }
echo "  通过"

echo "==> 4/6 Flutter 测试（约 2 分钟）"
flutter --no-version-check test >>"$LOG" 2>&1 || { tail -n 40 "$LOG"; die "Flutter 测试未通过"; }
echo "  $(grep -Eo 'All tests passed!|\+[0-9]+' "$LOG" | tail -1)"

echo "==> 5/6 打包候选版（约 3-6 分钟，别关终端）"
flutter --no-version-check build apk --release --target-platform android-arm64 \
  --dart-define=YH_API_BASE="$API" \
  --dart-define=YH_FUND_KEY_ID_1=fund-prod-1 \
  --dart-define=YH_FUND_PUBKEY_1="$FUNDPUB" >>"$LOG" 2>&1 || { tail -n 30 "$LOG"; die "打包失败"; }
APK="$APP/build/app/outputs/flutter-apk/app-release.apk"
[ -f "$APK" ] || die "没有生成安装包"

echo "==> 6/6 安卓原生单元测试 + 校验（约 2-4 分钟）"
DEFINES=$(printf 'YH_API_BASE=%s' "$API" | base64)
(cd android && ./gradlew -q ":app:testDebugUnitTest" "-Pdart-defines=$DEFINES") >>"$LOG" 2>&1 \
  || { tail -n 30 "$LOG"; die "原生单元测试未通过"; }
echo "  原生单元测试通过（XrayPolicyTest、WireGuardPolicyTest）"
AS=$(ls -d "$ANDROID_HOME"/build-tools/*/ | sort -V | tail -1)apksigner
AAPT=$(ls -d "$ANDROID_HOME"/build-tools/*/ | sort -V | tail -1)aapt2
"$AS" verify "$APK" || die "签名校验失败"
PKG=$("$AAPT" dump badging "$APK" 2>/dev/null | grep -Eo "package: name='[^']+'" | cut -d"'" -f2)
[ "$PKG" = "com.yuhai.yuhai_vpn.candidate" ] || die "包名不对（$PKG），为防覆盖正式版已停止"
# 先取完整清单再检查：管道里 grep -q 提前退出会让 unzip 收到 SIGPIPE，在 pipefail 下误判失败。
LIST=$(unzip -l "$APK")
grep -q "lib/arm64-v8a/libgojni.so" <<<"$LIST" || die "安装包里没有 Xray 内核"
if grep -Eq "assets/(geoip|geosite)\.dat" <<<"$LIST"; then die "内核数据文件没有被剔除"; fi
if grep -Eq "lib/(armeabi-v7a|x86_64|x86)/" <<<"$LIST"; then die "候选版不应包含 64 位 ARM 以外的原生库"; fi
cp "$APK" "$OUT"
echo
echo "  包名：$PKG"
echo "  大小：$(du -h "$OUT" | cut -f1)"
echo "  SHA-256：$(shasum -a 256 "$OUT" | cut -d' ' -f1)"
echo "  签名证书：$("$AS" verify --print-certs "$APK" | grep -m1 'SHA-256 digest' | awk '{print $NF}')"
echo; echo "✅ 候选版已生成：桌面上的「宇海VPN-多协议候选版.apk」（测试用，不要发给客户）"
