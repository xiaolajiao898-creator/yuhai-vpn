#!/bin/bash
# 真机 arm64-v8a release。公开调试证书签名，包名带 .candidate，可与正式版并存。
# 不读取正式钥匙，不打开模拟器探测入口，API 只指向 https://api.yhvpn.io。
set -euo pipefail

root="$(cd "$(dirname "$0")/../../../.." && pwd)"
cd "$root"

mkdir -p apps/mobile/android/app/libs
aar=apps/mobile/android/app/libs/libv2ray.aar
if [ ! -f "$aar" ]; then
  curl -fL --retry 3 -o "$aar" \
    https://github.com/2dust/AndroidLibXrayLite/releases/download/v26.9.9/libv2ray.aar
fi
echo "9ecf4c921568d8f4cb8550d3bafe08ff6f1d1984f45a6ad183dcdf52ee9302de  $aar" | sha256sum -c

sdk="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-/usr/local/lib/android/sdk}}"
sdkmanager="$sdk/cmdline-tools/latest/bin/sdkmanager"
if [ ! -x "$sdkmanager" ]; then
  echo "预装 Android SDK 缺少 cmdline-tools：$sdkmanager"
  exit 1
fi
if [ ! -w "$sdk" ]; then
  sudo chmod -R a+w "$sdk"
fi
export ANDROID_SDK_ROOT="$sdk"
export ANDROID_HOME="$sdk"
export PATH="$sdk/cmdline-tools/latest/bin:$sdk/platform-tools:$PATH"
flutter config --android-sdk "$sdk"
set +o pipefail
yes | "$sdkmanager" --sdk_root="$sdk" --licenses >/dev/null || true
set -o pipefail
flutter_root="${FLUTTER_ROOT:-$(cd "$(dirname "$(command -v flutter)")/.." && pwd)}"
ext="$flutter_root/packages/flutter_tools/gradle/src/main/kotlin/FlutterExtension.kt"
ndk="$(sed -n 's/.*ndkVersion: String = "\([^"]*\)".*/\1/p' "$ext" | head -1)"
compile_sdk="$(sed -n 's/.*compileSdkVersion: Int = \([0-9][0-9]*\).*/\1/p' "$ext" | head -1)"
if [ -z "$ndk" ] || [ -z "$compile_sdk" ]; then
  echo "无法从 $ext 读取 Flutter 需要的 SDK 组件"
  exit 1
fi
packages=()
if [ ! -d "$sdk/platforms/android-$compile_sdk" ]; then
  packages+=("platforms;android-$compile_sdk")
fi
if [ ! -d "$sdk/ndk/$ndk" ]; then
  packages+=("ndk;$ndk")
fi
if [ "${#packages[@]}" -gt 0 ]; then
  if ! "$sdkmanager" --sdk_root="$sdk" "${packages[@]}"; then
    echo "预装 sdkmanager 无法安装 ${packages[*]}，改用 cmdline-tools 22.0"
    tmp="$(mktemp -d)"
    curl -fL --retry 3 -o "$tmp/cmdtools.zip" \
      https://dl.google.com/android/repository/commandlinetools-linux-15859902_latest.zip
    unzip -q "$tmp/cmdtools.zip" -d "$tmp"
    rm -rf "$sdk/cmdline-tools/22.0"
    mv "$tmp/cmdline-tools" "$sdk/cmdline-tools/22.0"
    rm -rf "$tmp"
    sdkmanager="$sdk/cmdline-tools/22.0/bin/sdkmanager"
    set +o pipefail
    yes | "$sdkmanager" --sdk_root="$sdk" --licenses >/dev/null || true
    set -o pipefail
    "$sdkmanager" --sdk_root="$sdk" "${packages[@]}"
  fi
fi

if [ ! -f "$HOME/.android/debug.keystore" ]; then
  mkdir -p "$HOME/.android"
  keytool -genkeypair -keystore "$HOME/.android/debug.keystore" \
    -storepass android -alias androiddebugkey -keypass android \
    -keyalg RSA -keysize 2048 -validity 10000 \
    -dname "CN=Android Debug,O=Android,C=US"
fi

cd apps/mobile
export YH_CANDIDATE=multiproto
export YH_ABI=arm64-v8a
export YH_RELEASE_DEBUG_SIGN=1
export YH_API_BASE="${YH_API_BASE:-https://api.yhvpn.io}"
unset YH_VPN_PROBE || true
mapfile -t defines < <(python3 "$root/apps/mobile/tool/release_defines.py")
flutter pub get
flutter build apk --release --split-per-abi --target-platform android-arm64 \
  "${defines[@]}"

cd "$root"
apk=apps/mobile/build/app/outputs/flutter-apk/app-arm64-v8a-release.apk
if [ ! -f "$apk" ]; then
  echo "没有生成 $apk"
  ls -la apps/mobile/build/app/outputs/flutter-apk || true
  exit 1
fi
unzip -l "$apk" > /tmp/apk-arm64.txt
grep -q 'lib/arm64-v8a/libgojni.so' /tmp/apk-arm64.txt
grep -q 'lib/arm64-v8a/libwg-go.so' /tmp/apk-arm64.txt
if grep -E 'lib/(x86_64|armeabi-v7a|x86)/' /tmp/apk-arm64.txt; then
  echo "安装包带了不该有的架构"
  exit 1
fi
if grep -E 'assets/(geoip|geosite)' /tmp/apk-arm64.txt; then
  echo "内核数据文件没有被剔除"
  exit 1
fi
if grep -Ei '\.(jks|keystore|p12|pem)$|debug\.keystore|signing\.env' /tmp/apk-arm64.txt; then
  echo "安装包里有钥匙或证书文件"
  exit 1
fi
python3 -c 'import sys,zipfile; apk=sys.argv[1]; z=zipfile.ZipFile(apk); natives=[i for i in z.infolist() if i.filename.startswith("lib/") and i.filename.endswith(".so")]; stored=[i.filename for i in natives if i.file_size>=1000000 and i.compress_type==zipfile.ZIP_STORED]; assert natives and not stored, stored; print("native libs", len(natives))' "$apk"
bt="$(ls -d "$sdk"/build-tools/*/ | sort -V | tail -1)"
if [ ! -x "${bt}aapt" ] || [ ! -x "${bt}apksigner" ]; then
  echo "build-tools 缺少 aapt 或 apksigner：$bt"
  exit 1
fi
"${bt}aapt" dump badging "$apk" > /tmp/badging-arm64.txt
grep -q "name='com.yuhai.yuhai_vpn.candidate'" /tmp/badging-arm64.txt
grep -q "native-code: 'arm64-v8a'" /tmp/badging-arm64.txt
if grep -q 'application-debuggable' /tmp/badging-arm64.txt; then
  echo "release 包不应是 debuggable"
  exit 1
fi
"${bt}apksigner" verify --print-certs "$apk" > /tmp/certs-arm64.txt
grep -q 'CN=Android Debug' /tmp/certs-arm64.txt
python3 "$root/apps/mobile/tool/assert_baked_api.py" "$apk"
bytes="$(stat -c%s "$apk")"
hash="$(sha256sum "$apk" | awk '{print $1}')"
echo "yuhai-candidate-arm64-v8a-release-debugsigned bytes=$bytes sha256=$hash"
