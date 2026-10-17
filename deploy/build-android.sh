#!/bin/bash
# 宇海VPN 安卓正式版一键打包（在你的 Mac 上运行，可重复执行）
# 连接正式后台 https://api.yhvpn.io，用正式签名钥匙签名。
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
OUT="$OUT_DIR/宇海VPN-安卓正式版.apk"

[ -x "$JAVA_HOME/bin/java" ] || die "找不到 JDK 17：${JAVA_HOME:-未设置}（可设置 JAVA_HOME）"
[ -x "$FLUTTER_HOME/bin/flutter" ] || die "找不到 Flutter：$FLUTTER_HOME（可设置 FLUTTER_HOME）"
[ -d "$ANDROID_HOME/platforms" ] || die "找不到 Android SDK：$ANDROID_HOME（可设置 ANDROID_SDK_ROOT）"
[ -d "$APP" ] || die "找不到 App 源码：$APP"
FUNDPUB=$(grep '^FUND_ADDRESS_PUBKEY_B64=' "$FUND_KEY_FILE" 2>/dev/null | cut -d= -f2-)
[ -n "$FUNDPUB" ] || die "找不到收款签名公钥（$FUND_KEY_FILE，可设置 YUHAI_FUND_KEY_FILE）"

echo "==> 1/4 准备正式签名钥匙（只在第一次生成）"
if [ ! -f "$SIGN/release.jks" ]; then
  mkdir -p "$SIGN"; chmod 700 "$SIGN"
  PW=$(openssl rand -hex 24)
  keytool -genkeypair -keystore "$SIGN/release.jks" -storetype PKCS12 -alias yuhai \
    -keyalg RSA -keysize 4096 -validity 10000 -storepass "$PW" -keypass "$PW" \
    -dname "CN=Yuhai VPN, O=Yuhai, C=JP" >/dev/null 2>&1 || die "生成签名钥匙失败"
  (umask 077; printf 'PW=%s\n' "$PW" > "$SIGN/signing.env")
  chmod 600 "$SIGN/release.jks" "$SIGN/signing.env"
  echo "  已生成：$SIGN/release.jks（这把钥匙以后每次更新 App 都要用，务必备份）"
else
  echo "  沿用已有签名钥匙"
fi
# shellcheck disable=SC1091
. "$SIGN/signing.env"
export YH_ANDROID_KEYSTORE="$SIGN/release.jks" YH_ANDROID_KEY_ALIAS=yuhai
export YH_ANDROID_STORE_PASSWORD="$PW" YH_ANDROID_KEY_PASSWORD="$PW"

echo "==> 2/4 下载依赖（约 1 分钟）"
cd "$APP"
flutter --no-version-check config --no-analytics >/dev/null 2>&1 || true
flutter --no-version-check pub get >/dev/null || die "依赖下载失败"

echo "==> 3/4 打包正式版（约 3-6 分钟，别关终端）"
flutter --no-version-check build apk --release \
  --dart-define=YH_API_BASE="$API" \
  --dart-define=YH_FUND_KEY_ID_1=fund-prod-1 \
  --dart-define=YH_FUND_PUBKEY_1="$FUNDPUB" 2>&1 | tail -n 5
APK="$APP/build/app/outputs/flutter-apk/app-release.apk"
[ -f "$APK" ] || die "没有生成安装包"

echo "==> 4/4 校验签名"
AS=$(ls -d "$ANDROID_HOME"/build-tools/*/ | sort -V | tail -1)apksigner
"$AS" verify "$APK" || die "签名校验失败"
cp "$APK" "$OUT"
echo "  大小：$(du -h "$OUT" | cut -f1)"
echo "  SHA-256：$(shasum -a 256 "$OUT" | cut -d' ' -f1)"
echo "  签名证书：$("$AS" verify --print-certs "$APK" | grep -m1 'SHA-256 digest' | awk '{print $NF}')"
echo; echo "✅ 安卓正式版已生成：桌面上的「宇海VPN-安卓正式版.apk」"
