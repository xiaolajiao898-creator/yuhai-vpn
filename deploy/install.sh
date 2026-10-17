#!/usr/bin/env bash
# 宇海VPN 后台一键安装（Ubuntu 24.04，root 执行，可重复执行）
#   bash install.sh <资金签名公钥Base64>
# 已生成的密钥和数据库口令不会被覆盖；数据库迁移只执行尚未执行过的部分。
set -euo pipefail
cd "$(dirname "$0")"

FUND_PUB="${1:-}"
PGPORT="${PGPORT:-5432}"
PSQL_SU="${PSQL_SU:-sudo -u postgres psql -p $PGPORT}"
REHEARSAL="${REHEARSAL:-0}"       # 1 = 只演练数据库部分（不装软件、不动系统服务）
ETC=/etc/yuhai-vpn
OPT=/opt/yuhai-vpn
[ "$REHEARSAL" = 1 ] && ETC="${ETC_OVERRIDE:?}" && OPT="${OPT_OVERRIDE:?}"

say() { printf '\n==> %s\n' "$*"; }
die() { printf '\n[失败] %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = 0 ] || die "请用 root 执行"
if ! printf '%s' "$FUND_PUB" | base64 -d 2>/dev/null | wc -c | grep -qx 32; then
  die "资金签名公钥不对（应为 44 位 Base64）"
fi

if [ "$REHEARSAL" != 1 ]; then
  say "1/8 安装系统组件（约 60 秒）"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq postgresql redis-server ufw qrencode curl gnupg debian-keyring debian-archive-keyring apt-transport-https unattended-upgrades >/dev/null
  if ! command -v caddy >/dev/null; then
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/gpg.key | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -1sLf https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -qq
    apt-get install -y -qq caddy >/dev/null
  fi
  systemctl enable --now postgresql redis-server >/dev/null 2>&1
  id yuhai >/dev/null 2>&1 || useradd --system --home-dir /nonexistent --shell /usr/sbin/nologin yuhai
fi

say "2/8 生成密钥（已有则保留）"
install -d -m 750 -o root -g "$( [ "$REHEARSAL" = 1 ] && echo root || echo yuhai )" "$ETC"
SECRETS="$ETC/secrets.env"
if [ ! -s "$SECRETS" ]; then
  umask 077
  cat > "$SECRETS" <<EOF
SECRET_PEPPER_HEX=$(openssl rand -hex 32)
JWT_ED25519_SEED_HEX=$(openssl rand -hex 32)
FIELD_ENC_KEY_HEX=$(openssl rand -hex 32)
PG_MIGRATOR_PW=$(openssl rand -hex 24)
PG_APP_PW=$(openssl rand -hex 24)
PG_ADMIN_PW=$(openssl rand -hex 24)
PG_READONLY_PW=$(openssl rand -hex 24)
EOF
  umask 022
  echo "已生成新密钥：$SECRETS（请勿外传）"
else
  echo "沿用已有密钥"
fi
chmod 600 "$SECRETS"
# shellcheck disable=SC1090
. "$SECRETS"

say "3/8 数据库角色与权限"
$PSQL_SU -v ON_ERROR_STOP=1 -q <<SQL
DO \$\$
DECLARE r text;
BEGIN
  FOREACH r IN ARRAY ARRAY['yuhai_migrator','yuhai_app','yuhai_admin','yuhai_readonly'] LOOP
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=r) THEN
      EXECUTE format('CREATE ROLE %I LOGIN', r);
    END IF;
  END LOOP;
END \$\$;
ALTER ROLE yuhai_migrator PASSWORD '$PG_MIGRATOR_PW';
ALTER ROLE yuhai_app      PASSWORD '$PG_APP_PW';
ALTER ROLE yuhai_admin    PASSWORD '$PG_ADMIN_PW';
ALTER ROLE yuhai_readonly PASSWORD '$PG_READONLY_PW';
SQL
if ! $PSQL_SU -tAc "SELECT 1 FROM pg_database WHERE datname='yuhai'" | grep -q 1; then
  $PSQL_SU -v ON_ERROR_STOP=1 -q -c "CREATE DATABASE yuhai OWNER yuhai_migrator ENCODING 'UTF8' TEMPLATE template0"
