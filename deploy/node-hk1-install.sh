#!/usr/bin/env bash
# node-hk1-install.sh —— 宇海VPN 自建出口节点一键安装（VLESS + Reality + Vision，可选 Hysteria2）
#
# 适用：Ubuntu 22.04 / 24.04，root 运行。可重复执行（幂等）：已生成的 UUID / 密钥 / shortId
# 保存在 /usr/local/etc/xray/yuhai-node.env（root 600），再次运行会沿用，不会让已导入的线路失效。
# 本脚本不含任何写死的密钥；结束时打印分享链接与后台「出口线路 → 批量导入」用的 JSON。
#
# 可选环境变量：
#   XRAY_VERSION=v26.9.9          服务端内核版本（与 App 内嵌内核一致）
#   REALITY_TARGET=dl.google.com  Reality 伪装目标（须支持 TLS1.3 + H2；不要用 www.microsoft.com /
#                                 www.apple.com 这类被 Xray 26 点名“更易被封”的热门目标）
#   NODE_NAME="香港 HK1"  COUNTRY=HK  CITY=香港  REGION=ASIA
#   ENABLE_HY2=1                  额外开 Hysteria2（UDP 443，自签证书 + 指纹固定）
#   HY2_SNI=www.bing.com          Hysteria2 自签证书的域名（客户端靠指纹校验）
#   PUBLIC_IP=x.x.x.x             自动探测失败时手动指定
#   SKIP_TARGET_CHECK=1           跳过伪装目标检测（不建议）
#   ROTATE=1                      重新生成全部凭据（旧线路会失效，需在后台覆盖导入）
set -euo pipefail

XRAY_VERSION="${XRAY_VERSION:-v26.9.9}"
REALITY_TARGET="${REALITY_TARGET:-dl.google.com}"
NODE_NAME="${NODE_NAME:-香港 HK1}"
COUNTRY="${COUNTRY:-HK}"
CITY="${CITY:-香港}"
REGION="${REGION:-ASIA}"
ENABLE_HY2="${ENABLE_HY2:-0}"
HY2_SNI="${HY2_SNI:-www.bing.com}"
ROTATE="${ROTATE:-0}"

XRAY_DIR=/usr/local/etc/xray
STATE="$XRAY_DIR/yuhai-node.env"
CONF="$XRAY_DIR/config.json"
HY2_DIR="$XRAY_DIR/hy2"

