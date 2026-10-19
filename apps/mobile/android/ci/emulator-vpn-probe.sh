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
adb logcat -G 16M || true

LIVE=$OUT/live-logcat.txt
: > "$LIVE"
adb logcat -c || true
adb logcat -v threadtime > "$LIVE" &
LOGCAT_PID=$!

pidof_pkg() {
  adb shell pidof "$PKG" 2>/dev/null | tr -d '\r' || true
}

adb install -r "$APK"
adb shell pm clear "$PKG" || true
# 忙碌的模拟器上 pm clear 会先返回，随后的清数据还会 force-stop。
# 那时再启动探测页，页面会被拆掉，安全存储的写入日志也不会出现。
for i in $(seq 1 180); do
  if grep -F "PACKAGE_DATA_CLEARED" "$LIVE" | grep -F "$PKG" >/dev/null; then
    break
  fi
  sleep 1
done
adb shell am force-stop "$PKG" || true
for i in $(seq 1 30); do
  if [ -z "$(pidof_pkg)" ]; then
    break
  fi
  sleep 1
done
adb shell appops set "$PKG" ACTIVATE_VPN allow

start_probe() {
  if [ -n "${2:-}" ]; then
    adb shell am start -W -n "$COMP" --es op "$1" --es phase "$2"
  else
    adb shell am start -W -n "$COMP" --es op "$1"
  fi
}

log_mark() {
  local n
  n=$(wc -c < "$LIVE" | tr -d ' ')
  if [ "${n:-0}" -lt 1 ]; then
    echo 1
  else
    echo "$n"
  fi
}

wait_log() {
  local needle=$1
  local mark=$2
  local i
  for i in $(seq 1 60); do
    # 不用 grep -q：pipefail 下它会让 tail 收到 SIGPIPE，匹配成功也被当成失败。
    if tail -c +"$mark" "$LIVE" | grep -F "$needle" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  echo "没等到日志：$needle"
  tail -n 80 "$LIVE" || true
  return 1
}

start_and_wait() {
  local op=$1
  local phase=${2:-}
  local needle=$3
  local attempt mark
  for attempt in 1 2 3; do
    mark=$(log_mark)
    start_probe "$op" "$phase" || true
    if wait_log "$needle" "$mark"; then
      return 0
    fi
    echo "重试 $op 第 $attempt 次"
    adb shell am force-stop "$PKG" || true
    sleep 2
  done
  return 1
}

dump_diagnostics() {
  if [ -n "${LOGCAT_PID:-}" ]; then
    kill "$LOGCAT_PID" 2>/dev/null || true
    wait "$LOGCAT_PID" 2>/dev/null || true
  fi
  if [ -s "$LIVE" ]; then
    cp "$LIVE" "$OUT/logcat.txt" || true
  else
    adb logcat -d -v threadtime > "$OUT/logcat.txt" || true
  fi
  adb shell dumpsys activity exit-info "$PKG" > "$OUT/exit-info.txt" || true
  adb shell ls /data/tombstones > "$OUT/tombstone-list.txt" 2>/dev/null || true
  adb pull /data/tombstones "$OUT/tombstones" >/dev/null 2>&1 || true
  mkdir -p "$OUT/tombstones"
  if ! find "$OUT/tombstones" -type f ! -name '.keep' ! -name 'none.txt' | grep . >/dev/null; then
    echo "no tombstones" > "$OUT/tombstones/none.txt"
  fi
}

trap dump_diagnostics EXIT

echo "==> 会话：写入后 force-stop，再读"
start_and_wait session-write "" "SESSION_WRITE ok"
sleep 2
adb shell am force-stop "$PKG"
sleep 1
start_and_wait session-read force-stop "SESSION_READ present phase=force-stop"

echo "==> 会话：写入后 kill -9，再读"
start_and_wait session-write "" "SESSION_WRITE ok"
sleep 2
pid=$(pidof_pkg)
if [ -n "$pid" ]; then
  adb shell kill -9 $pid || true
  sleep 1
fi
start_and_wait session-read kill "SESSION_READ present phase=kill"

echo "==> 本地黑洞隧道"
before=$(pidof_pkg)
start_and_wait vpn "" "VPN_START ok"
sleep 8
if ! adb shell echo adb-ok | tr -d '\r' | grep -F adb-ok >/dev/null; then
  echo "VPN 拉起后 adb 断开。进程是否崩溃要看已抓到的 logcat。"
  exit 2
fi
after=$(pidof_pkg)
logged=$(sed -n 's/.*VPN_START ok pid=\([0-9]*\).*/\1/p' "$LIVE" | tail -n 1)
echo "pid before=$before logged=$logged after=$after"
if [ -z "$after" ] || [ "$after" != "$logged" ]; then
  echo "VPN 拉起后进程不在了，或 pid 变了"
  exit 1
fi
echo "PROBE_OK session persisted and vpn process stayed up"
