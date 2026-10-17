#!/usr/bin/env bash
# 宇海VPN 后台回退：把程序和后台网页恢复成升级前那份（只有升级出问题时才用）。在 Mac 上执行：
#   bash ~/Desktop/yuhai-vpn/deploy/rollback-backend.sh
# 数据库保持不动：41 号迁移只新增了三张出口线路表，旧程序不会读它们，留着无害。
# 如确需把数据库也退回，备份在服务器 /root/yuhai-backups/<时间>/yuhai.dump，请先问 Claude。
set -euo pipefail
ssh -o ServerAliveInterval=15 vpn 'bash -s' <<'REMOTE'
set -euo pipefail
BK=$(cat /root/yuhai-backups/LATEST 2>/dev/null || true)
[ -n "$BK" ] && [ -d "$BK/bin" ] && [ -d "$BK/admin-web" ] || { echo "[失败] 找不到升级前的备份"; exit 1; }
echo "==> 恢复 $BK"
install -m 755 "$BK"/bin/* /opt/yuhai-vpn/bin/
rm -rf /opt/yuhai-vpn/admin-web.rb; cp -a "$BK/admin-web" /opt/yuhai-vpn/admin-web.rb
rm -rf /opt/yuhai-vpn/admin-web; mv /opt/yuhai-vpn/admin-web.rb /opt/yuhai-vpn/admin-web
for s in yuhai-vpn-api yuhai-vpn-admin yuhai-vpn-worker; do systemctl restart "$s"; done
sleep 3
for p in 18080 18081; do echo "  健康检查 $p $(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$p/healthz)"; done
echo "✅ 已回退到升级前的程序"
REMOTE