log()  { printf '\033[1;32m[+]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[!]\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "请用 root 运行"
. /etc/os-release
[ "${ID:-}" = "ubuntu" ] || warn "未在 Ubuntu 上测试：$PRETTY_NAME"

# ---------- 1. 系统包 ----------
log "安装依赖（curl openssl ufw fail2ban jq）"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq curl ca-certificates openssl ufw fail2ban jq >/dev/null

# ---------- 2. BBR ----------
if [ "$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null)" != "bbr" ]; then
  log "开启 BBR"
  cat >/etc/sysctl.d/99-yuhai-bbr.conf <<'S'
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
S
  sysctl --system >/dev/null
fi
log "拥塞控制：$(sysctl -n net.ipv4.tcp_congestion_control)"

# ---------- 3. Xray（官方安装脚本，固定版本） ----------
have_ver=""
command -v xray >/dev/null 2>&1 && have_ver="v$(xray version 2>/dev/null | awk 'NR==1{print $2}')"
if [ "$have_ver" != "$XRAY_VERSION" ]; then
  log "安装 Xray $XRAY_VERSION（当前：${have_ver:-未安装}）"
  bash -c "$(curl -fsSL https://github.com/XTLS/Xray-install/raw/main/install-release.sh)" @ install --version "$XRAY_VERSION"
else
  log "Xray 已是 $XRAY_VERSION"
fi
command -v xray >/dev/null || die "Xray 安装失败"

# ---------- 4. 检查 Reality 伪装目标 ----------
if [ "${SKIP_TARGET_CHECK:-0}" != "1" ]; then
  log "检查伪装目标 $REALITY_TARGET 是否支持 TLS1.3 + H2"
  out=$(echo | timeout 10 openssl s_client -connect "$REALITY_TARGET:443" -servername "$REALITY_TARGET" \
        -tls1_3 -alpn h2 2>/dev/null || true)
  echo "$out" | grep -q "TLSv1.3" || die "$REALITY_TARGET 不支持 TLS1.3（换 REALITY_TARGET=...，或 SKIP_TARGET_CHECK=1）"
  echo "$out" | grep -q "ALPN protocol: h2" || die "$REALITY_TARGET 不支持 H2（换一个目标）"
  log "伪装目标可用"
fi

# ---------- 5. 凭据（首次生成，之后沿用） ----------
mkdir -p "$XRAY_DIR"
if [ "$ROTATE" = "1" ] && [ -f "$STATE" ]; then
  warn "ROTATE=1：备份旧凭据并重新生成"
  cp -a "$STATE" "$STATE.bak-$(date +%Y%m%d%H%M%S)"
  rm -f "$STATE"
fi
if [ -f "$STATE" ]; then
  # shellcheck disable=SC1090
  . "$STATE"
  log "沿用已有凭据（$STATE）"
fi
if [ -z "${UUID:-}" ] || [ -z "${PRIV:-}" ] || [ -z "${PBK:-}" ] || [ -z "${SID:-}" ]; then
  log "生成 UUID / x25519 密钥 / shortId"
  UUID=$(xray uuid)
  kp=$(xray x25519)
  # 兼容新旧输出：PrivateKey / Private key；Password(=公钥) / Public key
  PRIV=$(echo "$kp" | awk -F': *' 'tolower($1) ~ /private/ {print $2; exit}')
  PBK=$(echo "$kp" | awk -F': *' 'tolower($1) ~ /public|password/ {print $2; exit}')
  SID=$(openssl rand -hex 8)
fi
[ -n "${HY2_PASS:-}" ] || HY2_PASS=$(openssl rand -hex 16)
[[ "$UUID" =~ ^[0-9a-f-]{36}$ ]] || die "UUID 异常：$UUID"
[[ "$PBK" =~ ^[A-Za-z0-9_-]{43}$ ]] || die "Reality 公钥解析失败，请手动运行 xray x25519 查看输出格式"
[[ "$SID" =~ ^[0-9a-f]{1,16}$ ]] || die "shortId 异常"
umask 077
cat >"$STATE" <<S
UUID=$UUID
PRIV=$PRIV
PBK=$PBK
SID=$SID
HY2_PASS=$HY2_PASS
S
chmod 600 "$STATE"
umask 022

# ---------- 6. Hysteria2 自签证书（可选） ----------
PIN=""
if [ "$ENABLE_HY2" = "1" ]; then
  mkdir -p "$HY2_DIR"
  if [ ! -s "$HY2_DIR/cert.pem" ]; then
    log "生成 Hysteria2 自签证书（$HY2_SNI，10 年）"
    openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -days 3650 -nodes \
      -keyout "$HY2_DIR/key.pem" -out "$HY2_DIR/cert.pem" \
      -subj "/CN=$HY2_SNI" -addext "subjectAltName=DNS:$HY2_SNI" 2>/dev/null
  fi
  chown root:nogroup "$HY2_DIR"/*.pem; chmod 640 "$HY2_DIR"/*.pem
  PIN=$(openssl x509 -in "$HY2_DIR/cert.pem" -outform der | sha256sum | awk '{print $1}')
fi

# ---------- 7. Xray 配置 ----------
log "写入 $CONF"
inbounds=$(jq -n --arg uuid "$UUID" --arg priv "$PRIV" --arg sid "$SID" --arg tgt "$REALITY_TARGET" '[
  {tag:"vless-reality", listen:"0.0.0.0", port:443, protocol:"vless",
   settings:{clients:[{id:$uuid, flow:"xtls-rprx-vision"}], decryption:"none"},
   streamSettings:{network:"raw", security:"reality",
     realitySettings:{show:false, target:($tgt+":443"), serverNames:[$tgt], privateKey:$priv, shortIds:[$sid]}},
   sniffing:{enabled:true, destOverride:["http","tls","quic"], routeOnly:true}}]')
if [ "$ENABLE_HY2" = "1" ]; then
  inbounds=$(echo "$inbounds" | jq --arg pw "$HY2_PASS" --arg d "$HY2_DIR" '. + [
  {tag:"hysteria2", listen:"0.0.0.0", port:443, protocol:"hysteria",
   settings:{version:2, clients:[{auth:$pw}]},
   streamSettings:{network:"hysteria", security:"tls", hysteriaSettings:{version:2},
     tlsSettings:{alpn:["h3"], certificates:[{certificateFile:($d+"/cert.pem"), keyFile:($d+"/key.pem")}]}}}]')
fi
jq -n --argjson ib "$inbounds" '{
  log:{loglevel:"warning", access:"none"},
  inbounds:$ib,
  outbounds:[{tag:"direct", protocol:"freedom"}, {tag:"block", protocol:"blackhole"}],
  routing:{domainStrategy:"AsIs", rules:[
    {type:"field", ip:["geoip:private"], outboundTag:"block"},
    {type:"field", protocol:["bittorrent"], outboundTag:"block"}]}
}' >"$XRAY_DIR/config.new.json"
chown root:nogroup "$XRAY_DIR/config.new.json"; chmod 640 "$XRAY_DIR/config.new.json"
test_out=$(xray run -test -config "$XRAY_DIR/config.new.json" 2>&1) || { echo "$test_out"; die "配置自检失败：xray run -test -config $XRAY_DIR/config.new.json"; }
echo "$test_out" | grep -i "REALITY" | while read -r l; do warn "Xray 提示：$l"; done || true
mv "$XRAY_DIR/config.new.json" "$CONF"

# ---------- 8. 防火墙 ----------
mkdir -p /run/sshd   # Ubuntu 24.04 的 ssh 走 socket 激活，缺这个目录时 sshd -T 会失败
SSH_PORT=$(sshd -T 2>/dev/null | awk '/^port /{print $2; exit}' || true); SSH_PORT=${SSH_PORT:-22}
log "防火墙：放行 ${SSH_PORT}/tcp、443/tcp$([ "$ENABLE_HY2" = 1 ] && echo '、443/udp' || true)"
ufw allow "${SSH_PORT}/tcp" >/dev/null
ufw allow 443/tcp >/dev/null
if [ "$ENABLE_HY2" = "1" ]; then ufw allow 443/udp >/dev/null; else ufw delete allow 443/udp >/dev/null 2>&1 || true; fi
ufw --force enable >/dev/null

# ---------- 9. fail2ban ----------
cat >/etc/fail2ban/jail.d/yuhai-sshd.local <<S
[sshd]
enabled  = true
backend  = systemd
port     = ${SSH_PORT}
maxretry = 5
findtime = 10m
bantime  = 1h
S
systemctl enable --now fail2ban >/dev/null 2>&1
systemctl restart fail2ban

# ---------- 10. 启动 ----------
systemctl enable xray >/dev/null 2>&1
systemctl restart xray
sleep 2
systemctl is-active --quiet xray || { journalctl -u xray -n 30 --no-pager; die "xray 未能启动"; }
[ -n "$(ss -Htln 'sport = :443')" ] || die "443/tcp 没有在监听"
log "xray 运行中"

# ---------- 11. 输出 ----------
# 公网 IP：多个探测源重试；NAT 机器的网卡地址是内网 IP，不能拿来当线路地址
IP="${PUBLIC_IP:-}"
if [ -z "$IP" ]; then
  for u in https://api.ipify.org https://ifconfig.me https://ipv4.icanhazip.com; do
    IP=$(curl -4 -fsS --max-time 8 "$u" 2>/dev/null | tr -d '[:space:]') && [[ "$IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] && break
    IP=""
  done
fi
[[ "$IP" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "探测不到公网 IP，请用 PUBLIC_IP=x.x.x.x 重跑（凭据会沿用）"
[[ "$IP" =~ ^(10\.|127\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.) ]] && die "探测到的是内网 IP $IP，请用 PUBLIC_IP=x.x.x.x 重跑"
NAME_ENC=$(jq -rn --arg s "$NODE_NAME" '$s|@uri')
VLESS_LINK="vless://${UUID}@${IP}:443?encryption=none&security=reality&type=tcp&flow=xtls-rprx-vision&sni=${REALITY_TARGET}&fp=chrome&pbk=${PBK}&sid=${SID}#${NAME_ENC}"
IMPORT=$(jq -n --arg ip "$IP" --arg uuid "$UUID" --arg name "$NODE_NAME" --arg cc "$COUNTRY" --arg city "$CITY" \
  --arg region "$REGION" --arg sni "$REALITY_TARGET" --arg pbk "$PBK" --arg sid "$SID" '[
  {protocol:"VLESS", host:$ip, port:443, uuid:$uuid, name:$name, country_code:$cc, city:$city, region:$region,
   params:{security:"reality", sni:$sni, pbk:$pbk, sid:$sid, flow:"xtls-rprx-vision", fp:"chrome", source_kind:"SELF_HOSTED"}}]')
if [ "$ENABLE_HY2" = "1" ]; then
  HY2_LINK="hysteria2://${HY2_PASS}@${IP}:443?sni=${HY2_SNI}&pin_sha256=${PIN}#$(jq -rn --arg s "$NODE_NAME HY2" '$s|@uri')"
  IMPORT=$(echo "$IMPORT" | jq --arg ip "$IP" --arg pw "$HY2_PASS" --arg name "$NODE_NAME HY2" --arg cc "$COUNTRY" \
    --arg city "$CITY" --arg region "$REGION" --arg sni "$HY2_SNI" --arg pin "$PIN" '. + [
    {protocol:"HYSTERIA2", host:$ip, port:443, password:$pw, name:$name, country_code:$cc, city:$city, region:$region,
     params:{sni:$sni, pin_sha256:$pin, source_kind:"SELF_HOSTED"}}]')
fi

cat <<OUT

================ 宇海VPN 节点参数（含凭据，勿发到聊天/群里） ================
地址        : ${IP}:443/tcp
协议        : VLESS + Reality + Vision（xtls-rprx-vision）
UUID        : ${UUID}
Reality 公钥 (pbk) : ${PBK}
shortId (sid)      : ${SID}
SNI / 伪装目标     : ${REALITY_TARGET}
指纹 (fp)          : chrome
OUT
if [ "$ENABLE_HY2" = "1" ]; then cat <<OUT
Hysteria2   : ${IP}:443/udp  口令 ${HY2_PASS}  SNI ${HY2_SNI}
证书指纹 pin_sha256 : ${PIN}
OUT
fi
cat <<OUT

--- 分享链接（v2rayN / v2rayNG / sing-box / Shadowrocket 测试用）---
${VLESS_LINK}
OUT
if [ "$ENABLE_HY2" = "1" ]; then echo "$HY2_LINK"; fi
cat <<OUT

--- 后台 admin.yhvpn.io →「出口线路」→「批量导入」，把下面整段 JSON 粘进「导入内容」---
${IMPORT}

凭据保存在 ${STATE}（root 600）。重跑本脚本会沿用；要换凭据用 ROTATE=1。
===========================================================================
OUT
