#!/bin/bash
# 在已启动的 x86_64 模拟器上安装 release 候选包，确认两件事：
# 1. 进程被杀掉之后，安全存储里的登录会话还在。
# 2. 用本地黑洞配置拉起 VPN 后，进程没有崩溃。不连接真实服务器。
set -euo pipefail

PKG=com.yuhai.yuhai_vpn.candidate
COMP=$PKG/com.yuhai.yuhai_vpn.VpnProbeActivity
APK=${PROBE_APK:?}
OUT=${PROBE_OUT:?}
mkdir -p "$OUT/tombstones"

adb wait-for-device
adb shell 'while [[ -z $(getprop sys.boot_completed) ]]; do sleep 1; done'
adb root || true
adb wait-for-device

adb logcat -c || true
adb install -r "$APK"
adb shell pm clear "$PKG"
adb shell appops set "$PKG" ACTIVATE_VPN allow

start_probe() {
  if [ -n "${2:-}" ]; then
    adb shell am start -W -n "$COMP" --es op "$1" --es phase "$2"
  else
    adb shell am start -W -n "$COMP" --es op "$1"
  fi
}

wait_log() {
  local needle=$1
  local i
  for i in $(seq 1 40); do
    if adb logcat -d -s YuhaiProbe:I | tr -d '\r' | grep -F -q "$needle"; then
      return 0
    fi
    sleep 1
  done
  echo "没等到日志：$needle"
  adb logcat -d -s YuhaiProbe:I AndroidRuntime:E | tail -n 80 || true
  return 1
}

pidof_pkg() {
  adb shell pidof "$PKG" 2>/dev/null | tr -d '\r' || true
}

dump_diagnostics() {
  adb logcat -d -v threadtime > "$OUT/logcat.txt" || true
  adb shell dumpsys activity exit-info "$PKG" > "$OUT/exit-info.txt" || true
  adb shell ls /data/tombstones > "$OUT/tombstone-list.txt" 2>/dev/null || true
  adb pull /data/tombstones "$OUT/tombstones" >/dev/null 2>&1 || true
  find "$OUT/tombstones" -type d -empty -exec touch {}/.keep \;
}

trap dump_diagnostics EXIT

echo "==> 会话：写入后 force-stop，再读"
start_probe session-write
wait_log "SESSION_WRITE ok"
sleep 2
adb shell am force-stop "$PKG"
sleep 1
start_probe session-read force-stop
wait_log "SESSION_READ present phase=force-stop"

echo "==> 会话：写入后 kill -9，再读"
start_probe session-write
wait_log "SESSION_WRITE ok"
sleep 2
pid=$(pidof_pkg)
if [ -n "$pid" ]; then
  adb shell kill -9 $pid || true
  sleep 1
fi
start_probe session-read kill
wait_log "SESSION_READ present phase=kill"

echo "==> 本地黑洞隧道"
before=$(pidof_pkg)
start_probe vpn
wait_log "VPN_START ok"
sleep 8
if ! adb shell echo adb-ok | tr -d '\r' | grep -q adb-ok; then
  echo "VPN 拉起后 adb 断开。进程是否崩溃要看已抓到的 logcat。"
  exit 2
fi
after=$(pidof_pkg)
logged=$(adb logcat -d -s YuhaiProbe:I | tr -d '\r' | sed -n 's/.*VPN_START ok pid=\([0-9]*\).*/\1/p' | tail -n 1)
echo "pid before=$before logged=$logged after=$after"
if [ -z "$after" ] || [ "$after" != "$logged" ]; then
  echo "VPN 拉起后进程不在了，或 pid 变了"
  exit 1
fi
echo "PROBE_OK session persisted and vpn process stayed up"
