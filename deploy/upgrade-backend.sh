#!/usr/bin/env bash
# 宇海VPN 后台升级到 41 号迁移（出口线路 / 多协议）。在你的 Mac 上执行：
#   bash ~/Desktop/yuhai-vpn/deploy/upgrade-backend.sh
# 做的事：校验安装包 → 上传 → 服务器先备份数据库和旧程序 → 安装 → 自检。
# 不碰资金私钥：安装所需的收款公钥直接沿用服务器上已有的配置。
set -euo pipefail
cd "$(dirname "$0")"

PKG=yuhai-vpn-backend-41.tar.xz
WANT=4fea42e3fa659f22da003c9029d47409f01a974cd8bfdad113e43d26511f281b
DIR=/root/yuhai-upgrade-41

echo "==> 1/4 合并并校验安装包"
cat "$PKG".part00 "$PKG".part01 > "/tmp/$PKG"
GOT=$(shasum -a 256 "/tmp/$PKG" | awk '{print $1}')
[ "$GOT" = "$WANT" ] || { echo "[失败] 安装包校验不对，停止（什么都没改）"; exit 1; }
echo "  校验通过"

echo "==> 2/4 上传到服务器（约 1 分钟，卡住会自动重试）"
ssh -o ServerAliveInterval=15 vpn "mkdir -p $DIR"
for i in 1 2 3 4 5 6; do
  if rsync --partial --timeout=60 -e "ssh -o ServerAliveInterval=15" "/tmp/$PKG" "vpn:$DIR/$PKG"; then break; fi
  [ "$i" = 6 ] && { echo "[失败] 上传多次中断，停止（服务器没有改动）"; exit 1; }
  echo "  重试 $i ..."; sleep 3
done

echo "==> 3/4 服务器：备份 → 安装（约 2 分钟）"
ssh -o ServerAliveInterval=15 vpn "bash -s" -- "$DIR" "$PKG" "$WANT" <<'REMOTE'
set -euo pipefail
DIR=$1; PKG=$2; WANT=$3
cd "$DIR"
[ "$(sha256sum "$PKG" | awk '{print $1}')" = "$WANT" ] || { echo "[失败] 服务器上的包校验不对"; exit 1; }

TS=$(date +%Y%m%d-%H%M%S)
BK=/root/yuhai-backups/$TS
mkdir -p "$BK"; chmod 700 /root/yuhai-backups "$BK"
sudo -u postgres pg_dump -Fc yuhai > "$BK/yuhai.dump"
cp -a /opt/yuhai-vpn/bin "$BK/bin"
cp -a /opt/yuhai-vpn/admin-web "$BK/admin-web"
BEFORE=$(sudo -u postgres psql -d yuhai -tAc "SELECT count(*) FROM yuhai_ops.schema_migrations")
echo "$BK" > /root/yuhai-backups/LATEST
echo "  备份完成：$BK（数据库 $(du -h "$BK/yuhai.dump" | cut -f1)，迁移 $BEFORE 个）"

rm -rf x; mkdir x; tar -xJf "$PKG" -C x
FUND=$(grep '^FUND_ADDRESS_PUBKEY_B64=' /etc/yuhai-vpn/app.env | cut -d= -f2-)
[ -n "$FUND" ] || { echo "[失败] 服务器上找不到收款公钥配置，停止（只做了备份）"; exit 1; }
bash x/install.sh "$FUND"
REMOTE

echo "==> 4/4 升级后核对"
ssh -o ServerAliveInterval=15 vpn 'bash -s' <<'REMOTE'
N=$(sudo -u postgres psql -d yuhai -tAc "SELECT count(*) FROM yuhai_ops.schema_migrations")
T=$(sudo -u postgres psql -d yuhai -tAc "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('egress_endpoints','egress_subscriptions')")
A=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18080/v1/egress)
B=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18081/admin/v1/egress)
echo "  迁移总数 $N（应为 41）；出口线路表 $T（应为 2）"
echo "  App 线路接口 $A、后台线路接口 $B（应为 401 或 403，表示接口已存在且要求登录；404 表示没装上）"
if [ "$N" = 41 ] && [ "$T" = 2 ] && case "$A$B" in 401401|401403|403401|403403) true;; *) false;; esac; then
  echo; echo "✅ 升级完成"
else
  echo; echo "⚠️ 核对没全过，把上面内容发给 Claude"
fi
REMOTE
rm -f "/tmp/$PKG"
