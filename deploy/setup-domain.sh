#!/usr/bin/env bash
# 宇海VPN：接入域名 yhvpn.io 并开启 https（在服务器上以 root 执行，可重复执行）
set -euo pipefail
IP="${SERVER_IP:?请先设置 SERVER_IP=<CONTROL_PLANE_IP>}"
API=api.yhvpn.io
ADMIN=admin.yhvpn.io

echo "==> 1/3 等待域名解析生效（最多约 5 分钟）"
for i in $(seq 1 60); do
  a=$(getent ahostsv4 "$API" | awk 'NR==1{print $1}' || true)
  b=$(getent ahostsv4 "$ADMIN" | awk 'NR==1{print $1}' || true)
  if [ "$a" = "$IP" ] && [ "$b" = "$IP" ]; then echo "  解析已生效"; break; fi
  [ "$i" = 60 ] && { echo "[失败] 域名还没解析到 $IP（现在：api=$a admin=$b），过几分钟再运行一次"; exit 1; }
  sleep 5
done

echo "==> 2/3 写入 Caddy 配置"
[ -f /etc/caddy/Caddyfile ] && cp /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.bak-$(date +%s)"
cat > /etc/caddy/Caddyfile <<EOF
{
	servers {
		protocols h1 h2 h3
	}
}

(secure) {
	header {
		Strict-Transport-Security "max-age=31536000"
		X-Content-Type-Options "nosniff"
		Referrer-Policy "no-referrer"
		-Server
	}
}

$API {
	import secure
	encode zstd gzip
	request_body {
		max_size 1MB
	}
	reverse_proxy 127.0.0.1:18080
}

$ADMIN {
	import secure
	encode zstd gzip
	header {
		X-Frame-Options "DENY"
		Content-Security-Policy "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
		Permissions-Policy "camera=(), microphone=(), geolocation=()"
	}
	handle /admin/v1/* {
		request_body {
			max_size 8MB
		}
		reverse_proxy 127.0.0.1:18081
	}
	handle {
		root * /opt/yuhai-vpn/admin-web
		@assets path /assets/*
		header @assets Cache-Control "public, max-age=31536000, immutable"
		header / Cache-Control "no-store"
		try_files {path} /index.html
		file_server
	}
}
EOF
caddy fmt --overwrite /etc/caddy/Caddyfile >/dev/null 2>&1 || true
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null
systemctl reload caddy

echo "==> 3/3 申请证书并自检（约 30 秒）"
ok=0
for i in $(seq 1 20); do
  c1=$(curl -s -o /dev/null -w '%{http_code}' "https://$API/healthz" || true)
  c2=$(curl -s -o /dev/null -w '%{http_code}' "https://$ADMIN/" || true)
  c3=$(curl -s -o /dev/null -w '%{http_code}' "https://$ADMIN/admin/v1/auth/me" || true)
  if [ "$c1" = 200 ] && [ "$c2" = 200 ] && [ "$c3" = 401 ]; then ok=1; break; fi
  sleep 3
done
printf '  App 接口   https://%s/healthz  %s\n' "$API" "$c1"
printf '  后台网页   https://%s/         %s\n' "$ADMIN" "$c2"
printf '  后台接口   https://%s/admin/v1  %s（401=正常，未登录）\n' "$ADMIN" "$c3"
if [ "$ok" = 1 ]; then
  echo; echo "✅ 域名和 https 已配好"
else
  echo; echo "⚠️ 还没全部通过，把上面内容发给 Claude"
  journalctl -u caddy -n 15 --no-pager | tail -n 15
fi
