-- +goose Up
-- 多协议「出口」：外购代理（SOCKS5 / HTTP / HTTPS）与自建多协议节点（Shadowsocks / Trojan /
-- VLESS / VMess / Hysteria2）的统一登记。客户端拿到出口配置后**直连出口**，香港控制面不转发流量。
--
-- 与 nodes/node_inbounds（WireGuard 自建节点 + agent 心跳）是两套独立的表：外购代理没有 agent、
-- 没有心跳，不能套用节点的「心跳新鲜才可用」门槛；硬塞进同一张表只会让两边的约束都变松。
--
-- 机密（账号/密码/UUID/口令、订阅地址）一律字段加密：AES-256-GCM，AAD = 表.列.主键。
-- 列表、审计、日志只出现 identity_hint（如 "ab***"）与主机端口。

INSERT INTO admin_permissions(code,name,risk_level) VALUES
 ('egress.read','查看出口与订阅列表（凭据脱敏）',0),
 ('egress.write','导入、修改、启停出口与订阅',1)
 ON CONFLICT(code) DO NOTHING;

-- 超级管理员角色本就是「全部权限（不含开发工具）」，新能力同步给它；其它角色仍需单独授予。
INSERT INTO admin_role_permissions(role_id,permission_code)
 SELECT r.id, p.code FROM admin_roles r CROSS JOIN (VALUES ('egress.read'),('egress.write')) AS p(code)
  WHERE r.code='super_admin'
 ON CONFLICT DO NOTHING;

CREATE TABLE egress_subscriptions (
  id              BIGSERIAL    PRIMARY KEY,
  name            TEXT         NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
  url_enc         BYTEA        NOT NULL,
  url_hint        TEXT         NOT NULL CHECK (char_length(url_hint) BETWEEN 1 AND 253),
  default_protocol TEXT        NOT NULL DEFAULT 'SOCKS5' CHECK (default_protocol IN ('SOCKS5','HTTP','HTTPS')),
  country_code    CHAR(2)      CHECK (country_code ~ '^[A-Z]{2}$'),
  region          TEXT         NOT NULL DEFAULT '' CHECK (char_length(region) <= 32),
  min_tier        INT          NOT NULL DEFAULT 1 CHECK (min_tier BETWEEN 1 AND 9),
  status          TEXT         NOT NULL DEFAULT 'ENABLED' CHECK (status IN ('ENABLED','DISABLED')),
  last_fetched_at TIMESTAMPTZ,
  last_result     TEXT         NOT NULL DEFAULT '' CHECK (char_length(last_result) <= 200),
  created_at      TIMESTAMPTZ  NOT NULL,
  updated_at      TIMESTAMPTZ  NOT NULL
);

CREATE TABLE egress_endpoints (
  id              BIGSERIAL    PRIMARY KEY,
  name            TEXT         NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
  protocol        TEXT         NOT NULL CHECK (protocol IN ('SOCKS5','HTTP','HTTPS','SHADOWSOCKS','TROJAN','VLESS','VMESS','HYSTERIA2')),
  host            TEXT         NOT NULL CHECK (char_length(host) BETWEEN 1 AND 253),
  port            INT          NOT NULL CHECK (port BETWEEN 1 AND 65535),
  country_code    CHAR(2)      CHECK (country_code ~ '^[A-Z]{2}$'),
  city            TEXT         NOT NULL DEFAULT '' CHECK (char_length(city) <= 64),
  region          TEXT         NOT NULL DEFAULT '' CHECK (char_length(region) <= 32),
  kind            TEXT         NOT NULL DEFAULT 'THIRD_PARTY' CHECK (kind IN ('THIRD_PARTY','SELF_HOSTED')),
  supports_udp    BOOLEAN      NOT NULL DEFAULT false,
  -- HTTP/HTTPS 代理没有 UDP：库里也钉住，防止被改成「支持」。
  CHECK (NOT (protocol IN ('HTTP','HTTPS') AND supports_udp)),
  params          JSONB        NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(params) = 'object'),
  secret_enc      BYTEA,
  identity_hint   TEXT         NOT NULL DEFAULT '' CHECK (char_length(identity_hint) <= 16),
  fingerprint     CHAR(64)     NOT NULL UNIQUE CHECK (fingerprint ~ '^[a-f0-9]{64}$'),
  min_tier        INT          NOT NULL DEFAULT 1 CHECK (min_tier BETWEEN 1 AND 9),
  status          TEXT         NOT NULL DEFAULT 'DISABLED' CHECK (status IN ('ENABLED','DISABLED')),
  sort_order      INT          NOT NULL DEFAULT 0,
  source          TEXT         NOT NULL CHECK (source IN ('MANUAL','IMPORT','SUBSCRIPTION')),
  subscription_id BIGINT       REFERENCES egress_subscriptions(id),
  CHECK ((source = 'SUBSCRIPTION') = (subscription_id IS NOT NULL)),
  created_at      TIMESTAMPTZ  NOT NULL,
  updated_at      TIMESTAMPTZ  NOT NULL
);

CREATE INDEX egress_endpoints_list_idx ON egress_endpoints (status, min_tier, sort_order, id);
CREATE INDEX egress_endpoints_country_idx ON egress_endpoints (country_code, protocol);

CREATE TABLE admin_egress_mutations (
  admin_user_id BIGINT      NOT NULL REFERENCES admin_users(id),
  operation_id  UUID        NOT NULL PRIMARY KEY,
  action_code   TEXT        NOT NULL CHECK (action_code IN ('egress.import','egress.update','egress.subscription_create','egress.subscription_refresh')),
  request_hash  CHAR(64)    NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  target_id     BIGINT      NOT NULL CHECK (target_id > 0),
  response      JSON        NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX admin_egress_mutations_admin_idx ON admin_egress_mutations (admin_user_id, created_at);

-- 权限：App 只读出口（下发时解密）；订阅地址与后台幂等表与 App 无关；出口只停用不删除。
REVOKE ALL ON admin_egress_mutations, egress_subscriptions FROM yuhai_app, PUBLIC;
REVOKE ALL ON egress_endpoints FROM yuhai_app, PUBLIC;
GRANT SELECT ON egress_endpoints TO yuhai_app;
REVOKE DELETE, TRUNCATE ON egress_endpoints, egress_subscriptions FROM yuhai_admin;
REVOKE ALL ON egress_subscriptions, admin_egress_mutations FROM yuhai_readonly;

-- +goose Down
DROP TABLE admin_egress_mutations;
DROP TABLE egress_endpoints;
DROP TABLE egress_subscriptions;
DELETE FROM admin_role_permissions WHERE permission_code IN ('egress.read','egress.write');
DELETE FROM admin_permissions WHERE code IN ('egress.read','egress.write');
