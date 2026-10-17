#!/bin/bash
# 宇海VPN：在 Mac 上开一台安卓模拟器，并装上「多协议候选版」（可重复执行）
# 第一次会下载模拟器和安卓系统镜像，约 2GB；之后再运行只需 1-2 分钟。
set -euo pipefail
die() { printf '\n[失败] %s\n' "$*" >&2; exit 1; }

# 工具链位置可用环境变量覆盖：JAVA_HOME / ANDROID_SDK_ROOT / SDKMANAGER / APK
if [ -z "${JAVA_HOME:-}" ] && [ -x /usr/libexec/java_home ]; then JAVA_HOME=$(/usr/libexec/java_home -v 17 2>/dev/null || true); fi
export JAVA_HOME="${JAVA_HOME:-}"
export ANDROID_HOME="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-$HOME/Library/Android/sdk}}"
export ANDROID_SDK_ROOT="$ANDROID_HOME"
export PATH="$JAVA_HOME/bin:$ANDROID_HOME/platform-tools:$PATH"
APK="${APK:-${OUT_DIR:-$HOME/Desktop}/宇海VPN-多协议候选版.apk}"
PKG="com.yuhai.yuhai_vpn.candidate"
IMG="system-images;android-35;google_apis;arm64-v8a"
AVD="yuhai-test"
SDKM="${SDKMANAGER:-$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager}"

[ "$(uname -m)" = "arm64" ] || die "这台 Mac 不是苹果芯片，本脚本只准备了苹果芯片用的系统镜像"
[ -x "$JAVA_HOME/bin/java" ] || die "找不到 JDK 17：${JAVA_HOME:-未设置}（可设置 JAVA_HOME）"
[ -x "$SDKM" ] || die "找不到 sdkmanager：$SDKM（可设置 SDKMANAGER）"
[ -f "$APK" ] || die "找不到安装包 $APK，请先运行 build-android-candidate.sh（或设置 APK=路径）"

echo "==> 1/4 准备模拟器与系统镜像（第一次约 10-20 分钟，约 2GB）"
yes | "$SDKM" --sdk_root="$ANDROID_HOME" --licenses >/dev/null 2>&1 || true
"$SDKM" --sdk_root="$ANDROID_HOME" "emulator" "cmdline-tools;latest" "$IMG" >/dev/null || die "下载失败，请检查网络后重试"
AVDM="$ANDROID_HOME/cmdline-tools/latest/bin/avdmanager"
if ! "$AVDM" list avd 2>/dev/null | grep -q "Name: $AVD"; then
  echo no | "$AVDM" create avd -n "$AVD" -k "$IMG" -d pixel_7 >/dev/null || die "创建模拟器失败"
fi
echo "  模拟器已就绪：$AVD（Android 15，64 位 ARM）"

echo "==> 2/4 启动模拟器（约 1-2 分钟，会弹出一个手机窗口）"
if ! adb devices | grep -q "^emulator-"; then
  nohup "$ANDROID_HOME/emulator/emulator" -avd "$AVD" -no-snapshot-save -gpu auto >/tmp/yuhai-emulator.log 2>&1 &
fi
adb wait-for-device
for i in $(seq 1 120); do
  [ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ] && break
  sleep 2
done
[ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ] || die "模拟器 4 分钟内没有启动完成（日志：/tmp/yuhai-emulator.log）"

echo "==> 3/4 安装候选版（约 30 秒）"
adb install -r "$APK" >/dev/null || die "安装失败"

echo "==> 4/4 打开宇海多协议测试版"
adb shell monkey -p "$PKG" -c android.intent.category.LAUNCHER 1 >/dev/null 2>&1 || true
echo
echo "✅ 已在模拟器里打开「宇海多协议测试版」。看完直接关掉模拟器窗口即可。"