fi
$PSQL_SU -d yuhai -v ON_ERROR_STOP=1 -q <<'SQL'
REVOKE ALL ON DATABASE yuhai FROM PUBLIC;
GRANT CONNECT ON DATABASE yuhai TO yuhai_migrator, yuhai_app, yuhai_admin, yuhai_readonly;
GRANT ALL ON SCHEMA public TO yuhai_migrator;
GRANT USAGE ON SCHEMA public TO yuhai_app, yuhai_admin, yuhai_readonly;
ALTER DEFAULT PRIVILEGES FOR ROLE yuhai_migrator IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE ON TABLES TO yuhai_app, yuhai_admin;
ALTER DEFAULT PRIVILEGES FOR ROLE yuhai_migrator IN SCHEMA public
  GRANT SELECT ON TABLES TO yuhai_readonly;
ALTER DEFAULT PRIVILEGES FOR ROLE yuhai_migrator IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO yuhai_app, yuhai_admin;
SQL

say "4/8 数据库迁移（只执行未执行过的）"
export PGPASSWORD="$PG_MIGRATOR_PW"
MIG="psql -h 127.0.0.1 -p $PGPORT -U yuhai_migrator -d yuhai -v ON_ERROR_STOP=1 -q"
$MIG -c "SET client_min_messages=warning; CREATE SCHEMA IF NOT EXISTS yuhai_ops; CREATE TABLE IF NOT EXISTS yuhai_ops.schema_migrations(version bigint PRIMARY KEY, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())"
applied=0
for f in migrations/*.sql; do
  name=$(basename "$f")
  ver=$((10#${name%%_*}))
  if $MIG -tAc "SELECT 1 FROM yuhai_ops.schema_migrations WHERE version=$ver" | grep -q 1; then continue; fi
  grep -q '^-- +goose Up' "$f" || die "$name 不是标准迁移文件"
  # 只取 Up 段：遇到 Down 标记就停止（整段 Down 绝不执行）
  { sed '/^-- +goose Down/,$d' "$f"; echo "INSERT INTO yuhai_ops.schema_migrations(version,name) VALUES ($ver,'$name');"; } \
    | $MIG -1 -f - || die "迁移 $name 失败（已整体回滚）"
  echo "  已执行 $name"; applied=$((applied+1))
done
echo "本次执行 $applied 个迁移；共 $($MIG -tAc 'SELECT count(*) FROM yuhai_ops.schema_migrations') 个"
unset PGPASSWORD

say "5/8 写入运行配置"
install -d -m 755 "$OPT/bin" "$OPT/admin-web"
install -m 755 bin/* "$OPT/bin/"
rm -rf "$OPT/admin-web.new"; cp -r admin-web "$OPT/admin-web.new"
rm -rf "$OPT/admin-web.old"; [ -d "$OPT/admin-web" ] && mv "$OPT/admin-web" "$OPT/admin-web.old"
mv "$OPT/admin-web.new" "$OPT/admin-web"
DBURL() { printf 'postgres://%s:%s@127.0.0.1:%s/yuhai?sslmode=disable' "$1" "$2" "$PGPORT"; }
common() {
  cat <<EOF
APP_ENV=production
REDIS_URL=redis://127.0.0.1:6379/0
API_LISTEN=127.0.0.1:18080
ADMIN_LISTEN=127.0.0.1:18081
NODE_API_LISTEN=127.0.0.1:18443
TRUSTED_PROXIES=127.0.0.1,::1
SECRET_PEPPER_HEX=$SECRET_PEPPER_HEX
JWT_ED25519_SEED_HEX=$JWT_ED25519_SEED_HEX
JWT_KEY_ID=jwt-prod-1
FIELD_ENC_KEY_HEX=$FIELD_ENC_KEY_HEX
FIELD_ENC_KEY_ID=fek-prod-1
FUND_ADDRESS_PUBKEY_B64=$FUND_PUB
FUND_ADDRESS_KEY_ID=fund-prod-1
USDT_TRC20_CONTRACT=TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t
DEV_TOOLS_ENABLED=false
INVITE_REWARDS_ENABLED=false
NODE_WG_RECEIPTS_ENABLED=false
WG_ADDRESS_POOL=10.20.0.0/16
WG_ADDRESS_RESERVED=16
EOF
  # 可选的附加配置（例如找回密码的官方收款地址、TronGrid 密钥），每行 KEY=VALUE
  if [ -s "$ETC/extra.env" ]; then grep -E '^[A-Z_][A-Z0-9_]*=' "$ETC/extra.env"; fi
}
umask 027
{ common; echo "DATABASE_URL=$(DBURL yuhai_app "$PG_APP_PW")"; } > "$ETC/app.env"
{ common; echo "DATABASE_URL=$(DBURL yuhai_admin "$PG_ADMIN_PW")"; } > "$ETC/admin.env"
{ common; echo "MIGRATE_DATABASE_URL=$(DBURL yuhai_migrator "$PG_MIGRATOR_PW")"; } > "$ETC/init.env"
umask 022
chmod 640 "$ETC/app.env" "$ETC/admin.env"; chmod 600 "$ETC/init.env"
[ "$REHEARSAL" = 1 ] || chgrp yuhai "$ETC/app.env" "$ETC/admin.env"

if [ "$REHEARSAL" = 1 ]; then say "演练结束（数据库部分完成）"; exit 0; fi

say "6/8 注册系统服务"
unit() { # 名称 程序 配置文件 说明
  cat > "/etc/systemd/system/$1.service" <<EOF
[Unit]
Description=$4
After=network-online.target postgresql.service redis-server.service
Wants=network-online.target

[Service]
User=yuhai
Group=yuhai
EnvironmentFile=$3
ExecStart=$OPT/bin/$2
Restart=always
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
LockPersonality=true
CapabilityBoundingSet=
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF
}
unit yuhai-vpn-api    yuhai-api       "$ETC/app.env"   "YuhaiVPN App API"
unit yuhai-vpn-admin  yuhai-admin-api "$ETC/admin.env" "YuhaiVPN Admin API"
unit yuhai-vpn-worker yuhai-worker    "$ETC/app.env"   "YuhaiVPN Worker"
systemctl daemon-reload
for s in yuhai-vpn-api yuhai-vpn-admin yuhai-vpn-worker; do systemctl enable "$s" >/dev/null 2>&1; systemctl restart "$s"; done

# 只在全新安装时写占位；已接入域名（或已有标记）就绝不覆盖
if [ ! -e /etc/caddy/Caddyfile.yuhai ] && ! grep -q "yhvpn.io" /etc/caddy/Caddyfile 2>/dev/null; then
  cat > /etc/caddy/Caddyfile <<'EOF'
# 域名接入前的占位：不对外暴露任何后台
:80 {
	respond "ok" 200
}
EOF
  touch /etc/caddy/Caddyfile.yuhai
  systemctl reload caddy || systemctl restart caddy
fi

say "7/8 服务器安全加固"
cat > /etc/ssh/sshd_config.d/00-vpn-key.conf <<'EOF'
PubkeyAuthentication yes
AuthenticationMethods publickey
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
MaxAuthTries 3
EOF
sshd -t && systemctl reload ssh
ufw --force default deny incoming >/dev/null
ufw --force default allow outgoing >/dev/null
ufw allow 22/tcp >/dev/null; ufw allow 80/tcp >/dev/null; ufw allow 443/tcp >/dev/null; ufw allow 443/udp >/dev/null
ufw --force enable >/dev/null
dpkg-reconfigure -f noninteractive unattended-upgrades >/dev/null 2>&1 || true

say "8/8 自检"
sleep 3
ok=1
for s in yuhai-vpn-api yuhai-vpn-admin yuhai-vpn-worker postgresql redis-server caddy; do
  st=$(systemctl is-active "$s" || true); printf '  %-18s %s\n' "$s" "$st"; [ "$st" = active ] || ok=0
done
for p in 18080 18081; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$p/healthz" || true)
  printf '  健康检查 %-6s %s\n' "$p" "$code"; [ "$code" = 200 ] || ok=0
done
if [ "$ok" = 1 ]; then
  echo; echo "✅ 宇海VPN 后台安装完成"
else
  echo; echo "⚠️ 有服务没起来，把上面内容发给 Claude"
  for s in yuhai-vpn-api yuhai-vpn-admin yuhai-vpn-worker; do journalctl -u "$s" -n 8 --no-pager 2>/dev/null | tail -n 8; done
fi
